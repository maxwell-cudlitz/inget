// The `inget run` command: execute the enrichment pipeline for one or all datatypes.
//
// It resolves dependencies from configuration, constructs the pipeline Deps, and calls
// pipeline.Run. Signal handling is set up here, at the outermost layer that knows the
// process boundary, and the resulting channel is handed to the pipeline through the
// context: the pool that drains on SIGTERM must be watching the signal handler's channel
// and not one of its own.
package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/cli"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/pipeline"
)

// runOptions are the flags run and plan share.
type runOptions struct {
	dryRun                   bool
	only                     []string
	limit                    int
	estimateProfile          string
	rebuildOnSignatureChange bool
	cachedFragmentsOnly      bool
	indexedOnly              bool
	cachedFragmentSignatures []string
}

// runCommand builds `inget run`.
func runCommand() *cobra.Command {
	var opts runOptions

	cmd := &cobra.Command{
		Use:   "run [datatype]",
		Short: "Execute the enrichment pipeline for a datatype",
		Long: "run opens the latest artifact run for the given datatype, reconciles it against\n" +
			"persisted state, and processes changed items through the enrichment cascade. If no\n" +
			"datatype is given, all configured datatypes are processed sequentially.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return execute(cmd, args, opts)
		},
	}
	cmd.Flags().StringSliceVar(&opts.only, "only", nil, "restrict processing to these item IDs")
	cmd.Flags().IntVar(&opts.limit, "limit", 0, "process at most N changed items")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "report the work and its estimated cost without doing it")
	cmd.Flags().BoolVar(&opts.rebuildOnSignatureChange, "rebuild-on-signature-change", false,
		"explicitly process every item when a prompt or enricher signature has changed")
	cmd.Flags().BoolVar(&opts.cachedFragmentsOnly, "cached-fragments-only", false,
		"require matching cached file summaries; refuse all fragment generation")
	cmd.Flags().BoolVar(&opts.indexedOnly, "indexed-only", false,
		"restrict processing to previously indexed items; intersect --only before --limit")
	cmd.Flags().StringSliceVar(&opts.cachedFragmentSignatures, "cached-fragment-signatures", nil,
		"ordered historical fragment signatures allowed with --cached-fragments-only; prefer the current signature")
	return cmd
}

// execute resolves the datatypes named on the command line and runs each one.
func execute(cmd *cobra.Command, args []string, opts runOptions) error {
	if err := validateCachedFragmentOptions(cmd, opts); err != nil {
		return err
	}
	cfg, err := config.Load(cli.ConfigPath(cmd))
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	datatypes, err := selectDatatypes(cfg, args)
	if err != nil {
		return err
	}
	if opts.estimateProfile != "" && len(datatypes) != 1 {
		return fmt.Errorf("--estimate-profile requires selecting a single datatype")
	}

	ctx, cancel, shutdownCh := pipeline.NotifyShutdown(cmd.Context())
	defer cancel()
	ctx = pipeline.WithShutdown(ctx, shutdownCh)

	for _, dt := range datatypes {
		select {
		case <-shutdownCh:
			slog.InfoContext(ctx, "shutdown requested, skipping remaining datatypes")
			return nil
		default:
		}
		if err := runOneDatatype(ctx, cfg, dt, opts); err != nil {
			return err
		}
	}
	return nil
}

// runOneDatatype sets up deps and runs the pipeline for a single datatype.
func runOneDatatype(ctx context.Context, cfg *config.Config, dt config.Datatype, opts runOptions) error {
	deps, arts, cleanup, err := buildDeps(ctx, cfg, dt, opts.dryRun)
	if err != nil {
		return err
	}
	defer cleanup()

	configHash, err := cfg.ConfigHash(dt.Source, dt.Name)
	if err != nil {
		return fmt.Errorf("computing config hash: %w", err)
	}

	gen := cfg.Models.Generator
	viewGen := cfg.Models.EffectiveViewGenerator()
	rc := pipeline.RunConfig{
		Binary:                   "inget",
		Concurrency:              gen.Concurrency,
		ConfigHash:               configHash,
		DryRun:                   opts.dryRun,
		Only:                     opts.only,
		Limit:                    opts.limit,
		RebuildOnSignatureChange: opts.rebuildOnSignatureChange,
		CachedFragmentsOnly:      opts.cachedFragmentsOnly,
		IndexedOnly:              opts.indexedOnly,
		CachedFragmentSignatures: opts.cachedFragmentSignatures,
		Pricing: pipeline.Pricing{
			PerMTokIn:       gen.PricePerMTokIn,
			PerMTokOut:      gen.PricePerMTokOut,
			MaxOutputTokens: gen.MaxOutputTokens,
		},
		ViewPricing: &pipeline.Pricing{
			PerMTokIn:       viewGen.PricePerMTokIn,
			PerMTokOut:      viewGen.PricePerMTokOut,
			MaxOutputTokens: viewGen.MaxOutputTokens,
		},
		MaxReferenceDepth: cfg.Enrich.MaxReferenceDepth,
		MaxCascadePerRun:  cfg.Enrich.MaxCascadePerRun,
	}
	if opts.estimateProfile != "" {
		profile, err := loadEstimateProfile(opts.estimateProfile)
		if err != nil {
			return err
		}
		rc.EstimateProfile = profile
	}

	plan, stats, err := pipeline.Run(ctx, deps, arts, rc)
	if err != nil {
		return fmt.Errorf("running pipeline for %s: %w", dt.Name, err)
	}

	if opts.dryRun {
		return reportPlan(plan)
	}
	if stats != nil {
		slog.InfoContext(ctx, "run complete",
			"datatype", dt.Name,
			"processed", stats.ItemsProcessed,
			"failed", stats.ItemsFailed,
			"views_generated", stats.ViewsGenerated,
			"embeddings", stats.EmbeddingsStored)
	}
	return nil
}
