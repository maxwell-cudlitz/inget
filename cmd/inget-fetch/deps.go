// Assembly of what a fetch run needs: the state store, the artifact store and the connector.
//
// It lives in the binary rather than in internal/fetch because it reads secrets and opens paths,
// both of which are concerns of the entrypoint. Connector drivers are linked by importing them
// for their side effect in main.go, so this file names no source by name.
package main

import (
	"context"
	"fmt"

	"github.com/maxwell-cudlitz/inget/internal/artifact"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/fetch"
	"github.com/maxwell-cudlitz/inget/internal/source"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// buildDeps opens everything one datatype's fetch needs, returning a cleanup that closes them in
// reverse order.
//
// A dry run still opens the state store and the connector: the whole point of --dry-run is to
// report what the level-0 guard would skip, which is a question about persisted fingerprints. It
// opens the artifact store too, because enumerating without one would report work that could not
// be written anywhere.
func buildDeps(ctx context.Context, cfg *config.Config, dt config.Datatype) (fetch.Deps, func(), error) {
	noop := func() {}

	stateOpts, err := state.FromConfig(cfg)
	if err != nil {
		return fetch.Deps{}, noop, err
	}
	store, err := state.Open(ctx, stateOpts)
	if err != nil {
		return fetch.Deps{}, noop, err
	}
	closers := []func(){func() { _ = store.Close() }}
	fail := func(err error) (fetch.Deps, func(), error) {
		runAll(closers)
		return fetch.Deps{}, noop, err
	}

	arts, err := artifact.Open(ctx, artifact.FromConfig(cfg.Artifacts))
	if err != nil {
		return fail(fmt.Errorf("opening artifact store: %w", err))
	}
	closers = append(closers, func() { _ = arts.Close() })

	opts, err := source.FromConfig(cfg, dt.Source)
	if err != nil {
		return fail(err)
	}
	conn, err := source.Open(ctx, opts)
	if err != nil {
		return fail(err)
	}
	closers = append(closers, func() { _ = conn.Close() })

	if !supports(conn, dt.Name) {
		return fail(fmt.Errorf("source %s does not produce datatype %s (produces: %v)",
			dt.Source, dt.Name, conn.Datatypes()))
	}
	return fetch.Deps{State: store, Artifacts: arts, Connector: conn}, func() { runAll(closers) }, nil
}

// supports reports whether a connector produces a datatype. Checking here turns a configuration
// mistake into a message before the run takes a lock.
func supports(conn source.Connector, datatype string) bool {
	for _, name := range conn.Datatypes() {
		if name == datatype {
			return true
		}
	}
	return false
}

// runAll invokes every cleanup function, most recently added first.
func runAll(closers []func()) {
	for i := len(closers) - 1; i >= 0; i-- {
		closers[i]()
	}
}
