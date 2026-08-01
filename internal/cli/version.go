// Build metadata and the `version` subcommand.
//
// Values are injected at link time by the Makefile and by goreleaser. When they are
// absent — `go run`, `go install`, or a plain `go build` — Build falls back to the
// module version and VCS stamps the toolchain embeds in the binary, so `version`
// reports something truthful in every build mode.
package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// Injected via -ldflags -X. Do not read these directly; call Build instead.
var (
	Version string
	Commit  string
	Date    string
)

// unknown is reported for any stamp that neither the linker nor the toolchain supplied.
const unknown = "unknown"

// BuildInfo is the resolved build metadata for the running binary.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
}

// Build resolves the build metadata, preferring linker-injected values and falling back
// to the embedded build info.
func Build() BuildInfo {
	b := BuildInfo{Version: Version, Commit: Commit, Date: Date, Go: runtime.Version()}
	if info, ok := debug.ReadBuildInfo(); ok {
		if b.Version == "" {
			b.Version = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if b.Commit == "" {
					b.Commit = s.Value
				}
			case "vcs.time":
				if b.Date == "" {
					b.Date = s.Value
				}
			case "vcs.modified":
				// `git describe --dirty` may already have said so; do not repeat it.
				if s.Value == "true" && !strings.HasSuffix(b.Version, "-dirty") {
					b.Version += "-dirty"
				}
			}
		}
	}
	if b.Version == "" {
		b.Version = "dev"
	}
	if b.Commit == "" {
		b.Commit = unknown
	}
	if b.Date == "" {
		b.Date = unknown
	}
	return b
}

// String renders the metadata as one line.
func (b BuildInfo) String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s)", b.Version, b.Commit, b.Date, b.Go)
}

// versionCommand prints build metadata to stdout, since it is program output rather
// than a diagnostic. It skips the persistent logger setup so that `version` works even
// when the environment is misconfigured.
func versionCommand(app App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build metadata",
		Args:  cobra.NoArgs,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", app.Name, Build()); err != nil {
				return fmt.Errorf("writing version: %w", err)
			}
			return nil
		},
	}
}
