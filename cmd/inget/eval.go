// The `inget eval` command: the retrieval quality gate (D14).
//
// It reads the view text state already holds, re-embeds it, scores the five metrics of D14
// per datatype and per view, prints the reports as JSON on stdout, and exits non-zero when
// any datatype breaches a threshold or cannot be scored. No generator is constructed and no
// destination is opened: the harness never generates, and retrieval is computed inside the
// re-embedded sample rather than against a vector store bound to another model.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/cli"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/eval"
)

// evalOptions are the command's flags. The three embedder overrides exist together because a
// real A/B needs all of them: a second model is usually served by a second endpoint and is
// rarely the same native width as the first, and a width mismatch is rejected by the embedder
// client before any score is produced.
type evalOptions struct {
	embedder  string
	embedURL  string
	embedDims int
}

// evalCommand builds `inget eval`.
func evalCommand() *cobra.Command {
	var opts evalOptions

	cmd := &cobra.Command{
		Use:   "eval [datatype]",
		Short: "Score retrieval quality against the configured thresholds",
		Long: "eval samples items that have stored view text, re-embeds that text, and scores\n" +
			"self-retrieval, distinctiveness, view coverage, view distinctiveness and top-3\n" +
			"metadata retrieval per datatype and per view. It never calls the generator, so no\n" +
			"generator credentials are needed and no views are regenerated. A threshold breach,\n" +
			"or a corpus with nothing to score, exits non-zero.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEval(cmd, args, opts)
		},
	}
	cmd.Flags().StringVar(&opts.embedder, "embedder", "", "embedder model to score with, overriding config")
	cmd.Flags().StringVar(&opts.embedURL, "embedder-url", "", "embedder base URL, overriding config")
	cmd.Flags().IntVar(&opts.embedDims, "embedder-dims", 0, "embedder native dimensions, overriding config")
	return cmd
}

// runEval evaluates every requested datatype and reports the results.
func runEval(cmd *cobra.Command, args []string, opts evalOptions) error {
	cfg, err := config.Load(cli.ConfigPath(cmd))
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	datatypes, err := selectDatatypes(cfg, args)
	if err != nil {
		return err
	}
	opts.apply(cfg)

	emb, err := buildEmbedder(cfg)
	if err != nil {
		return err
	}
	store, err := openState(cmd.Context(), cfg)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	reports := make([]eval.Report, 0, len(datatypes))
	for _, dt := range datatypes {
		report, err := eval.Run(cmd.Context(), store, emb, eval.Options{
			Datatype:   dt.Name,
			Views:      viewNames(dt),
			SampleSize: cfg.Eval.SampleSize,
			Thresholds: cfg.Eval.Thresholds,
		})
		if err != nil {
			return fmt.Errorf("evaluating %s: %w", dt.Name, err)
		}
		reports = append(reports, report)
	}
	return reportEval(reports)
}

// apply folds the flag overrides into the loaded configuration, so everything below this point
// reads one source of truth. Dropping a now-impossible truncation target is part of that: a
// narrower model with the configured target left in place would fail every embedding call
// instead of scoring the model under test.
func (o evalOptions) apply(cfg *config.Config) {
	if o.embedder != "" {
		cfg.Models.Embedder.Model = o.embedder
	}
	if o.embedURL != "" {
		cfg.Models.Embedder.BaseURL = o.embedURL
	}
	if o.embedDims > 0 {
		cfg.Models.Embedder.Dimensions = o.embedDims
		if cfg.Models.Embedder.TruncateDims > o.embedDims {
			cfg.Models.Embedder.TruncateDims = 0
		}
	}
}

// viewNames returns a datatype's configured view names, which is what coverage is measured
// against.
func viewNames(dt config.Datatype) []string {
	names := make([]string, 0, len(dt.Views))
	for _, v := range dt.Views {
		names = append(names, v.Name)
	}
	return names
}

// reportEval writes the reports as JSON on stdout, logs one summary line per datatype, and
// returns an error when any datatype failed the gate. Stdout carries data and stderr carries
// diagnostics, so `inget eval | jq` works while the summary still reaches a terminal.
func reportEval(reports []eval.Report) error {
	var failed []error
	for _, r := range reports {
		slog.Info("datatype evaluated", logArgs(r)...)
		if !r.Passed() {
			failed = append(failed, fmt.Errorf("%s: %s", r.Datatype, evalFailure(r)))
		}
	}

	data, err := json.MarshalIndent(reports, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling eval reports: %w", err)
	}
	fmt.Println(string(data))

	if len(failed) > 0 {
		return fmt.Errorf("quality gate failed: %w", errors.Join(failed...))
	}
	return nil
}

// logArgs renders one report as structured log attributes: the counts, then one key per
// metric so a breach is greppable without parsing the JSON.
func logArgs(r eval.Report) []any {
	args := []any{
		"datatype", r.Datatype,
		"status", r.Status,
		"embedder", r.Embedder,
		"live_items", r.LiveItems,
		"viewed_items", r.ViewedItems,
		"sampled_items", r.SampledItems,
		"view_vectors", r.ViewVectors,
	}
	if r.Reason != "" {
		args = append(args, "reason", r.Reason)
	}
	for _, m := range r.Metrics {
		if m.Skipped != "" {
			args = append(args, m.Name, "skipped")
			continue
		}
		args = append(args, m.Name, m.Score)
	}
	return args
}

// evalFailure describes why a datatype did not pass, naming every breached metric.
func evalFailure(r eval.Report) string {
	if r.Reason != "" {
		return r.Reason
	}
	breaches := make([]string, 0, len(r.Metrics))
	for _, m := range r.Metrics {
		if m.Breached() {
			breaches = append(breaches, fmt.Sprintf("%s %.4f < %.4f over %d",
				m.Name, m.Score, m.Threshold, m.Sample))
		}
	}
	return strings.Join(breaches, "; ")
}
