// Tests for secret redaction. The negative cases matter as much as the positive ones:
// this codebase logs token counts, cache keys and signatures, and redacting those would
// destroy the diagnostics the invalidation cascade depends on.
package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestIsSecretKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		// Credentials.
		{"token", "token", true},
		{"prefixed token", "github_token", true},
		{"api key", "api_key", true},
		{"header style api key", "X-Api-Key", true},
		{"authorization header", "Authorization", true},
		{"bearer", "bearer", true},
		{"password", "password", true},
		{"client secret", "client_secret", true},
		{"dsn", "dsn", true},
		{"nested config path", "state.dsn", true},
		{"private key", "private_key", true},
		{"cookie", "cookie", true},

		// Names of environment variables are not themselves secrets.
		{"token_env", "token_env", false},
		{"api_key_env", "api_key_env", false},
		{"dsn_env", "dsn_env", false},

		// Domain vocabulary that must survive redaction.
		{"input token count", "input_tokens", false},
		{"output token count", "output_tokens", false},
		{"cache key", "cache_key", false},
		{"fragment key", "fragment_key", false},
		{"related keys", "related_keys", false},
		{"key_from", "key_from", false},
		{"signature", "signature", false},
		{"run id", "run_id", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSecretKey(tt.key); got != tt.want {
				t.Errorf("IsSecretKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestIsSecretKeyExtraPatterns(t *testing.T) {
	if !IsSecretKey("monday_pat", "pat") {
		t.Error("IsSecretKey(monday_pat, extra=pat) = false, want true")
	}
	if IsSecretKey("monday_pat") {
		t.Error("IsSecretKey(monday_pat) = true, want false without the extra pattern")
	}
}

// logAndCapture builds a JSON logger over a buffer and hands it to fn, returning what
// was written.
func logAndCapture(t *testing.T, opts Options, fn func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	logger, err := New(&buf, opts)
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	fn(logger)
	return buf.String()
}

func TestRedactorHidesSecretValues(t *testing.T) {
	out := logAndCapture(t, Default(), func(l *slog.Logger) {
		l.Info("calling model", "api_key", "sk-live-abc123", "model", "qwen3", "input_tokens", 42)
	})

	if strings.Contains(out, "sk-live-abc123") {
		t.Errorf("secret value leaked into output: %q", out)
	}
	if !strings.Contains(out, Placeholder) {
		t.Errorf("output is missing the redaction placeholder: %q", out)
	}
	if !strings.Contains(out, "qwen3") || !strings.Contains(out, "42") {
		t.Errorf("non-secret attributes were altered: %q", out)
	}
}

func TestRedactorHandlesGroupsAndWithAttrs(t *testing.T) {
	tests := []struct {
		name string
		emit func(*slog.Logger)
	}{
		{
			name: "inline group",
			emit: func(l *slog.Logger) {
				l.Info("config", slog.Group("state", "dsn", "postgres://u:hunter2@host/db", "schema", "inget_state"))
			},
		},
		{
			name: "bound attrs",
			emit: func(l *slog.Logger) {
				l.With("dsn", "postgres://u:hunter2@host/db").Info("config", "schema", "inget_state")
			},
		},
		{
			name: "named group",
			emit: func(l *slog.Logger) {
				l.WithGroup("state").Info("config", "dsn", "postgres://u:hunter2@host/db", "schema", "inget_state")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := logAndCapture(t, Default(), tt.emit)
			if strings.Contains(out, "hunter2") {
				t.Errorf("secret leaked: %q", out)
			}
			if !strings.Contains(out, "inget_state") {
				t.Errorf("sibling attribute was lost: %q", out)
			}
		})
	}
}

func TestRedactorRespectsConfiguredPatterns(t *testing.T) {
	out := logAndCapture(t, Options{Redact: []string{"board_id"}}, func(l *slog.Logger) {
		l.Info("fetch", "board_id", 998877, "item_id", 12345)
	})

	if strings.Contains(out, "998877") {
		t.Errorf("configured pattern was not redacted: %q", out)
	}
	if !strings.Contains(out, "12345") {
		t.Errorf("unrelated attribute was redacted: %q", out)
	}
}

func TestRedactorPreservesLevelEnablement(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	r := NewRedactor(inner)

	if r.Enabled(t.Context(), slog.LevelInfo) {
		t.Error("Enabled(info) = true, want false — enablement must defer to the wrapped handler")
	}
	if !r.Enabled(t.Context(), slog.LevelError) {
		t.Error("Enabled(error) = false, want true")
	}
}
