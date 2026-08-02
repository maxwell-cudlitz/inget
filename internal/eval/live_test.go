// The live half of the harness's acceptance: the same fixtures through a real embedder.
//
// Gated on INGET_TEST_EMBEDDER_URL, so CI and offline development skip it. What it asserts is
// relative rather than absolute — a degraded corpus must breach, and a healthy one must score
// strictly better — because the absolute numbers are a property of the model under test and
// pinning them here would turn every embedder swap into a failing test.
package eval

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/maxwellcudlitz/inget/internal/model"
)

// Environment for the live embedder. Only the URL is required; the rest default to the shipped
// configuration's embedder.
const (
	envEmbedderURL   = "INGET_TEST_EMBEDDER_URL"
	envEmbedderModel = "INGET_TEST_EMBEDDER_MODEL"
	envEmbedderDims  = "INGET_TEST_EMBEDDER_DIMS"
)

// liveEmbedder builds a client against the configured server, or skips the test.
func liveEmbedder(t *testing.T) model.Embedder {
	t.Helper()
	baseURL := os.Getenv(envEmbedderURL)
	if baseURL == "" {
		t.Skipf("%s is not set; skipping the live embedder case", envEmbedderURL)
	}
	name := os.Getenv(envEmbedderModel)
	if name == "" {
		name = "Qwen/Qwen3-Embedding-0.6B"
	}
	dims := 1024
	if raw := os.Getenv(envEmbedderDims); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s = %q: %v", envEmbedderDims, raw, err)
		}
		dims = parsed
	}
	return model.NewEmbedder(model.OpenAIEmbedderConfig{
		BaseURL:    baseURL,
		Model:      name,
		Dimensions: dims,
		BatchSize:  16,
		Timeout:    60 * time.Second,
	})
}

func TestLiveEmbedderSeparatesHealthyFromDegraded(t *testing.T) {
	emb := liveEmbedder(t)

	healthy := liveReport(t, emb, healthyCorpus())
	degraded := liveReport(t, emb, degradedCorpus())

	for _, m := range healthy.Metrics {
		t.Logf("healthy %s: %.4f over %d, threshold %.2f, pass=%v", m.Name, m.Score, m.Sample, m.Threshold, m.Pass)
	}
	if degraded.Status != StatusBreach {
		t.Errorf("degraded corpus status = %q, want %q; metrics: %+v",
			degraded.Status, StatusBreach, degraded.Metrics)
	}
	for _, name := range []string{MetricSelfRetrieval, MetricDistinctiveness, MetricViewDistinctiveness} {
		good, bad := metric(t, healthy, name), metric(t, degraded, name)
		if good.Score <= bad.Score {
			t.Errorf("%s scored %.4f on the healthy corpus and %.4f on the degraded one; "+
				"a real embedder must rank distinct prose above one repeated sentence",
				name, good.Score, bad.Score)
		}
	}
}

// liveReport seeds a fresh store with the fixtures and evaluates it.
func liveReport(t *testing.T, emb model.Embedder, items []fixture) Report {
	t.Helper()
	store := openStore(t)
	seed(t, store, items)
	report, err := Run(t.Context(), store, emb, options())
	if err != nil {
		t.Fatalf("Run against the live embedder: %v", err)
	}
	return report
}
