// The migrate subcommand: bring the state store and every configured destination up to the
// schema this binary carries.
//
// It lives in the entrypoint rather than in internal/cli because it links the state store
// and the destination drivers, and internal/cli is shared with inget-fetch, which needs
// neither. A subcommand that pulls dependencies belongs to the binary that wants them.
package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/cli"
	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/destination"
	"github.com/maxwell-cudlitz/inget/internal/state"
)

// migrateCommand builds `inget migrate`.
func migrateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending schema migrations to the state store and destinations",
		Long: "migrate brings the inget_state schema and every configured destination table up to\n" +
			"the migration set embedded in this binary. It is idempotent: an up-to-date schema is\n" +
			"left alone. It creates no vectors and binds no model; the first run does that.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cli.ConfigPath(cmd))
			if err != nil {
				return fmt.Errorf("loading configuration: %w", err)
			}
			return migrateAll(cmd.Context(), cfg)
		},
	}
}

// migrateAll migrates the state store, then each destination in configuration order.
//
// State comes first because it is the store every command needs; a destination that cannot
// be reached should not stop the state schema from being correct. Each destination is opened
// and closed in turn rather than all at once, so a configuration with several sinks does not
// hold a pool open against every one of them for the duration.
func migrateAll(ctx context.Context, cfg *config.Config) error {
	stateOpts, err := state.FromConfig(cfg)
	if err != nil {
		return err
	}
	store, err := state.Open(ctx, stateOpts)
	if err != nil {
		return err
	}
	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	slog.InfoContext(ctx, "state schema is up to date", "driver", stateOpts.Driver)

	for _, d := range cfg.Destinations {
		if err := migrateDestination(ctx, cfg, d.Name); err != nil {
			return err
		}
	}
	return nil
}

// migrateDestination migrates one named destination.
func migrateDestination(ctx context.Context, cfg *config.Config, name string) error {
	opts, err := destination.FromConfig(cfg, name)
	if err != nil {
		return err
	}
	sink, err := destination.Open(ctx, opts)
	if err != nil {
		return err
	}
	defer func() {
		if err := sink.Close(); err != nil {
			slog.WarnContext(ctx, "closing destination", "destination", name, "error", err)
		}
	}()
	if err := sink.Migrate(ctx); err != nil {
		return err
	}
	slog.InfoContext(ctx, "destination schema is up to date",
		"destination", name, "table", opts.Table, "storage", opts.Storage, "dims", opts.Dims)
	return nil
}
