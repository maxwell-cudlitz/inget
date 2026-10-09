// Validate explicit historical cache reuse before opening configuration or dependencies.
package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// validateCachedFragmentOptions keeps malformed CLI requests from opening state or sinks.
func validateCachedFragmentOptions(cmd *cobra.Command, opts runOptions) error {
	if cmd.Flags().Changed("cached-fragment-signatures") && len(opts.cachedFragmentSignatures) == 0 {
		return fmt.Errorf("--cached-fragment-signatures requires at least one nonempty signature")
	}
	if len(opts.cachedFragmentSignatures) > 0 && !opts.cachedFragmentsOnly {
		return fmt.Errorf("--cached-fragment-signatures requires --cached-fragments-only")
	}
	for _, sig := range opts.cachedFragmentSignatures {
		if strings.TrimSpace(sig) == "" {
			return fmt.Errorf("--cached-fragment-signatures contains an empty signature")
		}
	}
	return nil
}
