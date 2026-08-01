// Tests for logger construction: level parsing, environment precedence, encoding
// selection and level filtering. Output is captured through New rather than Setup so
// that the process streams and the slog default stay untouched.
package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    slog.Level
		wantErr bool
	}{
		{"empty defaults to info", "", slog.LevelInfo, false},
		{"debug", "debug", slog.LevelDebug, false},
		{"info", "info", slog.LevelInfo, false},
		{"warn", "warn", slog.LevelWarn, false},
		{"warning alias", "warning", slog.LevelWarn, false},
		{"error", "error", slog.LevelError, false},
		{"mixed case", "DeBuG", slog.LevelDebug, false},
		{"padded", "  warn  ", slog.LevelWarn, false},
		{"unknown", "trace", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevel(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseLevel(%q) = %v, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLevel(%q) returned unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestOptionsWithEnvOverridesSuppliedValues(t *testing.T) {
	base := Options{Level: "info", Format: "json", Destination: "stderr", Redact: []string{"from_config"}}
	t.Setenv(EnvLevel, "debug")
	t.Setenv(EnvFormat, "text")
	t.Setenv(EnvDestination, "stdout")
	t.Setenv(EnvRedact, "from_env, another ")

	got := base.WithEnv()

	if got.Level != "debug" || got.Format != "text" || got.Destination != "stdout" {
		t.Errorf("WithEnv() = %+v, want level=debug format=text destination=stdout", got)
	}
	want := []string{"from_config", "from_env", "another"}
	if strings.Join(got.Redact, ",") != strings.Join(want, ",") {
		t.Errorf("Redact = %v, want %v (env patterns accumulate, never replace)", got.Redact, want)
	}
}

func TestOptionsWithEnvLeavesUnsetValuesAlone(t *testing.T) {
	t.Setenv(EnvLevel, "")
	t.Setenv(EnvFormat, "")
	t.Setenv(EnvDestination, "")
	t.Setenv(EnvRedact, "")

	got := Default().WithEnv()

	if want := Default(); got.Level != want.Level || got.Format != want.Format ||
		got.Destination != want.Destination || len(got.Redact) != 0 {
		t.Errorf("WithEnv() = %+v, want the defaults unchanged (%+v)", got, want)
	}
}

func TestNewEmitsStructuredJSON(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(&buf, Default())
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}

	logger.Info("fetch complete", "items", 3)

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, buf.String())
	}
	for _, key := range []string{slog.TimeKey, slog.LevelKey, slog.MessageKey, "items"} {
		if _, ok := record[key]; !ok {
			t.Errorf("record is missing %q: %v", key, record)
		}
	}
	if record[slog.MessageKey] != "fetch complete" {
		t.Errorf("msg = %v, want %q", record[slog.MessageKey], "fetch complete")
	}
}

func TestNewTextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(&buf, Options{Format: "text"})
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}

	logger.Info("hello", "items", 3)

	if got := buf.String(); !strings.Contains(got, "msg=hello") || !strings.Contains(got, "items=3") {
		t.Errorf("text output = %q, want key=value encoding", got)
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{"bad level", Options{Level: "loud"}},
		{"bad format", Options{Format: "xml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(&bytes.Buffer{}, tt.opts); err == nil {
				t.Errorf("New(%+v) = nil error, want failure", tt.opts)
			}
		})
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger, err := New(&buf, Options{Level: "warn"})
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}

	logger.Debug("dropped")
	logger.Info("dropped")
	logger.Warn("kept")

	out := buf.String()
	if strings.Contains(out, "dropped") {
		t.Errorf("output contains records below the configured level: %q", out)
	}
	if !strings.Contains(out, "kept") {
		t.Errorf("output is missing the warn record: %q", out)
	}
}

func TestDestination(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty defaults to stderr", "", false},
		{"stderr", "stderr", false},
		{"stdout", "stdout", false},
		{"file paths are unsupported", "/var/log/inget.log", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := destination(tt.input)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Errorf("destination(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
