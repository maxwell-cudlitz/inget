// Invalid historical cache flags fail before configuration, state, or model setup.
package main

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestHistoricalFragmentFlagsValidateBeforeDependencies(t *testing.T) {
	commands := []struct {
		name string
		new  func() *cobra.Command
	}{{"plan", planCommand}, {"run", runCommand}}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"empty list", []string{"--cached-fragment-signatures="}, "nonempty signature"},
		{"requires cache-only", []string{"--cached-fragment-signatures=old"}, "requires --cached-fragments-only"},
		{"empty member", []string{"--cached-fragments-only", "--cached-fragment-signatures=old,"}, "empty signature"},
	}
	for _, command := range commands {
		for _, tt := range tests {
			t.Run(command.name+"/"+tt.name, func(t *testing.T) {
				cmd := command.new()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(tt.args)
				err := cmd.ExecuteContext(t.Context())
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("CLI error = %v, want %q before any configuration or dependency access", err, tt.want)
				}
			})
		}
	}
}
