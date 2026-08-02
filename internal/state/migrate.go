// Schema migration.
//
// goose is used as a library rather than a CLI (D15), against the migration set embedded in
// the binary, so `inget migrate` needs nothing on disk. The PostgreSQL schema is created
// here rather than inside migration 00001: goose records its version table through the
// connection's search_path, and if the schema did not exist yet that table would land in
// public and the next run would believe nothing had been applied.
package state

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"

	"github.com/maxwell-cudlitz/inget/migrations"
)

// Migrate applies every pending migration for the driver. It is idempotent: applying an
// up-to-date schema logs nothing and changes nothing.
func (s *store) Migrate(ctx context.Context) error {
	if s.d.name == DriverPostgres {
		if _, err := s.exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schemaName); err != nil {
			return fmt.Errorf("creating schema %s: %w", schemaName, err)
		}
	}
	fsys, err := migrations.State(s.d.name)
	if err != nil {
		return fmt.Errorf("loading state migrations: %w", err)
	}
	provider, err := goose.NewProvider(s.d.goose, s.db, fsys, goose.WithLogger(goose.NopLogger()))
	if err != nil {
		return fmt.Errorf("preparing %s state migrations: %w", s.d.name, err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrating %s state store: %w", s.d.name, err)
	}
	for _, r := range results {
		slog.InfoContext(ctx, "applied state migration",
			"driver", s.d.name, "version", r.Source.Version, "source", r.Source.Path)
	}
	return nil
}
