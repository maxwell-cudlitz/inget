// Unit tests for the decisions the cascade makes per view and per run: whether an existing
// vector can be kept, which items a limited run takes, and what metadata a row carries.
package pipeline

import (
	"strings"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/delta"
	"github.com/maxwellcudlitz/inget/internal/enrich"
	"github.com/maxwellcudlitz/inget/internal/model"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// replaceViewPrompt swaps one view's prompt template, which is what editing a .tmpl file does
// to a running configuration: same view, different signature.
func replaceViewPrompt(t *testing.T, h *cascadeHarness, view, template string) {
	t.Helper()
	prompt, err := enrich.ParsePrompt(view, []byte(template))
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Enrichers[view] = enrich.NewLLMEnricher(h.gen, prompt, enrich.LLMConfig{SchemaVersion: 1})
}

// TestReuseVector covers the level-3 guard. The interesting case is the third: text that
// changed by less than the threshold keeps its vector *and* its recorded text, so the next
// comparison is still measured against what was embedded.
func TestReuseVector(t *testing.T) {
	emb := model.NewFakeEmbedder("e5", 8)
	ex := &execution{deps: Deps{Embedder: emb, Config: DatatypeConfig{DriftThreshold: 0.2}}}

	embedded := "the quick brown fox jumps over the lazy dog"
	prev := state.ViewState{
		Name:         "role",
		InputHash:    "sha256:old-input",
		Text:         embedded,
		EmbeddedHash: delta.EmbeddingHash(embedded),
		Model:        emb.Model(),
		Dims:         emb.Dims(),
		Signature:    emb.Signature(),
	}

	tests := []struct {
		name      string
		text      string
		existing  state.ViewState
		wantReuse bool
		wantText  string
	}{
		{"identical text keeps the vector", embedded, prev, true, embedded},
		{"sub-threshold change keeps the vector and the baseline", embedded + " today", prev, true, embedded},
		{"large change re-embeds", "an entirely different sentence about cats", prev, false, ""},
		{"never embedded must embed", embedded, state.ViewState{Name: "role"}, false, ""},
		{"different embedder must embed", embedded, withSignature(prev, "other-embedder"), false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reused := reuseVector(ex, pendingView{
				name:      "role",
				text:      tt.text,
				inputHash: "sha256:new-input",
				existing:  tt.existing,
			})
			if reused != tt.wantReuse {
				t.Fatalf("reuseVector reused = %v, want %v", reused, tt.wantReuse)
			}
			if !reused {
				return
			}
			if got.Text != tt.wantText {
				t.Errorf("kept text = %q, want %q", got.Text, tt.wantText)
			}
			if got.EmbeddedHash != prev.EmbeddedHash {
				t.Error("kept state should keep the hash of the stored vector")
			}
			if got.InputHash != "sha256:new-input" {
				t.Errorf("input hash = %q, want the new one", got.InputHash)
			}
		})
	}
}

// withSignature returns v with a different embedder signature, standing in for a model swap.
func withSignature(v state.ViewState, signature string) state.ViewState {
	v.Signature = signature
	return v
}

func TestSelectWork(t *testing.T) {
	candidates := []string{"c", "a", "d", "b"}

	tests := []struct {
		name        string
		rc          RunConfig
		want        []string
		wantPartial bool
	}{
		{"no flags takes everything, sorted", RunConfig{}, []string{"a", "b", "c", "d"}, false},
		{"only filters", RunConfig{Only: []string{"b", "d"}}, []string{"b", "d"}, true},
		{"only ignores unknown ids", RunConfig{Only: []string{"b", "zz"}}, []string{"b"}, true},
		{"limit truncates deterministically", RunConfig{Limit: 2}, []string{"a", "b"}, true},
		{"limit above the count is not partial", RunConfig{Limit: 9}, []string{"a", "b", "c", "d"}, false},
		{"only and limit compose", RunConfig{Only: []string{"b", "c", "d"}, Limit: 2}, []string{"b", "c"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := append([]string(nil), candidates...)
			got, partial := selectWork(input, tt.rc)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("selectWork = %v, want %v", got, tt.want)
			}
			if partial != tt.wantPartial {
				t.Errorf("partial = %v, want %v", partial, tt.wantPartial)
			}
		})
	}
}

func TestAnnotateMetadata(t *testing.T) {
	rec := &artifact.Record{Metadata: map[string]string{"full_name": "owner/repo"}}
	cfg := DatatypeConfig{MetadataFields: map[string]string{
		"tier":         "internal",
		"related_keys": "${references.*.resolved_keys}",
	}}

	got := annotateMetadata(cfg, rec)

	if got["full_name"] != "owner/repo" {
		t.Errorf("record metadata was dropped: %v", got)
	}
	if got["tier"] != "internal" {
		t.Errorf("static metadata field = %q, want %q", got["tier"], "internal")
	}
	if _, present := got["related_keys"]; present {
		t.Error("an unresolved ${...} field must be omitted, not written literally")
	}
	// The record's own map must not be mutated: it belongs to the artifact reader.
	if _, leaked := rec.Metadata["tier"]; leaked {
		t.Error("annotateMetadata mutated the record's metadata")
	}
}

func TestChangedKeysIncludesDeletions(t *testing.T) {
	d := delta.Delta{Added: []string{"a"}, Modified: []string{"b"}, Deleted: []string{"c"}, Unchanged: []string{"d"}}

	got := strings.Join(changedKeys(d), ",")

	if got != "a,b,c" {
		t.Errorf("changedKeys = %q, want %q", got, "a,b,c")
	}
}
