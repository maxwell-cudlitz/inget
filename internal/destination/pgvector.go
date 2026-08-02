// The pgvector driver: connection, schema migration, and the D7 model binding.
//
// This driver talks to pgx directly rather than through database/sql, because CopyFrom is
// pgx's and BulkLoad needs it. goose wants a *sql.DB, so Migrate adapts the same pool for
// the duration of the migration; nothing else in the package uses that handle.
package destination

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"testing/fstest"
	"text/template"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/maxwell-cudlitz/inget/migrations"
)

// registryTable holds one row per destination table, keyed by table name (D7). It is shared
// by every destination in a database on purpose: the binding is a property of a table, and
// one registry makes `inget state show` able to list every binding from one place.
const registryTable = "inget_model_registry"

// minPGVector is the extension version iterative index scans arrived in. Below it a
// filtered search silently loses recall rather than failing, which is why the check is here
// and not left to the first query (D9).
const minPGVector = "0.8.0"

func init() { Register(DriverPGVector, openPGVector) }

// pgvectorStore is one destination table plus the binding it has been asserted against.
type pgvectorStore struct {
	pool *pgxpool.Pool
	opts Options

	mu    sync.RWMutex
	model binding
}

// openPGVector connects and verifies the connection.
func openPGVector(ctx context.Context, opts Options) (Destination, error) {
	pool, err := pgxpool.New(ctx, opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("creating destination connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to destination %s: %w", opts.Table, err)
	}
	return &pgvectorStore{pool: pool, opts: opts}, nil
}

// Close releases the pool.
func (p *pgvectorStore) Close() error {
	p.pool.Close()
	return nil
}

// Migrate renders the embedded template for this destination's shape and applies it.
//
// Each destination gets its own goose version table, derived from its own table name, so
// two destinations in one database migrate independently instead of the second concluding
// that version 1 was already applied to it.
func (p *pgvectorStore) Migrate(ctx context.Context) error {
	fsys, err := p.renderMigrations()
	if err != nil {
		return err
	}
	db := stdlib.OpenDBFromPool(p.pool)
	defer func() { _ = db.Close() }() // returns the connections to the pool; does not close it

	provider, err := goose.NewProvider(database.DialectPostgres, db, fsys,
		goose.WithLogger(goose.NopLogger()),
		goose.WithTableName(p.opts.Table+"_goose_version"))
	if err != nil {
		return fmt.Errorf("preparing destination migrations for %s: %w", p.opts.Table, err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrating destination %s: %w", p.opts.Table, err)
	}
	for _, r := range results {
		slog.InfoContext(ctx, "applied destination migration",
			"table", p.opts.Table, "version", r.Source.Version, "source", r.Source.Path)
	}
	return p.checkExtension(ctx)
}

// renderMigrations substitutes this destination's shape into the embedded templates and
// returns them as a filesystem goose can read.
func (p *pgvectorStore) renderMigrations() (fstest.MapFS, error) {
	raw, err := migrations.Destination(DriverPGVector)
	if err != nil {
		return nil, fmt.Errorf("loading destination migrations: %w", err)
	}
	tmpl, err := template.ParseFS(raw, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("parsing destination migrations: %w", err)
	}
	params := struct {
		Table, Storage, Ops, Registry string
		Dims, M, EFConstruction       int
	}{
		Table:          p.opts.Table,
		Storage:        p.opts.Storage,
		Ops:            p.opts.Storage + "_cosine_ops",
		Registry:       registryTable,
		Dims:           p.opts.Dims,
		M:              p.opts.M,
		EFConstruction: p.opts.EFConstruction,
	}
	rendered := fstest.MapFS{}
	for _, t := range tmpl.Templates() {
		var out bytes.Buffer
		if err := t.Execute(&out, params); err != nil {
			return nil, fmt.Errorf("rendering destination migration %s: %w", t.Name(), err)
		}
		rendered[t.Name()] = &fstest.MapFile{Data: out.Bytes()}
	}
	return rendered, nil
}

// checkExtension refuses a pgvector too old for iterative index scans.
func (p *pgvectorStore) checkExtension(ctx context.Context) error {
	var installed string
	err := p.pool.QueryRow(ctx,
		`SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&installed)
	if err != nil {
		return fmt.Errorf("reading the installed pgvector version: %w", err)
	}
	if compareVersions(installed, minPGVector) < 0 {
		return fmt.Errorf("pgvector %s is installed but %s or newer is required: below it an HNSW scan "+
			"filters after the fact, so a search restricted to one view silently loses recall (D9)",
			installed, minPGVector)
	}
	return nil
}

// AssertModel binds the table to one embedder or verifies the existing binding.
//
// The insert is unconditional and conflict-tolerant, then the row is read back and
// compared, so two processes racing to bind the same fresh table agree on the winner
// instead of one of them overwriting the other.
func (p *pgvectorStore) AssertModel(ctx context.Context, model string, dims int, signature string) error {
	switch {
	case model == "":
		return errors.New("AssertModel needs a model name")
	case signature == "":
		return errors.New("AssertModel needs an embedder signature")
	case dims != p.opts.Dims:
		return fmt.Errorf("embedder yields %d dimensions but destination %s was migrated for %d; "+
			"align models.embedder with the destination and run `inget reindex`",
			dims, p.opts.Table, p.opts.Dims)
	}
	if _, err := p.pool.Exec(ctx,
		`INSERT INTO `+registryTable+` (table_name, model, dims, signature)
		 VALUES ($1, $2, $3, $4) ON CONFLICT (table_name) DO NOTHING`,
		p.opts.Table, model, dims, signature); err != nil {
		return fmt.Errorf("binding destination %s to model %s: %w", p.opts.Table, model, err)
	}
	var have binding
	if err := p.pool.QueryRow(ctx,
		`SELECT model, dims, signature FROM `+registryTable+` WHERE table_name = $1`,
		p.opts.Table).Scan(&have.Model, &have.Dims, &have.Signature); err != nil {
		return fmt.Errorf("reading the model binding of destination %s: %w", p.opts.Table, err)
	}
	if have.Model != model || have.Dims != dims || have.Signature != signature {
		return fmt.Errorf("destination %s is bound to model %q (%d dims, signature %q) but this run uses "+
			"model %q (%d dims, signature %q); the rows already there came from the other model, so run "+
			"`inget reindex --destination %s` rather than mixing two vector spaces",
			p.opts.Table, have.Model, have.Dims, have.Signature, model, dims, signature, p.opts.Table)
	}
	p.mu.Lock()
	p.model = have
	p.mu.Unlock()
	return nil
}

// binding returns the asserted binding, if any.
func (p *pgvectorStore) binding() binding {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.model
}

// vectorCast is the type modifier every embedding literal is cast to, built once from the
// configured storage and width.
func (p *pgvectorStore) vectorCast() string {
	return "::" + p.opts.Storage + "(" + strconv.Itoa(p.opts.Dims) + ")"
}

// inTx runs fn in a transaction, rolling back on error and on panic.
func (p *pgvectorStore) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	committed = true
	return nil
}
