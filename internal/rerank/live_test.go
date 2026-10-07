// Optional live-provider smoke test. It uses a complete operator-supplied config and
// synthetic documents, never source data; normal unit runs skip it without the opt-in.
package rerank

import (
	"context"
	"os"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/model"
)

func TestLiveOpenAICompatibleReranking(t *testing.T) {
	path := os.Getenv("INGET_TEST_RERANK_CONFIG")
	if path == "" {
		t.Skip("set INGET_TEST_RERANK_CONFIG and its referenced credentials to opt into inference")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	mc := cfg.Models.Reranker
	if mc.Driver != "openai" {
		t.Fatalf("reranker driver must be openai")
	}
	key := ""
	if mc.APIKeyEnv != "" {
		key, err = cfg.Secret(mc.APIKeyEnv)
		if err != nil {
			t.Fatal(err)
		}
	}
	prompt, err := enrich.LoadPrompt(cfg.Query.Rerank.Prompt)
	if err != nil {
		t.Fatal(err)
	}
	gen := model.NewGenerator(model.OpenAIGeneratorConfig{
		BaseURL: mc.BaseURL, Model: mc.Model, APIKey: key,
		Temperature: mc.Temperature, Seed: mc.Seed,
		MaxOutputTokens: mc.MaxOutputTokens, MaxInputChars: mc.MaxInputChars,
		Timeout: mc.Timeout.Duration(), RequestOptions: mc.RequestOptions,
	})
	ctx, cancel := context.WithTimeout(context.Background(), mc.Timeout.Duration())
	defer cancel()
	order, err := New(gen, prompt, cfg.Query.Rerank.MaxCandidateChars).Order(ctx,
		"Which tool maps Terraform state resources using provider schemas?", []string{
			"An audio player with playlists and equalizer controls.",
			"A Terraform state mapping tool that maps state resources using provider schemas.",
			"A calendar app that schedules meetings and sends reminders.",
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 || order[0] != 1 {
		t.Fatalf("synthetic Terraform document was not ranked first: %v", order)
	}
	t.Logf("valid complete ranking; Terraform document first; model=%s", mc.Model)
}
