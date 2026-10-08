// Fetch: one repository into one Result.
//
// The order is what makes it cheap. The tree endpoint comes first, because it names every path
// and its exact content hash for one request; the noise filter then runs against paths alone, so
// the archive is asked for only the files that survived. Content is examined for secrets before
// it becomes a fragment, and oversized files are split rather than dropped.
//
// Nothing here writes a blob or reads state. Which fragments still need uploading is a question
// about the artifact store and the previous run, and internal/fetch owns both.
package github

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/maxwell-cudlitz/inget/internal/source"
)

// warnedPathLimit bounds how many individual paths a warning names before it summarizes. A
// manifest is read by a person; a thousand-line warnings array is not read at all.
const warnedPathLimit = 5

// Fetch implements source.Connector.
func (c *Connector) Fetch(ctx context.Context, datatype string, ref source.Ref) (source.Result, error) {
	if err := c.checkDatatype(datatype); err != nil {
		return source.Result{}, err
	}
	var r repo
	if err := c.client.getJSON(ctx, "repos/"+ref.ID, &r); err != nil {
		return source.Result{}, fmt.Errorf("reading repository %s: %w", ref.ID, err)
	}
	item := source.Item{ID: r.FullName, Fingerprint: r.fingerprint(), Metadata: r.metadata()}
	if item.ID == "" {
		item.ID = ref.ID
	}
	if r.DefaultBranch == "" {
		// An empty repository is a valid item with nothing in it, not a failure.
		return source.Result{Item: item, Warnings: []string{item.ID + ": repository has no default branch"}}, nil
	}

	tree, err := c.tree(ctx, item.ID, r.DefaultBranch)
	if err != nil {
		if errors.Is(err, errEmptyRepository) {
			return source.Result{Item: item, Warnings: []string{item.ID + ": repository is empty; no files to index"}}, nil
		}
		if errors.Is(err, errNotFound) {
			return source.Result{Item: item, Warnings: []string{item.ID + ": no tree for branch " + r.DefaultBranch}}, nil
		}
		return source.Result{}, err
	}

	candidates := keepCandidates(tree.blobs())
	result := source.Result{Item: item}
	if tree.Truncated {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%s: the tree API truncated its response at %d entries; some paths are missing",
				item.ID, len(tree.Tree)))
	}
	if len(candidates) == 0 {
		return result, nil
	}

	budget := splitBudget(c.fragmentMaxBytes)
	arch, err := c.tarball(ctx, item.ID, r.DefaultBranch, func(p string) bool {
		_, ok := candidates[p]
		return ok
	}, budget)
	if err != nil {
		return source.Result{}, err
	}
	if arch.truncated {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%s: archive extraction stopped at tarball_max_bytes; some content is missing", item.ID))
	}

	fragments, notes := c.fragments(candidates, arch)
	result.Fragments = fragments
	result.Warnings = append(result.Warnings, notes.warnings(item.ID)...)
	return result, nil
}

// keepCandidates reduces a tree to the paths worth indexing, keyed by path so archive extraction
// can test membership in constant time.
func keepCandidates(entries []treeEntry) map[string]treeEntry {
	kept := make(map[string]treeEntry, len(entries))
	for _, entry := range entries {
		if keepPath(entry.Path) {
			kept[entry.Path] = entry
		}
	}
	return kept
}

// exclusions accumulates the per-file outcomes worth reporting.
type exclusions struct {
	secrets  []string // "path (rule,rule)"
	machine  []string // "path (reason)": content shape said no reader would read it
	withheld []string // paths present in the tree but with no content in the archive
}

// warnings renders the accumulated exclusions as manifest warning lines, naming a bounded number
// of paths and counting the rest.
func (e exclusions) warnings(itemID string) []string {
	var out []string
	if len(e.secrets) > 0 {
		out = append(out, fmt.Sprintf("%s: excluded %d file(s) with detected secrets: %s",
			itemID, len(e.secrets), summarize(e.secrets)))
	}
	if len(e.machine) > 0 {
		out = append(out, fmt.Sprintf("%s: excluded %d machine-generated file(s): %s",
			itemID, len(e.machine), summarize(e.machine)))
	}
	if len(e.withheld) > 0 {
		out = append(out, fmt.Sprintf("%s: %d file(s) recorded without content: %s",
			itemID, len(e.withheld), summarize(e.withheld)))
	}
	return out
}

// summarize joins up to warnedPathLimit entries and counts whatever is left.
func summarize(entries []string) string {
	if len(entries) <= warnedPathLimit {
		return strings.Join(entries, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(entries[:warnedPathLimit], ", "), len(entries)-warnedPathLimit)
}

// recorded builds a fragment that is listed but carries no content: the item still says the file
// exists and why it was excluded, and no blob is written and no derivation is paid for.
func recorded(path, sha string, bytes int64, tier int, meta map[string]string) source.Fragment {
	return source.Fragment{
		Key:         path,
		Fingerprint: sha,
		Bytes:       bytes,
		Tier:        tier,
		MIME:        mimeType(path),
		Meta:        meta,
	}
}

// fragments builds the fragment list from the surviving paths and the extracted archive.
//
// The result is sorted by tier then path. That is deterministic, which the composed hash depends
// on, and it puts the most informative fragments first, so a max_fragments_per_item cap applied
// as a prefix keeps documentation and entrypoints rather than an alphabetical accident.
func (c *Connector) fragments(candidates map[string]treeEntry, arch archive) ([]source.Fragment, exclusions) {
	var (
		out   []source.Fragment
		notes exclusions
	)
	for path, entry := range candidates {
		tier := classify(path)
		content, present := arch.files[path]
		if !present {
			// The tree named it but the archive did not carry it: over the per-file
			// budget, or extraction stopped early.
			notes.withheld = append(notes.withheld, path)
			out = append(out, source.Fragment{
				Key:         path,
				Fingerprint: entry.SHA,
				Bytes:       entry.Size,
				Tier:        tier,
				MIME:        mimeType(path),
				Truncated:   true,
			})
			continue
		}
		if rules := c.scanner.rules(content); len(rules) > 0 {
			notes.secrets = append(notes.secrets, fmt.Sprintf("%s (%s)", path, strings.Join(rules, ",")))
			out = append(out, recorded(path, entry.SHA, int64(len(content)), tier,
				map[string]string{"excluded": "secret", "rules": strings.Join(rules, ",")}))
			continue
		}
		// Shape, after content is in hand and before the file is split: a dump or a dataset
		// is one decision here and sixteen derivations if it gets past.
		if reason := exclusionReason(path, content); reason != "" {
			notes.machine = append(notes.machine, fmt.Sprintf("%s (%s)", path, reason))
			out = append(out, recorded(path, entry.SHA, int64(len(content)), tier,
				map[string]string{"excluded": reason}))
			continue
		}
		out = append(out, fileFragments(path, entry.SHA, content, tier, c.fragmentMaxBytes)...)
	}
	sort.Strings(notes.secrets)
	sort.Strings(notes.machine)
	sort.Strings(notes.withheld)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tier != out[j].Tier {
			return out[i].Tier < out[j].Tier
		}
		return out[i].Key < out[j].Key
	})
	return out, notes
}
