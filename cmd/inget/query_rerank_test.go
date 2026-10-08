// Query ranking tests exercise item grouping, pool expansion, successful reordering,
// vector-score preservation, fallback and cancellation without touching a database.
package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/maxwell-cudlitz/inget/internal/config"
	"time"
)

type testQueryRanker struct {
	order []int
	err   error
	docs  []string
	calls int
	wait  bool
}

func (r *testQueryRanker) Order(ctx context.Context, _ string, docs []string) ([]int, error) {
	r.calls++
	r.docs = docs
	if r.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.order, r.err
}

func queryTestHits() []hit {
	return []hit{
		{Datatype: "github/repo", ItemID: "org/a", ViewName: "purpose", Score: 0.9, Text: "first"},
		{Datatype: "github/repo", ItemID: "org/a", ViewName: "stack", Score: 0.8, Text: "duplicate facet"},
		{Datatype: "github/repo", ItemID: "org/b", ViewName: "purpose", Score: 0.7, Text: "second"},
		{Datatype: "monday/item", ItemID: "org/a", ViewName: "purpose", Score: 0.6, Text: "different datatype"},
	}
}

func TestQueryRerankingAndVectorFallback(t *testing.T) {
	tests := []struct {
		name   string
		ranker *testQueryRanker
		ids    []string
		ranks  []int
		scores []float64
	}{
		{"success", &testQueryRanker{order: []int{2, 0, 1}}, []string{"org/a", "org/a"}, []int{1, 2}, []float64{0.6, 0.9}},
		{"failure", &testQueryRanker{err: errors.New("invalid ranking")}, []string{"org/a", "org/b"}, []int{0, 0}, []float64{0.9, 0.7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits, err := finishQueryHits(context.Background(), "query", queryTestHits(), 2, 50, tt.ranker, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			var ranks []int
			var scores []float64
			for _, h := range hits {
				ids, ranks, scores = append(ids, h.ItemID), append(ranks, h.Rank), append(scores, h.Score)
			}
			if !reflect.DeepEqual(ids, tt.ids) || !reflect.DeepEqual(ranks, tt.ranks) || !reflect.DeepEqual(scores, tt.scores) {
				t.Fatalf("hits=%+v", hits)
			}
			if len(tt.ranker.docs) != 3 || tt.ranker.calls != 1 {
				t.Fatalf("grouping or call count incorrect: %+v", tt.ranker)
			}
		})
	}
}

func TestQuerySkipsUnnecessaryRanking(t *testing.T) {
	ranker := &testQueryRanker{}
	hits, err := finishQueryHits(context.Background(), "query", queryTestHits()[:2], 10, 50, ranker, time.Second)
	if err != nil || len(hits) != 1 || ranker.calls != 0 {
		t.Fatalf("hits=%v, err=%v, calls=%d", hits, err, ranker.calls)
	}
	// Disabling rerank preserves the existing view-level output, including duplicates.
	hits, err = finishQueryHits(context.Background(), "query", queryTestHits(), 2, 50, nil, 0)
	if err != nil || len(hits) != 2 || hits[0].ItemID != hits[1].ItemID || hits[1].ViewName != "stack" {
		t.Fatalf("disabled rerank changed view results: %v, %v", hits, err)
	}
}

func TestQueryRankingPoolPrecedesOutputLimit(t *testing.T) {
	ranker := &testQueryRanker{order: []int{1, 0}}
	hits, err := finishQueryHits(context.Background(), "query", queryTestHits(), 1, 2, ranker, time.Second)
	if err != nil || len(hits) != 1 || hits[0].ItemID != "org/b" || len(ranker.docs) != 2 {
		t.Fatalf("hits=%v, err=%v, candidates=%v", hits, err, ranker.docs)
	}
}

func TestQueryRerankerTimeoutFallsBackButCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := finishQueryHits(ctx, "query", queryTestHits(), 1, 50, &testQueryRanker{wait: true}, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
	hits, err := finishQueryHits(context.Background(), "query", queryTestHits(), 1, 50, &testQueryRanker{wait: true}, time.Millisecond)
	if err != nil || hits[0].Score != 0.9 || hits[0].Rank != 0 {
		t.Fatalf("timeout did not fall back: hits=%v, err=%v", hits, err)
	}
}

func TestQueryReranksEvenWhenEveryItemFitsOutput(t *testing.T) {
	ranker := &testQueryRanker{order: []int{2, 0, 1}}
	hits, err := finishQueryHits(context.Background(), "query", queryTestHits(), 10, 50, ranker, time.Second)
	if err != nil || len(hits) != 3 || hits[0].Datatype != "monday/item" || hits[0].Rank != 1 || ranker.calls != 1 {
		t.Fatalf("hits=%v, err=%v, calls=%d", hits, err, ranker.calls)
	}
}

func TestQueryRerankFlagOverrideAndPoolCap(t *testing.T) {
	cmd := queryCommand()
	cfg := &config.Config{}
	cfg.Query.Rerank.Enabled = true
	cfg.Query.Rerank.Candidates = 50
	if err := cmd.Flags().Set("rerank", "false"); err != nil {
		t.Fatal(err)
	}
	ranker, err := prepareQueryRanker(cmd, cfg, queryOptions{limit: 10})
	if err != nil || ranker != nil {
		t.Fatalf("explicit false did not bypass ranking: %v, %v", ranker, err)
	}
	if err := cmd.Flags().Set("rerank", "true"); err != nil {
		t.Fatal(err)
	}
	_, err = prepareQueryRanker(cmd, cfg, queryOptions{limit: 1001, rerank: true})
	if err == nil {
		t.Fatal("oversized ranking pool accepted")
	}
}
