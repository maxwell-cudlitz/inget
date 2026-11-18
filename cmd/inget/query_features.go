// Query feature discovery is local and configuration-free. Consumers can gate
// optional CLI flags before resolving credentials, opening stores or embedding.
package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

type queryFeatureReport struct {
	SchemaVersion     int      `json:"schema_version"`
	Features          []string `json:"features"`
	MaxCandidateLimit int      `json:"max_candidate_limit"`
}

func queryFeaturesCommand() *cobra.Command {
	asJSON := false
	cmd := &cobra.Command{
		Use: "query-features", Short: "Describe optional query features without configuration or network access",
		Args: cobra.NoArgs, SilenceUsage: true,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := queryFeatureReport{SchemaVersion: 1, Features: []string{"item_grouping", "selected_views", "bounded_grouped_output"}, MaxCandidateLimit: maxQueryCandidates}
			if asJSON {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return fmt.Errorf("writing query features: %w", err)
				}
				return nil
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "query features: item_grouping selected_views bounded_grouped_output (max candidates: %d)\n", maxQueryCandidates); err != nil {
				return fmt.Errorf("writing query features: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "write the feature contract as JSON")
	return cmd
}
