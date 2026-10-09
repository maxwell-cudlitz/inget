// The `inget plan` command: report what a run would do, and what it would cost, without
// doing it.
//
// It reconciles artifacts against state, counts the fragment derivations and view
// generations the work set implies, and prices them from the generator's configured rates.
// No LLM call, embedding or destination write happens, and no destination is even opened.
// Output is JSON on stdout; the summary goes to the log.
package main

import (
	"github.com/spf13/cobra"
)

// planCommand builds `inget plan`.
func planCommand() *cobra.Command {
	opts := runOptions{dryRun: true}

	cmd := &cobra.Command{
		Use:   "plan [datatype]",
		Short: "Show what a run would do and what it would cost",
		Long: "plan reconciles the latest artifact run against persisted state and reports what\n" +
			"items would be processed, skipped or tombstoned, how many derivations and view\n" +
			"generations that implies, and a conservative generation allowance.\n" +
			"--estimate-profile adds an approximate expected cost calibrated from\n" +
			"observed usage, without changing generation settings. No LLM calls, embeddings\n" +
			"or destination writes are performed.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return execute(cmd, args, opts)
		},
	}
	cmd.Flags().StringSliceVar(&opts.only, "only", nil, "restrict the plan to these item IDs")
	cmd.Flags().IntVar(&opts.limit, "limit", 0, "plan at most N changed items")
	cmd.Flags().StringVar(&opts.estimateProfile, "estimate-profile", "", "JSON usage profile for an additional expected cost estimate")
	cmd.Flags().BoolVar(&opts.rebuildOnSignatureChange, "rebuild-on-signature-change", false,
		"include every item in the plan when a prompt or enricher signature has changed")
	cmd.Flags().BoolVar(&opts.cachedFragmentsOnly, "cached-fragments-only", false,
		"require matching cached file summaries; fail if any selected fragment needs generation")
	cmd.Flags().BoolVar(&opts.indexedOnly, "indexed-only", false,
		"restrict the plan to previously indexed items; intersect --only before --limit")
	cmd.Flags().StringSliceVar(&opts.cachedFragmentSignatures, "cached-fragment-signatures", nil,
		"ordered historical fragment signatures allowed with --cached-fragments-only; prefer the current signature")
	return cmd
}
