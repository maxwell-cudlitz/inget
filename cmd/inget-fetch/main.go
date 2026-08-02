// Command inget-fetch enumerates items from a configured source, fragments them, and writes
// immutable content-addressed artifacts to the blob store. It holds source credentials but no
// model credentials, so a fetch failure can never waste LLM spend.
//
// Fetching is the binary's only action, so it is the root command's own behaviour rather than a
// subcommand: `inget-fetch --source github --limit 3`.
//
// Connector drivers are linked by importing them for their registration side effect. A binary
// that omitted one would fail at open time with a clear "unknown source driver", which is the
// same shape of failure the artifact and destination registries produce.
package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/maxwellcudlitz/inget/internal/cli"
	_ "github.com/maxwellcudlitz/inget/internal/source/github" // registers the github driver
)

func main() {
	var opts fetchOptions

	cli.Execute(cli.App{
		Name:  "inget-fetch",
		Short: "Fetch source items into immutable artifacts",
		Long: "inget-fetch enumerates items from a configured source, splits them into\n" +
			"fingerprinted fragments, and writes an atomically committed artifact run to the\n" +
			"blob store for inget to consume.",
		Flags: func(flags *pflag.FlagSet) { opts.register(flags) },
		Run: func(cmd *cobra.Command, _ []string) error {
			return runFetch(cmd, opts)
		},
	})
}
