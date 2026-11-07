// Restricted role fixtures use disposable database logins rather than SET ROLE: startup
// defaults, grants and the one-connection pool are the same boundary as a real reader.
package destination

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// restrictedQueryStores grants one writer and one reader access to the existing scratch
// tables. INGET_TEST_PG must name a disposable owner with CREATE ROLE, as in the runbook.
func restrictedQueryStores(t *testing.T, owner *pgvectorStore) (*pgvectorStore, *pgvectorStore) {
	t.Helper()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	writer := restrictedQueryStore(t, owner, "inget_writer_"+suffix, false)
	reader := restrictedQueryStore(t, owner, "inget_reader_"+suffix, true)
	return writer, reader
}

func restrictedQueryStore(t *testing.T, owner *pgvectorStore, role string, readOnly bool) *pgvectorStore {
	t.Helper()
	name := pgx.Identifier{role}.Sanitize()
	// This fixed password belongs only to the throwaway integration database.
	const password = "inget-scratch-role"
	if _, err := owner.pool.Exec(t.Context(), "CREATE ROLE "+name+" LOGIN PASSWORD '"+password+"'"); err != nil {
		t.Fatalf("creating scratch role: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, statement := range []string{"DROP OWNED BY " + name, "DROP ROLE " + name} {
			if _, err := owner.pool.Exec(ctx, statement); err != nil {
				t.Errorf("removing scratch role: %v", err)
			}
		}
	})
	grants := []string{"GRANT USAGE ON SCHEMA public TO " + name}
	if readOnly {
		grants = append(grants,
			"GRANT SELECT ON "+owner.opts.Table+", "+registryTable+" TO "+name,
			"ALTER ROLE "+name+" SET default_transaction_read_only = on")
	} else {
		grants = append(grants, "GRANT SELECT, INSERT, UPDATE, DELETE ON "+owner.opts.Table+", "+registryTable+" TO "+name)
	}
	for _, statement := range grants {
		if _, err := owner.pool.Exec(t.Context(), statement); err != nil {
			t.Fatalf("granting scratch access: %v", err)
		}
	}
	cfg, err := pgxpool.ParseConfig(requirePostgres(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = role, password
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	return &pgvectorStore{pool: pool, opts: owner.opts}
}
