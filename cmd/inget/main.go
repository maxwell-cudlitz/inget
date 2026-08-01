// Command inget consumes committed artifact runs from the blob store, runs the
// invalidation cascade and enrichment pipeline over them, and upserts view vectors into
// destinations. It holds no source credentials.
//
// This entrypoint only wires the shared root command; the run, plan, migrate, reindex,
// state, query and eval subcommands arrive in later implementation steps.
package main

import "github.com/maxwellcudlitz/inget/internal/cli"

func main() {
	cli.Execute(cli.App{
		Name:  "inget",
		Short: "Enrich fetched artifacts into searchable vectors",
		Long: "inget reads committed artifact runs from the blob store, derives per-fragment\n" +
			"and per-view enrichments through a staged invalidation cascade, embeds them, and\n" +
			"upserts the result into a vector destination.",
	})
}
