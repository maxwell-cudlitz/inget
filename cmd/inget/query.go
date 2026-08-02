// The `inget query` command: a nearest-neighbour search against the destinations, for verifying
// that what the pipeline stored is retrievable.
//
// It is not an application API. It exists so that the answer to "did this work" is one command
// rather than a psql session with a hand-written vector literal, and so that an operator comparing
// two prompt sets can see the hits rather than the scores alone.
//
// The query is embedded by the configured embedder and every destination is asserted against that
// embedder first (D7): a query vector from one model searched against another model's index returns
// rankings that look ordinary and mean nothing, which is the one failure mode this command must not
// have.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/cli"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/destination"
	"github.com/maxwell-cudlitz/inget/internal/model"
)

// defaultQueryLimit is how many hits a query returns when none is asked for.
const defaultQueryLimit = 10

// snippetChars bounds the text shown per hit in the human-readable rendering. The full text is
// always available with --json.
const snippetChars = 160

// queryOptions are the command's flags.
type queryOptions struct {
	datatype    string
	view        string
	destination string
	limit       int
	efSearch    int
	asJSON      bool
}

// queryCommand builds `inget query`.
func queryCommand() *cobra.Command {
	var opts queryOptions

	cmd := &cobra.Command{
		Use:   "query <text>",
		Short: "Search the destinations for the nearest views to a query",
		Long: "query embeds the given text with the configured embedder and returns the nearest stored\n" +
			"views. With no --datatype it searches every configured datatype and merges the hits by\n" +
			"score. Results go to stdout, as a readable table or as JSON with --json.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuery(cmd, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.datatype, "datatype", "", "restrict the search to one datatype")
	cmd.Flags().StringVar(&opts.view, "view", "", "restrict the search to one view")
	cmd.Flags().StringVar(&opts.destination, "destination", "", "search this destination instead of the first configured one")
	cmd.Flags().IntVar(&opts.limit, "limit", defaultQueryLimit, "how many hits to return")
	cmd.Flags().IntVar(&opts.efSearch, "ef-search", 0, "override the destination's hnsw.ef_search")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "write the hits as JSON")
	return cmd
}

// hit is one search result plus the datatype it came from, which a merged search needs and
// destination.SearchResult does not carry.
type hit struct {
	Datatype string            `json:"datatype"`
	ItemID   string            `json:"item_id"`
	ViewName string            `json:"view"`
	Score    float64           `json:"score"`
	Text     string            `json:"text"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// runQuery embeds the query and searches every datatype in scope.
func runQuery(cmd *cobra.Command, text string, opts queryOptions) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("query text is empty")
	}
	cfg, err := config.Load(cli.ConfigPath(cmd))
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	var args []string
	if opts.datatype != "" {
		args = []string{opts.datatype}
	}
	datatypes, err := selectDatatypes(cfg, args)
	if err != nil {
		return err
	}
	if opts.limit <= 0 {
		return fmt.Errorf("--limit is %d, want 1 or more", opts.limit)
	}

	emb, err := buildEmbedder(cfg)
	if err != nil {
		return err
	}
	vector, err := embedQuery(cmd.Context(), emb, text)
	if err != nil {
		return err
	}

	var hits []hit
	for _, dt := range datatypes {
		found, err := searchDatatype(cmd.Context(), cfg, emb, dt, vector, opts)
		if err != nil {
			return err
		}
		hits = append(hits, found...)
	}
	// Merged across datatypes, so the ordering the destination gave us no longer holds.
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > opts.limit {
		hits = hits[:opts.limit]
	}

	slog.InfoContext(cmd.Context(), "query complete", "datatypes", len(datatypes),
		"embedder", emb.Model(), "dims", emb.Dims(), "hits", len(hits))
	if opts.asJSON {
		return printJSON(hits)
	}
	printHits(hits)
	return nil
}

// embedQuery embeds the query text and checks the width the embedder actually returned.
func embedQuery(ctx context.Context, emb model.Embedder, text string) ([]float32, error) {
	vectors, err := emb.Embed(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embedding the query: %w", err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embedder returned %d vectors for one query", len(vectors))
	}
	return vectors[0], nil
}

// searchDatatype searches one datatype's destination.
func searchDatatype(ctx context.Context, cfg *config.Config, emb model.Embedder,
	dt config.Datatype, vector []float32, opts queryOptions) ([]hit, error) {
	name, err := queryDestination(dt, opts.destination)
	if err != nil {
		return nil, err
	}
	if err := checkQueryView(dt, opts.view); err != nil {
		return nil, err
	}

	sink, err := openDestination(ctx, cfg, name, emb)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sink.Close() }()

	results, err := sink.Search(ctx, destination.SearchQuery{
		Embedding: vector,
		Datatype:  dt.Name,
		ViewName:  opts.view,
		EFSearch:  opts.efSearch,
		Limit:     opts.limit,
	})
	if err != nil {
		return nil, err
	}
	hits := make([]hit, 0, len(results))
	for _, r := range results {
		hits = append(hits, hit{
			Datatype: dt.Name, ItemID: r.ItemID, ViewName: r.ViewName,
			Score: r.Score, Text: r.Text, Metadata: r.Metadata,
		})
	}
	return hits, nil
}

// queryDestination picks the destination to search: the named one, or the datatype's first.
func queryDestination(dt config.Datatype, named string) (string, error) {
	if named == "" {
		if len(dt.Destinations) == 0 {
			return "", fmt.Errorf("datatype %s has no destinations", dt.Name)
		}
		return dt.Destinations[0], nil
	}
	for _, name := range dt.Destinations {
		if name == named {
			return name, nil
		}
	}
	return "", fmt.Errorf("datatype %s does not write to destination %q (it writes to %v)",
		dt.Name, named, dt.Destinations)
}

// checkQueryView rejects a --view the datatype does not declare, which would otherwise return no
// hits and look like an empty index.
func checkQueryView(dt config.Datatype, view string) error {
	if view == "" {
		return nil
	}
	for _, name := range viewNames(dt) {
		if name == view {
			return nil
		}
	}
	return fmt.Errorf("datatype %s has no view %q (declared: %v)", dt.Name, view, viewNames(dt))
}

// printHits writes the readable rendering: one header line per hit, then a snippet.
func printHits(hits []hit) {
	if len(hits) == 0 {
		fmt.Println("no hits")
		return
	}
	for _, h := range hits {
		fmt.Printf("%.4f  %s  %s  [%s]\n", h.Score, h.Datatype, h.ItemID, h.ViewName)
		fmt.Printf("        %s\n", snippet(h.Text))
	}
}

// snippet renders a view's text as one line, bounded so a screenful of hits stays readable.
func snippet(text string) string {
	flat := strings.Join(strings.Fields(text), " ")
	if len(flat) <= snippetChars {
		return flat
	}
	return flat[:snippetChars] + "…"
}
