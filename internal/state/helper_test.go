// Shared test fixtures for the conformance suite.
//
// Every case runs against every available driver. sqlite always runs, against a fresh file
// in a temporary directory. postgres runs only when INGET_TEST_PG names a DSN, and when it
// does the suite DROPs the inget_state schema of that database before each case: point the
// variable at a scratch database, never at anything you care about.
package state

import (
	"os"
	"path/filepath"
	"testing"
)

// envTestPostgres names the DSN of a throwaway PostgreSQL database. Unset skips the
// postgres half of the suite, which is what CI does.
const envTestPostgres = "INGET_TEST_PG"

// Fixture identities. The datatype carries a slash because every real one does.
const (
	testDatatype = "github/repo"
	testSource   = "github"
	testItem     = "maxwellcudlitz/inget"
	testRun      = "01K1QZ8Q0000000000000000AA"
	testConfig   = "sha256:config"
)

// storeCase is one conformance assertion. The suite runs each against each driver.
type storeCase struct {
	name string
	run  func(t *testing.T, h harness)
}

// harness is a migrated store plus a way to open a second, independent store against the
// same target, which the locking cases need.
type harness struct {
	Store
	Open func(t *testing.T) Store
}

// driverFixture builds a harness for one driver.
type driverFixture struct {
	name       string
	newHarness func(t *testing.T) harness
}

// drivers returns the fixtures available in this environment.
func drivers(t *testing.T) []driverFixture {
	t.Helper()
	fixtures := []driverFixture{{name: DriverSQLite, newHarness: sqliteHarness}}
	if os.Getenv(envTestPostgres) == "" {
		t.Logf("%s is not set; running the conformance suite against sqlite only", envTestPostgres)
		return fixtures
	}
	return append(fixtures, driverFixture{name: DriverPostgres, newHarness: postgresHarness})
}

// sqliteHarness puts the database in a temporary directory, so cases cannot see each
// other's rows.
func sqliteHarness(t *testing.T) harness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	open := func(t *testing.T) Store {
		t.Helper()
		return migrated(t, Options{Driver: DriverSQLite, Path: path})
	}
	return harness{Store: open(t), Open: open}
}

// postgresHarness wipes the schema before handing back a store, because one database serves
// every case and a leftover row from the previous one would make failures depend on order.
func postgresHarness(t *testing.T) harness {
	t.Helper()
	dsn := os.Getenv(envTestPostgres)
	wipePostgres(t, dsn)
	open := func(t *testing.T) Store {
		t.Helper()
		return migrated(t, Options{Driver: DriverPostgres, DSN: dsn})
	}
	return harness{Store: open(t), Open: open}
}

// wipePostgres drops the state schema of the test database.
func wipePostgres(t *testing.T, dsn string) {
	t.Helper()
	s := openStore(t, Options{Driver: DriverPostgres, DSN: dsn})
	if _, err := s.exec(t.Context(), "DROP SCHEMA IF EXISTS "+schemaName+" CASCADE"); err != nil {
		t.Fatalf("wiping the %s schema: %v", schemaName, err)
	}
}

// migrated opens a store and brings its schema up. Migrating an already-current schema is a
// no-op, so a second store against the same target is safe to migrate again.
func migrated(t *testing.T, opts Options) Store {
	t.Helper()
	s := openStore(t, opts)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// openStore opens a store and closes it when the test ends. It returns the concrete type so
// that fixtures can run statements the Store interface deliberately does not expose.
func openStore(t *testing.T, opts Options) *store {
	t.Helper()
	opened, err := Open(t.Context(), opts)
	if err != nil {
		t.Fatalf("Open %s: %v", opts.Driver, err)
	}
	t.Cleanup(func() {
		if err := opened.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	s, ok := opened.(*store)
	if !ok {
		t.Fatalf("Open returned %T, want *store", opened)
	}
	return s
}

// seedItem writes one item with the fixture identity and the given fingerprint.
func seedItem(t *testing.T, s Store, itemID, fingerprint string) {
	t.Helper()
	it := Item{
		ID:          itemID,
		Source:      testSource,
		Fingerprint: fingerprint,
		Metadata:    map[string]string{"language": "Go"},
		RunID:       testRun,
	}
	if err := s.PutItem(t.Context(), testDatatype, it); err != nil {
		t.Fatalf("PutItem %s: %v", itemID, err)
	}
}

// seedRun starts a run so that work rows have one to belong to.
func seedRun(t *testing.T, s Store, runID string) {
	t.Helper()
	r := Run{ID: runID, Binary: "inget", Datatype: testDatatype, Scope: ScopeFull, ConfigHash: testConfig}
	if err := s.StartRun(t.Context(), r); err != nil {
		t.Fatalf("StartRun %s: %v", runID, err)
	}
}
