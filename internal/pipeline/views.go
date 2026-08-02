// Stages 5, 6 and 8: scoped composition, view generation, and the decision to re-embed.
//
// Each view is composed over only the fragments its depends_on globs match, which is what
// makes the level-2 guard mean "this view's input changed" rather than "something in this
// item changed". Composing the whole item for every view would put a repository's entire
// contents in front of a prompt asking about its build tooling, and would invalidate every
// view whenever any fragment moved.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/maxwellcudlitz/inget/internal/config"
	"github.com/maxwellcudlitz/inget/internal/delta"
	"github.com/maxwellcudlitz/inget/internal/destination"
	"github.com/maxwellcudlitz/inget/internal/enrich"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// viewRequest is one item's input to view processing.
type viewRequest struct {
	itemID      string
	entries     []delta.ComposeEntry // every fragment and reference contribution, unscoped
	changedKeys []string             // added, modified and deleted fragment keys, plus changed reference keys
	// anyChanged forces every view past the level-1 scope check. A metadata-injected
	// reference reaches every prompt, so no dependency glob can express which views it
	// affects; the level-2 input hash, which carries refDigest, then decides which of them
	// actually regenerate.
	anyChanged  bool
	refDigest   string            // metadata-injected reference payload, part of the level-2 key
	metadata    map[string]string // annotated item metadata
	relatedKeys []string          // resolved reference keys, for the vector row
	existing    map[string]state.ViewState
}

// viewOutput is what one item's views produced: rows to upsert, and the guard state to
// checkpoint once they are upserted.
type viewOutput struct {
	rows  []destination.Row
	views []state.ViewState
}

// pendingView is a generated view awaiting the embedding decision.
type pendingView struct {
	name      string
	text      string
	inputHash string
	existing  state.ViewState
}

// processViews generates each view that needs it, embeds the ones whose text moved far
// enough to matter, and returns the rows and guard state that follow.
func processViews(ctx context.Context, ex *execution, req viewRequest) (viewOutput, error) {
	var (
		out     viewOutput
		pending []pendingView
	)
	for _, view := range ex.deps.Config.Views {
		p, generated, err := generateView(ctx, ex, req, view)
		if err != nil {
			return viewOutput{}, err
		}
		if !generated {
			continue
		}
		if reused, ok := reuseVector(ex, p); ok {
			out.views = append(out.views, reused)
			continue
		}
		pending = append(pending, p)
	}
	if len(pending) == 0 {
		return out, nil
	}

	views, rows, err := embedViews(ctx, ex, req, pending)
	if err != nil {
		return viewOutput{}, err
	}
	out.views = append(out.views, views...)
	out.rows = append(out.rows, rows...)
	return out, nil
}

// generateView composes a view's scope and generates its text, reporting false when a guard
// let it skip.
func generateView(ctx context.Context, ex *execution, req viewRequest, view config.View) (pendingView, bool, error) {
	cfg := ex.deps.Config
	enricher, ok := ex.deps.Enrichers[view.Name]
	if !ok {
		return pendingView{}, false, fmt.Errorf("no enricher configured for view %s of %s", view.Name, cfg.Name)
	}

	// Level-1 scope: nothing this view depends on changed.
	if !req.anyChanged && len(view.DependsOn) > 0 && delta.ViewSkippable(req.changedKeys, view.DependsOn) {
		ex.stats.addViewsSkipped(1)
		return pendingView{}, false, nil
	}

	scoped := scopedCompose(ctx, cfg, view, req)
	if scoped.Text == "" {
		// The item holds nothing this view describes. Generating from an empty document
		// would produce a confident paragraph about nothing.
		slog.DebugContext(ctx, "view has no fragments in scope",
			"datatype", cfg.Name, "item_id", req.itemID, "view", view.Name)
		ex.stats.addViewsSkipped(1)
		return pendingView{}, false, nil
	}

	// Level-2 guard: same scoped input, same references, same prompt, and a vector already
	// exists.
	inputHash := delta.ViewInputHash(scoped.Hash, enricher.Signature(), req.refDigest)
	existing := req.existing[view.Name]
	if existing.InputHash == inputHash && existing.EmbeddedHash != "" {
		ex.stats.addViewsSkipped(1)
		return pendingView{}, false, nil
	}

	if err := ex.gen.acquire(ctx); err != nil {
		return pendingView{}, false, fmt.Errorf("waiting to generate view %s: %w", view.Name, err)
	}
	text, err := enricher.Enrich(ctx, enrich.TemplateData{
		Metadata: req.metadata,
		Document: scoped.Text,
		ViewName: view.Name,
	})
	ex.gen.release()
	if err != nil {
		return pendingView{}, false, fmt.Errorf("generating view %s for %s/%s: %w",
			view.Name, cfg.Name, req.itemID, err)
	}
	ex.stats.addViewsGenerated(1)

	return pendingView{name: view.Name, text: text, inputHash: inputHash, existing: existing}, true, nil
}

// scopedCompose composes only the fragments a view depends on. A view with no depends_on
// declares dependence on the whole item.
func scopedCompose(ctx context.Context, cfg DatatypeConfig, view config.View, req viewRequest) delta.Composition {
	entries := req.entries
	if len(view.DependsOn) > 0 {
		entries = make([]delta.ComposeEntry, 0, len(req.entries))
		for _, e := range req.entries {
			if delta.MatchesAny(e.Key, view.DependsOn) {
				entries = append(entries, e)
			}
		}
	}
	composed := delta.Compose(entries, cfg.ComposeOrder, cfg.ComposeMaxChars)
	if composed.Truncated {
		slog.WarnContext(ctx, "composed document truncated",
			"datatype", cfg.Name, "item_id", req.itemID, "view", view.Name,
			"chars", composed.OriginalChars, "max_chars", cfg.ComposeMaxChars)
	}
	return composed
}

// reuseVector reports whether a regenerated view can keep the vector it already has, and
// returns the guard state to record when it can.
//
// The comparison is against the text that produced the stored vector, not against the text
// generated last time. Those differ precisely when an earlier regeneration was skipped for
// low drift, and comparing against the newer text would let a run of sub-threshold changes
// walk the view arbitrarily far from what is indexed while every individual step looked
// acceptable (D4).
func reuseVector(ex *execution, p pendingView) (state.ViewState, bool) {
	emb := ex.deps.Embedder
	prev := p.existing
	if prev.EmbeddedHash == "" || prev.Model != emb.Model() || prev.Signature != emb.Signature() {
		return state.ViewState{}, false // never embedded, or embedded by a different model
	}
	identical := prev.EmbeddedHash == delta.EmbeddingHash(p.text)
	if !identical && delta.DriftExceedsThreshold(prev.Text, p.text, ex.deps.Config.DriftThreshold) {
		return state.ViewState{}, false
	}
	// Text and EmbeddedHash still describe the stored vector, so they are left alone; only
	// the level-2 guard advances.
	prev.InputHash = p.inputHash
	return prev, true
}
