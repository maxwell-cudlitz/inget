// Enumeration: what repositories the configured domain contains.
//
// Three additive sources — organisations, explicit owner/name entries, and topic searches —
// deduplicated by full name so a repository reachable more than one way is enumerated once.
// Organisation listings are requested sorted by push time descending, which makes --since a
// stopping condition rather than a filter: once a page's first repository is older than the
// cutoff, nothing later in that organisation can be newer.
//
// The level-0 fingerprint is pushed_at. It is compared for inequality only, so a repository
// whose timestamp moved is fetched and one whose timestamp did not is skipped without a
// single further request.
package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/maxwellcudlitz/inget/internal/source"
)

// perPage is the API maximum for every collection this connector walks. Fewer, larger pages
// is strictly fewer requests against the same quota.
const perPage = 100

// repo is the subset of the repository resource this connector reads. Everything here either
// filters the repository, fingerprints it, or becomes vector metadata.
type repo struct {
	FullName      string    `json:"full_name"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Language      string    `json:"language"`
	Topics        []string  `json:"topics"`
	HTMLURL       string    `json:"html_url"`
	DefaultBranch string    `json:"default_branch"`
	PushedAt      time.Time `json:"pushed_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Visibility    string    `json:"visibility"`
	Private       bool      `json:"private"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
}

// visibility normalizes the two ways the API reports it. Newer responses carry an explicit
// visibility; older ones and some search results carry only private.
func (r repo) visibility() string {
	if r.Visibility != "" {
		return r.Visibility
	}
	if r.Private {
		return VisibilityPrivate
	}
	return VisibilityPublic
}

// fingerprint is the level-0 change token: the last push, rendered as a stable string. An
// unset timestamp yields "", which obliges the consumer to treat the item as changed.
func (r repo) fingerprint() string {
	if r.PushedAt.IsZero() {
		return ""
	}
	return r.PushedAt.UTC().Format(time.RFC3339)
}

// metadata is the flat string map the record carries. It becomes vector metadata and prompt
// template variables, and it is untrusted third-party text in both roles.
func (r repo) metadata() map[string]string {
	return map[string]string{
		"name":           r.Name,
		"full_name":      r.FullName,
		"description":    r.Description,
		"language":       r.Language,
		"topics":         strings.Join(r.Topics, ","),
		"url":            r.HTMLURL,
		"default_branch": r.DefaultBranch,
		"updated_at":     r.UpdatedAt.UTC().Format(time.RFC3339),
		"visibility":     r.visibility(),
	}
}

// List implements source.Connector.
func (c *Connector) List(ctx context.Context, datatype string, q source.ListQuery, yield func(source.Ref) error) error {
	if err := c.checkDatatype(datatype); err != nil {
		return err
	}
	e := &enumerator{conn: c, query: q, yield: yield, seen: map[string]bool{}, now: time.Now()}
	if len(q.Only) > 0 {
		return e.only(ctx)
	}
	return e.domain(ctx)
}

// enumerator carries the state one List call accumulates: which repositories have been
// yielded, and how many remain before --limit is reached.
type enumerator struct {
	conn  *Connector
	query source.ListQuery
	yield func(source.Ref) error
	seen  map[string]bool
	count int
	now   time.Time
}

// only enumerates the explicitly named items.
//
// Domain filters are deliberately not applied: the caller named these repositories, by
// --only or by a webhook, and silently dropping one because config excludes forks would
// leave an operator with an empty run and no reason for it. A repository that no longer
// exists is logged and skipped rather than failing the run, because a webhook for a deleted
// repository is a normal event.
func (e *enumerator) only(ctx context.Context) error {
	for _, id := range e.query.Only {
		var r repo
		if err := e.conn.client.getJSON(ctx, "repos/"+id, &r); err != nil {
			if errors.Is(err, errNotFound) {
				slog.WarnContext(ctx, "skipping repository that no longer exists", "repo", id)
				continue
			}
			return err
		}
		if r.DefaultBranch == "" {
			slog.WarnContext(ctx, "skipping repository with no default branch", "repo", id)
			continue
		}
		if err := e.emit(r); err != nil {
			return err
		}
	}
	return nil
}

// domain enumerates organisations, explicit entries and topic searches, in that order.
func (e *enumerator) domain(ctx context.Context) error {
	d := e.conn.domain
	for _, org := range d.Orgs {
		if err := e.paginateRepos(ctx, e.orgRef(org)); err != nil {
			return err
		}
	}
	for _, name := range d.Repos {
		var r repo
		if err := e.conn.client.getJSON(ctx, "repos/"+name, &r); err != nil {
			if errors.Is(err, errNotFound) {
				slog.WarnContext(ctx, "configured repository does not exist", "repo", name)
				continue
			}
			return err
		}
		if err := e.filterAndEmit(ctx, r); err != nil {
			return err
		}
	}
	for _, topic := range d.Topics {
		if err := e.paginateSearch(ctx, e.topicRef(topic)); err != nil {
			return err
		}
	}
	return nil
}

// orgRef builds the organisation listing reference. Sorting by push time descending is what
// makes --since a stopping condition.
func (e *enumerator) orgRef(org string) string {
	query := url.Values{
		"per_page":  {fmt.Sprint(perPage)},
		"type":      {"all"},
		"sort":      {"pushed"},
		"direction": {"desc"},
	}
	return "orgs/" + url.PathEscape(org) + "/repos?" + query.Encode()
}

// topicRef builds the search reference for one topic. The search API caps total results at
// 1000 regardless of pagination, which is a limit of the endpoint and not of this code.
func (e *enumerator) topicRef(topic string) string {
	query := url.Values{
		"q":        {"topic:" + topic},
		"per_page": {fmt.Sprint(perPage)},
	}
	return "search/repositories?" + query.Encode()
}

// filterAndEmit applies the domain filters and yields what survives.
func (e *enumerator) filterAndEmit(ctx context.Context, r repo) error {
	if keep, reason := e.conn.domain.keep(r, e.now); !keep {
		slog.DebugContext(ctx, "skipping repository", "repo", r.FullName, "reason", reason)
		return nil
	}
	return e.emit(r)
}

// emit deduplicates, applies --limit, and hands the ref to the caller. Reaching the limit
// returns source.ErrStopList, which propagates out of List unchanged.
func (e *enumerator) emit(r repo) error {
	key := strings.ToLower(r.FullName)
	if key == "" || e.seen[key] {
		return nil
	}
	e.seen[key] = true
	if err := e.yield(source.Ref{ID: r.FullName, Fingerprint: r.fingerprint()}); err != nil {
		return err
	}
	e.count++
	if e.query.Limit > 0 && e.count >= e.query.Limit {
		return source.ErrStopList
	}
	return nil
}

// stale reports whether a repository is older than the --since cutoff.
func (e *enumerator) stale(r repo) bool {
	return !e.query.Since.IsZero() && !r.PushedAt.IsZero() && r.PushedAt.Before(e.query.Since)
}
