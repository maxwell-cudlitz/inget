// Redaction of secret-bearing log attributes.
//
// Secrets reach this process only as environment variable values named by config, but
// they can still be attached to a log record by accident — a wrapped HTTP error
// carrying an Authorization header, or a config dump. Redactor is a slog.Handler
// middleware that replaces the value of any attribute whose key looks like a secret,
// including attributes nested inside groups and those supplied via WithAttrs.
//
// Matching is by key, never by value, and the pattern set is deliberately narrow.
// This codebase uses "token" for LLM token counts, "key" for cache and fragment keys
// and "signature" for invalidation signatures; redacting those would destroy useful
// diagnostics. So single-word matches are made against whole underscore-separated
// segments, compound matches against the full key, and any key ending in _env is
// exempt because it names an environment variable rather than holding a secret.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Placeholder replaces every redacted value.
const Placeholder = "[REDACTED]"

// secretSegments match a whole underscore-separated segment of a key. Plurals are
// omitted on purpose: "tokens" is a count in this codebase, "token" is a credential.
var secretSegments = map[string]bool{
	"auth":          true,
	"authorization": true,
	"bearer":        true,
	"cookie":        true,
	"credential":    true,
	"credentials":   true,
	"dsn":           true,
	"passphrase":    true,
	"passwd":        true,
	"password":      true,
	"pwd":           true,
	"secret":        true,
	"secrets":       true,
	"token":         true,
}

// secretPhrases match anywhere in the normalised key. These are the compounds that
// make a "key" segment sensitive without catching cache_key or related_keys.
var secretPhrases = []string{
	"access_key",
	"api_key",
	"apikey",
	"client_secret",
	"private_key",
	"secret_key",
	"session_key",
	"signing_key",
}

// IsSecretKey reports whether an attribute key should have its value redacted. extra
// patterns from configuration are matched as plain substrings of the normalised key.
func IsSecretKey(key string, extra ...string) bool {
	k := normalizeKey(key)
	if k == "" {
		return false
	}
	// A *_env key names the variable holding a secret; the name itself is not one.
	if strings.HasSuffix(k, "_env") {
		return false
	}
	for _, phrase := range secretPhrases {
		if strings.Contains(k, phrase) {
			return true
		}
	}
	for _, segment := range strings.Split(k, "_") {
		if secretSegments[segment] {
			return true
		}
	}
	for _, pattern := range extra {
		p := normalizeKey(pattern)
		if p != "" && strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// normalizeKey lowercases a key and unifies the separators seen across YAML config
// keys, HTTP header names and Go field names into underscores.
func normalizeKey(key string) string {
	k := strings.ToLower(strings.TrimSpace(key))
	return strings.NewReplacer("-", "_", ".", "_", " ", "_", "/", "_").Replace(k)
}

// Redactor wraps a slog.Handler, redacting secret-bearing attributes on the way through.
type Redactor struct {
	inner slog.Handler
	extra []string
}

// NewRedactor wraps inner. extra adds configuration-supplied key patterns to the
// built-in set.
func NewRedactor(inner slog.Handler, extra ...string) *Redactor {
	return &Redactor{inner: inner, extra: extra}
}

// Enabled reports whether the wrapped handler handles this level.
func (r *Redactor) Enabled(ctx context.Context, level slog.Level) bool {
	return r.inner.Enabled(ctx, level)
}

// Handle rebuilds the record with redacted attributes and forwards it. The message and
// level are passed through untouched; only attribute values change.
func (r *Redactor) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(r.redact(a))
		return true
	})
	if err := r.inner.Handle(ctx, out); err != nil {
		return fmt.Errorf("writing log record: %w", err)
	}
	return nil
}

// WithAttrs redacts the attributes before they are bound to the wrapped handler, so
// that pre-bound context cannot leak a secret on every subsequent record.
func (r *Redactor) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		redacted = append(redacted, r.redact(a))
	}
	return &Redactor{inner: r.inner.WithAttrs(redacted), extra: r.extra}
}

// WithGroup opens a group on the wrapped handler.
func (r *Redactor) WithGroup(name string) slog.Handler {
	return &Redactor{inner: r.inner.WithGroup(name), extra: r.extra}
}

// redact resolves an attribute and replaces its value when the key looks secret,
// recursing into groups so nested attributes are covered too.
func (r *Redactor) redact(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		out := make([]slog.Attr, 0, len(members))
		for _, m := range members {
			out = append(out, r.redact(m))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	if IsSecretKey(a.Key, r.extra...) {
		return slog.String(a.Key, Placeholder)
	}
	return a
}
