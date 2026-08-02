// Assembly of the dependency graph a pipeline run needs: state store, artifact store,
// model clients, enrichers and destinations.
//
// It lives in the binary rather than in internal/pipeline because it reads secrets and
// opens paths, both of which are concerns of the entrypoint.
package main

import (
	"context"
	"fmt"

	"github.com/maxwellcudlitz/inget/internal/artifact"
	"github.com/maxwellcudlitz/inget/internal/config"
	"github.com/maxwellcudlitz/inget/internal/destination"
	"github.com/maxwellcudlitz/inget/internal/enrich"
	"github.com/maxwellcudlitz/inget/internal/enrich/refs"
	"github.com/maxwellcudlitz/inget/internal/model"
	"github.com/maxwellcudlitz/inget/internal/pipeline"
	"github.com/maxwellcudlitz/inget/internal/state"
)

// buildDeps constructs the pipeline dependencies from configuration.
//
// A dry run opens no destination. Plan mode writes nothing and embeds nothing, so requiring
// a reachable vector database to answer "what would this cost" would put the cost guard
// behind the very infrastructure someone may not have running yet.
func buildDeps(ctx context.Context, cfg *config.Config, dt config.Datatype, dryRun bool) (pipeline.Deps, *artifact.Store, func(), error) {
	noop := func() {}

	stateOpts, err := state.FromConfig(cfg)
	if err != nil {
		return pipeline.Deps{}, nil, noop, err
	}
	store, err := state.Open(ctx, stateOpts)
	if err != nil {
		return pipeline.Deps{}, nil, noop, err
	}
	closers := []func(){func() { _ = store.Close() }}
	fail := func(err error) (pipeline.Deps, *artifact.Store, func(), error) {
		runAll(closers)
		return pipeline.Deps{}, nil, noop, err
	}

	arts, err := artifact.Open(ctx, artifact.FromConfig(cfg.Artifacts))
	if err != nil {
		return fail(fmt.Errorf("opening artifact store: %w", err))
	}
	closers = append(closers, func() { _ = arts.Close() })

	gen, err := buildGenerator(cfg)
	if err != nil {
		return fail(err)
	}
	emb, err := buildEmbedder(cfg)
	if err != nil {
		return fail(err)
	}

	dests := map[string]destination.Destination{}
	if !dryRun {
		for _, name := range dt.Destinations {
			sink, err := openDestination(ctx, cfg, name, emb)
			if err != nil {
				return fail(err)
			}
			dests[name] = sink
			closers = append(closers, func() { _ = sink.Close() })
		}
	}

	enrichers, err := buildViewEnrichers(cfg, gen, dt)
	if err != nil {
		return fail(err)
	}

	var fragEnricher *enrich.FragmentLLMEnricher
	if dt.FragmentEnricher.Enabled {
		fragEnricher, err = buildFragmentEnricher(cfg, gen, dt)
		if err != nil {
			return fail(err)
		}
	}

	// References resolve against the state store, so they are built after it and share it:
	// a cross-record lookup reads the same rows this run is writing.
	refSet, err := refs.New(dt.References, store, cfg.Secret)
	if err != nil {
		return fail(err)
	}

	deps := pipeline.Deps{
		State:        store,
		Destinations: dests,
		Embedder:     emb,
		Enrichers:    enrichers,
		FragEnricher: fragEnricher,
		Refs:         refSet,
		Config: pipeline.DatatypeConfig{
			Name:             dt.Name,
			Source:           dt.Source,
			Destinations:     dt.Destinations,
			Views:            dt.Views,
			ComposeOrder:     composeOrder(dt),
			ComposeMaxChars:  composeMaxChars(dt),
			DriftThreshold:   dt.Drift(),
			FragmentEnricher: dt.FragmentEnricher.Enabled,
			MetadataFields:   dt.MetadataFields,
		},
	}
	return deps, arts, func() { runAll(closers) }, nil
}

// runAll invokes every cleanup function, most recently added first.
func runAll(closers []func()) {
	for i := len(closers) - 1; i >= 0; i-- {
		closers[i]()
	}
}

// openDestination opens one destination and binds it to the embedder before any row is
// written, so a model or dimension mismatch fails at startup rather than after the first
// item has been generated and paid for (D7).
func openDestination(ctx context.Context, cfg *config.Config, name string, emb model.Embedder) (destination.Destination, error) {
	opts, err := destination.FromConfig(cfg, name)
	if err != nil {
		return nil, err
	}
	sink, err := destination.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := sink.AssertModel(ctx, emb.Model(), emb.Dims(), emb.Signature()); err != nil {
		_ = sink.Close()
		return nil, fmt.Errorf("asserting model on %s: %w", name, err)
	}
	return sink, nil
}
