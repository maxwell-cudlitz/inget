// The driver-specific configuration blocks: what to enumerate, and how fast.
//
// internal/config carries these as raw maps because their keys differ per driver, so this
// is where they become typed. Decoding is strict, which is the point of doing it here at all:
// a misspelled include_forks fails the run instead of quietly enumerating every fork in an
// organisation.
package github

import (
	"fmt"
	"strings"
	"time"

	"github.com/maxwellcudlitz/inget/internal/config"
)

// Visibility values the domain block accepts, matching the GitHub API's own vocabulary.
const (
	VisibilityAll     = "all"
	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

// Defaults for the limits block. They are deliberately conservative: the cost of being too
// slow is a longer run, and the cost of being too fast is a secondary rate limit that
// pauses everything.
const (
	defaultTarballMaxBytes int64 = 32 << 20 // 32 MiB compressed archive
	defaultMaxConcurrent         = 4
)

// domain is what to enumerate. Orgs, Repos and Topics are additive: a repository reached by
// more than one of them is enumerated once.
type domain struct {
	Orgs            []string `mapstructure:"orgs"`
	Repos           []string `mapstructure:"repos"`  // explicit owner/name entries
	Topics          []string `mapstructure:"topics"` // search API topic: qualifiers
	Visibility      string   `mapstructure:"visibility"`
	IncludeArchived bool     `mapstructure:"include_archived"`
	IncludeForks    bool     `mapstructure:"include_forks"`
	MaxInactiveDays int      `mapstructure:"max_inactive_days"`
}

// limits is how fast, how much, and whether to scan.
//
// SecretScan belongs here rather than in domain because it does not change which items are
// enumerated, only what their fragments are allowed to carry. It defaults to on: a fetch
// that silently published credentials because a key was absent would be the worst possible
// default.
type limits struct {
	RequestsPerSecond float64 `mapstructure:"requests_per_second"`
	MaxConcurrent     int     `mapstructure:"max_concurrent"`
	TarballMaxBytes   int64   `mapstructure:"tarball_max_bytes"`
	SecretScan        *bool   `mapstructure:"secret_scan"`
}

// decodeDomain decodes and validates the domain block.
func decodeDomain(block map[string]any) (domain, error) {
	var d domain
	if err := config.DecodeInto(block, &d); err != nil {
		return domain{}, fmt.Errorf("github domain: %w", err)
	}
	if d.Visibility == "" {
		d.Visibility = VisibilityAll
	}
	switch d.Visibility {
	case VisibilityAll, VisibilityPublic, VisibilityPrivate:
	default:
		return domain{}, fmt.Errorf("github domain.visibility = %q, want %s, %s or %s",
			d.Visibility, VisibilityAll, VisibilityPublic, VisibilityPrivate)
	}
	if d.MaxInactiveDays < 0 {
		return domain{}, fmt.Errorf("github domain.max_inactive_days = %d, want 0 to disable or a positive number of days", d.MaxInactiveDays)
	}
	for _, name := range d.Repos {
		if strings.Count(name, "/") != 1 {
			return domain{}, fmt.Errorf("github domain.repos entry %q is not owner/name", name)
		}
	}
	if len(d.Orgs) == 0 && len(d.Repos) == 0 && len(d.Topics) == 0 {
		return domain{}, fmt.Errorf("github domain enumerates nothing: set orgs, repos or topics")
	}
	return d, nil
}

// decodeLimits decodes the limits block and fills in the defaults for what it omits.
func decodeLimits(block map[string]any) (limits, error) {
	var l limits
	if err := config.DecodeInto(block, &l); err != nil {
		return limits{}, fmt.Errorf("github limits: %w", err)
	}
	if l.MaxConcurrent <= 0 {
		l.MaxConcurrent = defaultMaxConcurrent
	}
	if l.TarballMaxBytes <= 0 {
		l.TarballMaxBytes = defaultTarballMaxBytes
	}
	if l.SecretScan == nil {
		on := true
		l.SecretScan = &on
	}
	return l, nil
}

// scanSecrets reports whether secret detection is enabled.
func (l limits) scanSecrets() bool { return l.SecretScan == nil || *l.SecretScan }

// keep applies the domain filters to one repository, returning the reason it was excluded
// when it was. The reason is returned rather than logged so the caller decides whether a
// skipped repository is worth a line.
//
// A repository with no default branch has never been pushed to and has nothing to fragment,
// which is a different thing from being inactive and so is named separately.
func (d domain) keep(r repo, now time.Time) (bool, string) {
	switch {
	case r.Archived && !d.IncludeArchived:
		return false, "archived"
	case r.Fork && !d.IncludeForks:
		return false, "fork"
	case r.DefaultBranch == "":
		return false, "empty repository"
	}
	if d.Visibility != VisibilityAll && r.visibility() != d.Visibility {
		return false, "visibility " + r.visibility()
	}
	if d.MaxInactiveDays > 0 && !r.PushedAt.IsZero() {
		cutoff := now.AddDate(0, 0, -d.MaxInactiveDays)
		if r.PushedAt.Before(cutoff) {
			return false, fmt.Sprintf("inactive since %s", r.PushedAt.UTC().Format(time.RFC3339))
		}
	}
	return true, ""
}
