// Tests for the shared command scaffolding: the bare root shows help without error,
// version reports build metadata on stdout, and unknown commands fail.
package cli

import (
	"bytes"
	"strings"
	"testing"
)

// run executes the root command for a test app with args, returning combined output.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRoot(App{Name: "inget", Short: "test binary"})
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func TestRootShowsHelp(t *testing.T) {
	out, err := run(t)
	if err != nil {
		t.Fatalf("bare root returned error: %v", err)
	}
	if !strings.Contains(out, "test binary") {
		t.Errorf("help output missing the short description: %q", out)
	}
}

func TestVersionCommand(t *testing.T) {
	out, err := run(t, "version")
	if err != nil {
		t.Fatalf("version returned error: %v", err)
	}
	if !strings.HasPrefix(out, "inget ") {
		t.Errorf("version output = %q, want it to start with the binary name", out)
	}
	for _, want := range []string{"commit", "built", "go1."} {
		if !strings.Contains(out, want) {
			t.Errorf("version output %q is missing %q", out, want)
		}
	}
}

func TestVersionRejectsArgs(t *testing.T) {
	if _, err := run(t, "version", "extra"); err == nil {
		t.Error("version with a positional argument = nil error, want failure")
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if _, err := run(t, "nope"); err == nil {
		t.Error("unknown command = nil error, want failure")
	}
}

func TestBuildFillsEveryField(t *testing.T) {
	got := Build()
	if got.Version == "" || got.Commit == "" || got.Date == "" || got.Go == "" {
		t.Errorf("Build() = %+v, want no empty fields", got)
	}
}

func TestBuildPrefersInjectedValues(t *testing.T) {
	// Restore the package-level stamps so this test does not leak into the others.
	origVersion, origCommit, origDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = origVersion, origCommit, origDate })

	Version, Commit, Date = "v1.2.3", "abc1234", "2026-01-01T00:00:00Z"

	if got := Build().String(); !strings.HasPrefix(got, "v1.2.3 (commit abc1234, built 2026-01-01T00:00:00Z, go") {
		t.Errorf("Build().String() = %q, want the injected stamps", got)
	}
}
