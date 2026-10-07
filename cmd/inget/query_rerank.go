// Optional query-time LLM ranking. The retrieval pool is grouped by datatype/item
// before ranking, retaining the best vector hit and its original score. Failure falls
// back to that grouped vector order, except cancellation of the command itself.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/enrich"
	"github.com/maxwell-cudlitz/inget/internal/model"
	"github.com/maxwell-cudlitz/inget/internal/rerank"
)

// queryRanker is the ranking operation the query command uses.
type queryRanker interface {
	Order(context.Context, string, []string) ([]int, error)
}

// prepareQueryRanker honors an explicit --rerank override before creating any client.
// Configuration errors are surfaced before spending on query embedding.
func prepareQueryRanker(cmd *cobra.Command, cfg *config.Config, opts queryOptions) (queryRanker, error) {
	enabled := cfg.Query.Rerank.Enabled
	if cmd.Flags().Changed("rerank") {
		enabled = opts.rerank
	}
	if !enabled {
		return nil, nil
	}
	if max(opts.limit, cfg.Query.Rerank.Candidates) > config.MaxRerankCandidates {
		return nil, fmt.Errorf("reranking supports at most %d candidates; lower --limit or use --rerank=false", config.MaxRerankCandidates)
	}
	mc := cfg.Models.Reranker
	if mc.Driver != "openai" {
		return nil, fmt.Errorf("models.reranker.driver = %q, want openai", mc.Driver)
	}
	apiKey, err := modelAPIKey(cfg, mc.APIKeyEnv, "reranker")
	if err != nil {
		return nil, err
	}
	prompt, err := enrich.LoadPrompt(cfg.Query.Rerank.Prompt)
	if err != nil {
		return nil, fmt.Errorf("loading rerank prompt: %w", err)
	}
	gen := model.NewGenerator(model.OpenAIGeneratorConfig{
		BaseURL: mc.BaseURL, Model: mc.Model, APIKey: apiKey,
		Temperature: mc.Temperature, Seed: mc.Seed,
		MaxOutputTokens: mc.MaxOutputTokens, MaxInputChars: mc.MaxInputChars,
		Timeout: mc.Timeout.Duration(), RequestOptions: mc.RequestOptions,
	})
	return rerank.New(gen, prompt, cfg.Query.Rerank.MaxCandidateChars), nil
}

// finishQueryHits applies the optional grouped ranking before the final output limit.
// The caller supplies vector-sorted hits and a deadline covering retries as well as HTTP.
func finishQueryHits(ctx context.Context, query string, hits []hit, limit, candidates int,
	ranker queryRanker, timeout time.Duration) ([]hit, error) {
	if ranker != nil {
		hits = groupQueryHits(hits)
		pool := max(limit, candidates)
		if len(hits) > pool {
			hits = hits[:pool]
		}
		if len(hits) > 1 {
			var err error
			hits, err = rerankQueryHits(ctx, query, hits, ranker, timeout)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// groupQueryHits keeps one representative per item. Datatype is part of the key:
// unrelated sources can legitimately share an item ID. Input is already vector-sorted.
func groupQueryHits(hits []hit) []hit {
	type itemKey struct{ datatype, id string }
	seen := make(map[itemKey]bool, len(hits))
	grouped := make([]hit, 0, len(hits))
	for _, h := range hits {
		key := itemKey{h.Datatype, h.ItemID}
		if !seen[key] {
			seen[key] = true
			grouped = append(grouped, h)
		}
	}
	return grouped
}

// rerankQueryHits changes order, never scores. A provider failure or malformed ranking
// emits one warning and retains vector order. The command's own cancellation is fatal.
func rerankQueryHits(ctx context.Context, query string, hits []hit,
	ranker queryRanker, timeout time.Duration) ([]hit, error) {
	docs := make([]string, len(hits))
	for i, h := range hits {
		// Unlike Lex's metadata-only input, the best matching view also contributes
		// evidence. The ranker bounds each complete preview before sending it.
		docs[i] = strings.Join([]string{h.Datatype, h.ItemID, h.ViewName, h.Text}, " ")
	}
	rankCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	order, err := ranker.Order(rankCtx, query, docs)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("query cancelled during reranking: %w", ctx.Err())
		}
		slog.WarnContext(ctx, "reranking failed; using grouped vector order", "error", err)
		return hits, nil
	}
	ranked := make([]hit, len(hits))
	for i, index := range order {
		ranked[i] = hits[index]
		ranked[i].Rank = i + 1
	}
	slog.InfoContext(ctx, "query reranked", "candidates", len(hits))
	return ranked, nil
}
