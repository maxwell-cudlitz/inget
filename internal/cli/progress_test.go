// CLI progress tests prove stderr-only rendering, JSON-safe stdout and shared flag
// validation without contacting external services or changing the config schema.
package cli

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/maxwell-cudlitz/inget/internal/progress"
)

func TestProgressLeavesStdoutDataClean(t *testing.T) {
	prior := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prior) })
	root := NewRoot(App{Name: "test", Run: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		progress.Emit(ctx, progress.Event{Kind: "start", Phase: "fetch", Datatype: "github/repo", Total: 1})
		slog.WarnContext(ctx, "example warning", "token", "must-be-redacted")
		progress.Emit(ctx, progress.Event{Kind: "completed", Stage: "fetched", Count: 1})
		progress.Emit(ctx, progress.Event{Kind: "done", Stage: "complete"})
		_, err := fmt.Fprintln(cmd.OutOrStdout(), `{"items":1}`)
		return err
	}})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--config", writeConfig(t, "log:\n  level: warn\n"), "--progress", "plain"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "{\"items\":1}\n" {
		t.Fatalf("stdout data was polluted: %q", stdout.String())
	}
	for _, want := range []string{"[complete]", "1/1 finished", "example warning", "[REDACTED]"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q: %s", want, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "must-be-redacted") {
		t.Fatal("coordinated logging bypassed secret redaction")
	}
}

func TestInvalidProgressModeFailsBeforeCommand(t *testing.T) {
	called := false
	root := NewRoot(App{Name: "test", Run: func(_ *cobra.Command, _ []string) error {
		called = true
		return nil
	}})
	root.SetArgs([]string{"--progress", "invalid"})
	err := root.Execute()
	if called || err == nil || !strings.Contains(err.Error(), "--progress") {
		t.Fatalf("invalid progress mode called=%v err=%v", called, err)
	}
}
