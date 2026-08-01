// Tests for the shared command scaffolding: the bare root shows help without error,
// version reports build metadata on stdout, unknown commands fail, and the logger is
// configured from the file named by --config.
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxwellcudlitz/inget/internal/config"
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

// writeConfig writes a configuration file containing only a log block, which is all the
// root command reads.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestConfigPathPrefersFlagThenEnvironment(t *testing.T) {
	if got := ConfigPath(NewRoot(App{Name: "inget"})); got != config.DefaultPath {
		t.Errorf("ConfigPath() = %q, want the working-directory default", got)
	}

	t.Setenv(EnvConfigPath, "/etc/inget/config.yaml")
	root := NewRoot(App{Name: "inget"})
	if got := ConfigPath(root); got != "/etc/inget/config.yaml" {
		t.Errorf("ConfigPath() = %q, want the INGET_CONFIG value", got)
	}

	if err := root.PersistentFlags().Set(ConfigFlag, "./custom.yaml"); err != nil {
		t.Fatalf("setting --config: %v", err)
	}
	if got := ConfigPath(root); got != "./custom.yaml" {
		t.Errorf("ConfigPath() = %q, want the flag value", got)
	}
}

// TestLoggingComesFromConfig exercises the persistent pre-run hook through the bare root.
// The version command deliberately skips that hook so it keeps working when the
// environment is misconfigured.
func TestLoggingComesFromConfig(t *testing.T) {
	path := writeConfig(t, "log:\n  level: debug\n  format: text\n")
	if _, err := run(t, "--config", path); err != nil {
		t.Fatalf("valid log block returned error: %v", err)
	}

	bad := writeConfig(t, "log:\n  level: trace\n")
	_, err := run(t, "--config", bad)
	if err == nil {
		t.Fatal("invalid log level = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "log level") {
		t.Errorf("error %q should explain the invalid log level", err)
	}
}

func TestMissingConfigDoesNotBlockCommands(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	if _, err := run(t, "--config", missing); err != nil {
		t.Errorf("running with no config file returned error: %v", err)
	}
}
