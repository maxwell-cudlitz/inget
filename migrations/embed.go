// Package migrations embeds the goose SQL migrations so that a single binary carries the
// schemas it needs and `inget migrate` works from any working directory.
//
// The .sql files stay here, at the repository root, rather than beside the package that
// runs them: go:embed cannot reach outside its own directory, so a Go file has to live
// next to the SQL, and the documented layout puts migrations at the root. Each driver gets
// its own directory because the dialects differ in type names, not just in syntax.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed state/postgres/*.sql state/sqlite/*.sql
//go:embed destination/pgvector/*.sql
var files embed.FS

// State returns the state-store migrations for one driver, rooted so that goose sees the
// .sql files directly. An unknown driver is an error rather than an empty filesystem,
// because goose treats "no migrations" as "already up to date".
func State(driver string) (fs.FS, error) {
	return sub("state", driver)
}

// Destination returns the migrations for one destination driver. The directory is keyed by
// driver rather than by SQL dialect, because a destination need not be a SQL database at
// all: the next one may ship no .sql files and be absent here entirely.
//
// Unlike the state set these are Go text/templates, not runnable SQL: a vector column's
// width and storage type come from configuration and PostgreSQL cannot parameterise a type
// modifier. internal/destination renders them before handing them to goose, which is why
// this returns the raw files and names no defaults.
func Destination(driver string) (fs.FS, error) {
	return sub("destination", driver)
}

// sub roots the embedded filesystem at one schema's driver directory.
func sub(schema, driver string) (fs.FS, error) {
	dir := schema + "/" + driver
	entries, err := files.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return nil, fmt.Errorf("no %s migrations for driver %q", schema, driver)
	}
	rooted, err := fs.Sub(files, dir)
	if err != nil {
		return nil, fmt.Errorf("locating %s %s migrations: %w", driver, schema, err)
	}
	return rooted, nil
}
