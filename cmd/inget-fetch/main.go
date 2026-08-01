// Command inget-fetch enumerates items from a configured source, fragments them, and
// writes immutable content-addressed artifacts to the blob store. It holds source
// credentials but no model credentials, so a fetch failure can never waste LLM spend.
//
// This entrypoint only wires the shared root command; the fetch surface itself arrives
// with the connector implementation steps.
package main

import "github.com/maxwellcudlitz/inget/internal/cli"

func main() {
	cli.Execute(cli.App{
		Name:  "inget-fetch",
		Short: "Fetch source items into immutable artifacts",
		Long: "inget-fetch enumerates items from a configured source, splits them into\n" +
			"fingerprinted fragments, and writes an atomically committed artifact run to the\n" +
			"blob store for inget to consume.",
	})
}
