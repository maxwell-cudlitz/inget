// Sampling and loading of the corpus the metrics run over.
//
// The sample is drawn from the items that have stored views, because an item that has never
// been enriched has no text to score and counting it as a failure would report "the pipeline
// has not run yet" as "the prompts are bad". How many items exist and how many have views is
// reported alongside the scores instead.
package eval

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
)

// item is one sampled record: its metadata, and the non-empty text of every view it has.
type item struct {
	ID       string
	Metadata map[string]string
	Views    map[string]string
}

// viewNames returns the item's view names in sorted order, so that vector indices, and
// therefore every tie-break below them, are the same on every run.
func (i item) viewNames() []string {
	names := make([]string, 0, len(i.Views))
	for name := range i.Views {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// corpus is the sample plus the two counts that put it in context: how many live items the
// datatype has, and how many of them have any view text at all. views is the configured view
// list, which is both what coverage measures against and what the sample is filtered to.
type corpus struct {
	live   int
	viewed []string
	views  []string
	items  []item
}

// unscorable reports why the corpus cannot be scored, or "" when it can. Two items is the
// floor: with one, every nearest neighbour is trivially the same item and no cross-item
// distance exists, so every retrieval metric would report a perfect score over no evidence.
func (c corpus) unscorable() string {
	switch {
	case c.live == 0:
		return "the datatype has no items in state: run inget-fetch and inget run first"
	case len(c.viewed) == 0:
		return fmt.Sprintf("none of the %d items has stored view text: run inget run first", c.live)
	case len(c.items) == 0:
		return "no sampled item has text for a configured view: the stored views are named " +
			"differently from the ones this configuration declares"
	case len(c.items) < 2:
		return fmt.Sprintf("only %d item has stored view text; scoring retrieval needs at least 2",
			len(c.items))
	}
	return ""
}

// loadCorpus samples the datatype and reads the sampled items' metadata and view text.
func loadCorpus(ctx context.Context, st Store, o Options) (corpus, error) {
	fingerprints, err := st.ItemFingerprints(ctx, o.Datatype)
	if err != nil {
		return corpus{}, err
	}
	viewed, err := st.ViewedItems(ctx, o.Datatype)
	if err != nil {
		return corpus{}, err
	}

	c := corpus{live: len(fingerprints), viewed: viewed, views: o.Views}
	for _, id := range sample(viewed, o.SampleSize) {
		loaded, err := loadItem(ctx, st, o.Datatype, id, o.Views)
		if err != nil {
			return corpus{}, err
		}
		if len(loaded.Views) > 0 {
			c.items = append(c.items, loaded)
		}
	}
	return c, nil
}

// loadItem reads one item's metadata and the text of its non-empty configured views.
//
// Two kinds of row are dropped. A view with empty text has nothing to embed, and coverage
// counts it as missing by comparing what is present against the configured list. A view that
// configuration no longer declares is stale state awaiting collection, and scoring it would
// report on a view nobody asked for.
func loadItem(ctx context.Context, st Store, datatype, id string, configured []string) (item, error) {
	stored, _, err := st.Item(ctx, datatype, id)
	if err != nil {
		return item{}, err
	}
	views, err := st.ViewState(ctx, datatype, id)
	if err != nil {
		return item{}, err
	}

	loaded := item{ID: id, Metadata: stored.Metadata, Views: map[string]string{}}
	for name, v := range views {
		if v.Text != "" && slices.Contains(configured, name) {
			loaded.Views[name] = v.Text
		}
	}
	return loaded, nil
}

// sample selects at most n IDs deterministically, by digest order rather than by ID order.
//
// Taking the first n sorted IDs would sample one alphabetical corner of the corpus, and
// taking n at random would make two runs incomparable — which is exactly what the
// --embedder A/B needs to be able to do. Hashing gives an arbitrary but fixed order, so the
// same corpus always yields the same sample.
func sample(ids []string, n int) []string {
	if n <= 0 || n >= len(ids) {
		return slices.Sorted(slices.Values(ids))
	}
	byDigest := slices.Clone(ids)
	digests := make(map[string]string, len(ids))
	for _, id := range ids {
		sum := sha256.Sum256([]byte(id))
		digests[id] = string(sum[:])
	}
	sort.Slice(byDigest, func(i, j int) bool {
		return digests[byDigest[i]] < digests[byDigest[j]]
	})
	return slices.Sorted(slices.Values(byDigest[:n]))
}
