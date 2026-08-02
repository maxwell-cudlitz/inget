// The bridge from configuration to store options.
//
// This is the only place the state implementation knows about internal/config, so tests
// construct Options directly while every command derives them the same way. The postgres DSN
// is resolved here and only here: it is a secret, named by state.dsn_env, and reading it at
// the boundary keeps it out of every other signature in this package.
package state

import (
	"fmt"

	"github.com/maxwellcudlitz/inget/internal/config"
)

// FromConfig converts the state block into store options. The DSN is only required by the
// postgres driver, so a sqlite-only development setup does not need the variable set.
func FromConfig(cfg *config.Config) (Options, error) {
	opts := Options{Driver: cfg.State.Driver, Path: cfg.State.Path}
	if opts.Driver == DriverPostgres {
		dsn, err := cfg.Secret(cfg.State.DSNEnv)
		if err != nil {
			return Options{}, fmt.Errorf("state store DSN: %w", err)
		}
		opts.DSN = dsn
	}
	return opts, nil
}
