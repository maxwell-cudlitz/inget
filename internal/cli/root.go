// Package cli assembles the cobra command trees shared by the inget binaries.
//
// Both entrypoints — inget and inget-fetch — need identical scaffolding: a root
// command, a version subcommand, logger initialisation before any subcommand runs, and
// a single place where a returned error is reported and turned into an exit status.
// That scaffolding lives here so neither main package repeats it.
//
// Subcommand trees are supplied by the callers and grow over later implementation
// steps; this package deliberately knows nothing about fetching or enrichment.
package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/maxwellcudlitz/inget/internal/logging"
)

// App identifies a binary to NewRoot.
type App struct {
	Name  string // binary name, e.g. "inget"
	Short string // one-line description shown in help
	Long  string // extended description shown in `<binary> --help`
}

// NewRoot builds the root command for app with the version subcommand attached and
// logging initialised in PersistentPreRunE.
//
// Errors are silenced at the cobra level and reported once by Execute, which keeps a
// runtime failure from printing the full usage text after it.
func NewRoot(app App) *cobra.Command {
	root := &cobra.Command{
		Use:           app.Name,
		Short:         app.Short,
		Long:          app.Long,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Running the bare binary is not an error; show help and exit zero.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			// Step 2 replaces these defaults with the loaded configuration's `log`
			// block. Environment overrides already apply, so operators can raise
			// verbosity today via LOG_LEVEL.
			if _, err := logging.Setup(logging.Default()); err != nil {
				return fmt.Errorf("configuring logger: %w", err)
			}
			return nil
		},
	}
	root.AddCommand(versionCommand(app))
	return root
}

// Execute builds the root command for app, attaches sub, runs it, and exits non-zero
// on failure. This is the outermost error handler: the failure is logged exactly once
// here, so nothing below it should log an error it also returns.
func Execute(app App, sub ...*cobra.Command) {
	root := NewRoot(app)
	root.AddCommand(sub...)
	if err := root.Execute(); err != nil {
		// The logger may not exist yet if PersistentPreRunE itself failed; slog's
		// default handler writes to stderr, which is where diagnostics belong either
		// way.
		slog.Error("command failed", "command", app.Name, "error", err)
		os.Exit(1)
	}
}
