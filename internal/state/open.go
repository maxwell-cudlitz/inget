// Opening a store: options, driver selection and connection setup.
package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// DefaultSQLitePath is where the sqlite driver keeps its database when config names no
// path, matching the commented default in the shipped config.yaml.
const DefaultSQLitePath = "./.inget/state.db"

// Options configures a Store. It mirrors the state block of the configuration; see
// FromConfig for the bridge.
type Options struct {
	Driver string // postgres | sqlite
	DSN    string // postgres connection string, resolved from dsn_env
	Path   string // sqlite database file
}

// normalize fills in defaults and rejects combinations that cannot work.
func (o Options) normalize() (Options, error) {
	if o.Driver == "" {
		return o, errors.New("state driver is required")
	}
	switch o.Driver {
	case DriverPostgres:
		if o.DSN == "" {
			return o, errors.New("state driver postgres requires a DSN")
		}
	case DriverSQLite:
		if o.Path == "" {
			o.Path = DefaultSQLitePath
		}
	}
	return o, nil
}

// store is the one implementation. The dialect it holds accounts for every difference
// between the drivers; see dialect.go.
type store struct {
	db   *sql.DB
	d    dialect
	pool *pgxpool.Pool // postgres only, closed after db
}

// Open connects to the state store described by opts and verifies the connection. It does
// not migrate; call Migrate for that.
func Open(ctx context.Context, opts Options) (Store, error) {
	opts, err := opts.normalize()
	if err != nil {
		return nil, err
	}
	d, err := lookupDialect(opts.Driver)
	if err != nil {
		return nil, err
	}
	s := &store{d: d}
	switch d.name {
	case DriverPostgres:
		if err := s.openPostgres(ctx, opts.DSN); err != nil {
			return nil, err
		}
	case DriverSQLite:
		if err := s.openSQLite(opts.Path); err != nil {
			return nil, err
		}
	}
	if err := s.db.PingContext(ctx); err != nil {
		_ = s.Close() // the ping failure is the one worth reporting
		return nil, fmt.Errorf("connecting to %s state store: %w", d.name, err)
	}
	return s, nil
}

// openPostgres builds a pgxpool and adapts it to database/sql.
//
// The pool is pgx's, so a future need for binary COPY or listen/notify has a native handle
// to reach for; the database/sql wrapper is what lets the statements in this package serve
// both drivers. search_path is a startup parameter rather than a per-statement
// qualification: it is set once per connection, and naming a schema that does not exist yet
// is not an error, so Migrate can create it afterwards.
func (s *store) openPostgres(ctx context.Context, dsn string) error {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parsing state DSN: %w", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName + ", public"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("creating state connection pool: %w", err)
	}
	s.pool = pool
	s.db = stdlib.OpenDBFromPool(pool)
	return nil
}

// openSQLite opens the file, creating its directory, with the three settings that make a
// file-backed database usable by more than one connection at a time: WAL so readers do not
// block the writer, a busy timeout so a contended write waits instead of failing, and
// immediate transactions so a write transaction takes the write lock up front rather than
// deadlocking on an upgrade.
func (s *store) openSQLite(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating state directory %s: %w", dir, err)
		}
	}
	dsn := "file:" + path + "?" + url.Values{
		"_pragma": {"busy_timeout(5000)", "journal_mode(wal)"},
		"_txlock": {"immediate"},
	}.Encode()

	db, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return fmt.Errorf("opening sqlite state store %s: %w", path, err)
	}
	s.db = db
	return nil
}

// Close releases the pool. The database/sql handle goes first: closing it returns its
// connections to the pgx pool, which is only then safe to shut down.
func (s *store) Close() error {
	var err error
	if s.db != nil {
		if closeErr := s.db.Close(); closeErr != nil {
			err = fmt.Errorf("closing state store: %w", closeErr)
		}
	}
	if s.pool != nil {
		s.pool.Close()
	}
	return err
}
