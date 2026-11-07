// Query preflight shares the search scope and validation path without opening state or
// artifacts, resolving model credentials, or sending text to a model provider.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/cli"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/destination"
	"github.com/maxwell-cudlitz/inget/internal/model"
)

// queryCheckResult reports only the validated profile, never credentials or stored text.
type queryCheckResult struct {
	Datatype    string `json:"datatype"`
	Destination string `json:"destination"`
	Table       string `json:"table"`
	Storage     string `json:"storage"`
	Model       string `json:"model"`
	Dims        int    `json:"dims"`
	Signature   string `json:"signature"`
}

// queryCheckCommand builds the read-only readiness check. JSON is emitted only after
// every selected destination passes; failure exits nonzero without a partial report.
func queryCheckCommand() *cobra.Command {
	var opts queryOptions
	cmd := &cobra.Command{
		Use: "query-check", Short: "Validate query configuration and destination read access without embedding",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error { return runQueryCheck(cmd, opts) },
	}
	cmd.Flags().StringVar(&opts.datatype, "datatype", "", "check one datatype instead of every configured datatype")
	cmd.Flags().StringVar(&opts.destination, "destination", "", "check this destination instead of the first configured one")
	cmd.Flags().StringVar(&opts.view, "view", "", "check that the selected datatypes declare this view")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "write validated profiles as JSON")
	return cmd
}

// runQueryCheck validates the same destination selection as query, with no embedder call.
func runQueryCheck(cmd *cobra.Command, opts queryOptions) error {
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
	emb := configuredEmbedder(cfg, "")
	_, closeSinks, err := prepareQueryDestinations(cmd.Context(), cfg, emb, datatypes, opts)
	if err != nil {
		return err
	}
	defer closeSinks()
	report := make([]queryCheckResult, 0, len(datatypes))
	for _, dt := range datatypes {
		name, _ := queryDestination(dt, opts.destination)
		d, _ := cfg.Destination(name)
		report = append(report, queryCheckResult{
			Datatype: dt.Name, Destination: name, Table: d.Table, Storage: d.Storage,
			Model: emb.Model(), Dims: emb.Dims(), Signature: emb.Signature(),
		})
	}
	if opts.asJSON {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
			return fmt.Errorf("writing query-check report: %w", err)
		}
		return nil
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "query checks passed for %d datatype(s)\n", len(report))
	if err != nil {
		return fmt.Errorf("writing query-check report: %w", err)
	}
	return nil
}

// prepareQueryDestinations validates the full scope before a query can incur model cost.
// A shared destination opens once, and all pools are closed on success or partial failure.
func prepareQueryDestinations(ctx context.Context, cfg *config.Config, emb model.Embedder,
	datatypes []config.Datatype, opts queryOptions) (map[string]destination.Destination, func(), error) {
	sinks := make(map[string]destination.Destination)
	closeSinks := func() {
		for _, sink := range sinks {
			_ = sink.Close()
		}
	}
	for _, dt := range datatypes {
		name, err := queryDestination(dt, opts.destination)
		if err != nil {
			closeSinks()
			return nil, func() {}, err
		}
		if err := checkQueryView(dt, opts.view); err != nil {
			closeSinks()
			return nil, func() {}, err
		}
		if _, exists := sinks[name]; exists {
			continue
		}
		sink, err := openQueryDestination(ctx, cfg, name, emb)
		if err != nil {
			closeSinks()
			return nil, func() {}, err
		}
		sinks[name] = sink
	}
	return sinks, closeSinks, nil
}
