// CLI integration fixtures run the actual main command in a child test process, backed
// by synthetic pgvector rows and separate owner, writer and SELECT-only reader logins.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maxwell-cudlitz/inget/internal/config"
	"github.com/maxwell-cudlitz/inget/internal/destination"
)

// TestQueryCLIProcess exposes the real main wiring only inside the integration child.
func TestQueryCLIProcess(t *testing.T) {
	if os.Getenv("INGET_QUERY_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"inget"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing child command separator")
}

type queryCLIFixture struct {
	owner      *pgx.Conn
	writer     destination.Destination
	configPath string
	dsn        string
	table      string
	reader     string
}

func newQueryCLIFixture(t *testing.T) *queryCLIFixture {
	t.Helper()
	dsn := os.Getenv("INGET_TEST_PG")
	if dsn == "" {
		t.Skip("INGET_TEST_PG is not set; skipping query CLI integration")
	}
	owner, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	f := &queryCLIFixture{owner: owner, table: "inget_query_cli_" + suffix, reader: "inget_cli_reader_" + suffix}
	writerRole := "inget_cli_writer_" + suffix
	const password = "inget-query-scratch-only"
	for _, role := range []string{writerRole, f.reader} {
		f.exec(t, "CREATE ROLE "+role+" LOGIN PASSWORD '"+password+"'")
		t.Cleanup(func() {
			if _, err := owner.Exec(context.Background(), "DROP OWNED BY "+role); err != nil {
				t.Errorf("removing query fixture grants: %v", err)
			}
			if _, err := owner.Exec(context.Background(), "DROP ROLE "+role); err != nil {
				t.Errorf("removing query fixture role: %v", err)
			}
		})
		f.exec(t, "GRANT USAGE ON SCHEMA public TO "+role)
	}
	opts := destination.Options{Driver: destination.DriverPGVector, DSN: dsn, Table: f.table,
		Storage: destination.StorageHalfvec, Dims: 4, M: 16, EFConstruction: 64, EFSearch: 40}
	owned, err := destination.Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owned.Close() })
	if err := owned.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, sql := range []string{"DROP TABLE " + f.table, "DROP TABLE " + f.table + "_goose_version",
			"DELETE FROM inget_model_registry WHERE table_name='" + f.table + "'"} {
			if _, err := owner.Exec(context.Background(), sql); err != nil {
				t.Errorf("removing query fixture tables: %v", err)
			}
		}
	})
	f.exec(t, "GRANT SELECT, INSERT, UPDATE, DELETE ON "+f.table+", inget_model_registry TO "+writerRole)
	f.exec(t, "GRANT SELECT ON "+f.table+", inget_model_registry TO "+f.reader)
	f.exec(t, "ALTER ROLE "+f.reader+" SET default_transaction_read_only = on")
	opts.DSN = queryRoleDSN(t, dsn, writerRole, password)
	f.writer, err = destination.Open(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.writer.Close() })
	if err := f.writer.AssertModel(t.Context(), "query-test", 4, "openai:dimensions=4,model=query-test"); err != nil {
		t.Fatal(err)
	}
	f.dsn = queryRoleDSN(t, dsn, f.reader, password)
	raw, err := os.ReadFile("testdata/query.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.configPath = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(f.configPath, []byte(strings.ReplaceAll(string(raw), "inget_query_test", f.table)), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func queryRoleDSN(t *testing.T, dsn, role, password string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Roles and passwords are generated without connection-string metacharacters.
	return fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=disable pool_max_conns=1",
		cfg.Host, cfg.Port, cfg.Database, role, password)
}

func (f *queryCLIFixture) exec(t *testing.T, sql string) {
	t.Helper()
	if _, err := f.owner.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}

func (f *queryCLIFixture) command(t *testing.T, env []string, args ...string) ([]byte, error) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, append([]string{"-test.run=^TestQueryCLIProcess$", "--", "--config", f.configPath}, args...)...)
	cmd.Env = append(os.Environ(), "INGET_QUERY_TEST_PROCESS=1", "INGET_QUERY_TEST_DSN="+f.dsn,
		"INGET_QUERY_TEST_EMBEDDER_KEY=", "INGET_QUERY_UNUSED_STATE=", "INGET_QUERY_UNUSED_GENERATOR=",
		"INGET_QUERY_UNUSED_RERANKER=", "INGET_QUERY_UNUSED_SOURCE=")
	cmd.Env = append(cmd.Env, env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return append(output, stderr.Bytes()...), err
	}
	return output, nil
}

// TestQueryCheckProfileNeedsNoSecret verifies the profile shares the writer's signature
// while model credentials are absent; it does not need a database.
func TestQueryCheckProfileNeedsNoSecret(t *testing.T) {
	cfg, err := config.Load("testdata/query.yaml")
	if err != nil {
		t.Fatal(err)
	}
	profile := configuredEmbedder(cfg, "")
	if profile.Signature() != "openai:dimensions=4,model=query-test" {
		t.Fatalf("preflight profile = %s", profile.Signature())
	}
}
