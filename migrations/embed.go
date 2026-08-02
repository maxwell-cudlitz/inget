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
var files embed.FS

// State returns the state-store migrations for one driver, rooted so that goose sees the
// .sql files directly. An unknown driver is an error rather than an empty filesystem,
// because goose treats "no migrations" as "already up to date".
func State(driver string) (fs.FS, error) {
	switch driver {
	case "postgres", "sqlite":
		sub, err := fs.Sub(files, "state/"+driver)
		if err != nil {
			return nil, fmt.Errorf("locating %s state migrations: %w", driver, err)
		}
		return sub, nil
	default:
		return nil, fmt.Errorf("no state migrations for driver %q", driver)
	}
}
