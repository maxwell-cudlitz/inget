// Separate view clients preserve paid fragment cache signatures when view budgets grow.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/config"
)

func TestViewGeneratorBudgetPreservesFragmentSignature(t *testing.T) {
	prompt := filepath.Join(t.TempDir(), "prompt.tmpl")
	if err := os.WriteFile(prompt, []byte("Describe {{.Document}} {{.Content}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &config.Config{}
	c.Models.Generator = config.Generator{ModelClient: config.ModelClient{Model: "gen"}, MaxOutputTokens: 1024}
	dt := config.Datatype{Enricher: "llm", Views: []config.View{{Name: "purpose", Prompt: prompt}},
		FragmentEnricher: config.FragmentEnricher{Enabled: true, Prompt: prompt, MaxInputChars: 4000}}
	gen, err := buildGenerator(c)
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := buildFragmentEnricher(c, gen, dt)
	if err != nil {
		t.Fatal(err)
	}
	before, err := buildViewEnrichers(c, gen, dt)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := buildViewGenerator(c)
	if err != nil || fallback.Signature() != gen.Signature() {
		t.Fatalf("omitted view generator changed model signature: %v", err)
	}
	c.Models.ViewGenerator = c.Models.Generator
	c.Models.ViewGenerator.MaxOutputTokens = 4096
	viewGen, err := buildViewGenerator(c)
	if err != nil {
		t.Fatal(err)
	}
	after, err := buildViewEnrichers(c, viewGen, dt)
	if err != nil {
		t.Fatal(err)
	}
	fragGen, err := buildGenerator(c)
	if err != nil {
		t.Fatal(err)
	}
	fragmentAfter, err := buildFragmentEnricher(c, fragGen, dt)
	if err != nil {
		t.Fatal(err)
	}
	if fragmentAfter.Signature() != fragment.Signature() || fragGen.Signature() != gen.Signature() {
		t.Fatal("increasing only the view budget invalidated cached file summaries")
	}
	if after["purpose"].Signature() == before["purpose"].Signature() {
		t.Fatal("changing view output budget did not invalidate view text")
	}
}
