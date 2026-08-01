// Package logging configures the process-wide structured logger.
//
// Diagnostic output goes to stderr as JSON by default; stdout is reserved for program
// data such as query results and version strings. Nothing in this package writes to
// files or rotates logs — routing is the deployment environment's concern.
//
// Verbosity and encoding come from the configuration layer as an Options value, and
// the LOG_LEVEL, LOG_FORMAT, LOG_DESTINATION and LOG_REDACT environment variables
// override whatever the caller supplied. Every logger built here passes through a
// Redactor so that secret-bearing attributes never reach the output stream.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Environment variables recognised by Options.WithEnv. These are deliberately
// unprefixed so that operators can set verbosity without knowing the config schema.
const (
	EnvLevel       = "LOG_LEVEL"
	EnvFormat      = "LOG_FORMAT"
	EnvDestination = "LOG_DESTINATION"
	EnvRedact      = "LOG_REDACT"
)

// Options describes the desired logger. Zero values fall back to the defaults returned
// by Default, so a partially populated Options is safe to pass to New or Setup.
type Options struct {
	Level       string   // debug | info | warn | error
	Format      string   // json | text
	Destination string   // stderr | stdout
	Redact      []string // extra substrings marking an attribute key as secret
}

// Default returns the shipped defaults: info level, JSON encoding, stderr destination.
func Default() Options {
	return Options{Level: "info", Format: "json", Destination: "stderr"}
}

// WithEnv returns a copy of o with any LOG_* environment variable applied. Redaction
// patterns accumulate rather than replace, since dropping a configured pattern would
// silently widen exposure.
func (o Options) WithEnv() Options {
	if v := os.Getenv(EnvLevel); v != "" {
		o.Level = v
	}
	if v := os.Getenv(EnvFormat); v != "" {
		o.Format = v
	}
	if v := os.Getenv(EnvDestination); v != "" {
		o.Destination = v
	}
	if v := os.Getenv(EnvRedact); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				o.Redact = append(o.Redact, p)
			}
		}
	}
	return o
}

// Setup builds a logger from opts plus environment overrides, installs it as the slog
// default, and returns it. It is the entrypoint every binary calls once at startup.
func Setup(opts Options) (*slog.Logger, error) {
	opts = opts.WithEnv()
	w, err := destination(opts.Destination)
	if err != nil {
		return nil, err
	}
	logger, err := New(w, opts)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(logger)
	return logger, nil
}

// New builds a logger writing to w. It exists separately from Setup so that tests can
// capture output without touching the process streams or the slog default.
func New(w io.Writer, opts Options) (*slog.Logger, error) {
	level, err := ParseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	handlerOpts := &slog.HandlerOptions{Level: level}

	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(opts.Format)) {
	case "", "json":
		h = slog.NewJSONHandler(w, handlerOpts)
	case "text":
		h = slog.NewTextHandler(w, handlerOpts)
	default:
		return nil, fmt.Errorf("invalid log format %q: want json or text", opts.Format)
	}
	return slog.New(NewRedactor(h, opts.Redact...)), nil
}

// ParseLevel maps a configured level name onto a slog.Level. An empty name is info.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: want debug, info, warn or error", name)
	}
}

// destination resolves a configured stream name to a writer. Only the two process
// streams are permitted; file destinations are intentionally unsupported.
func destination(name string) (io.Writer, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "stderr":
		return os.Stderr, nil
	case "stdout":
		return os.Stdout, nil
	default:
		return nil, fmt.Errorf("invalid log destination %q: want stderr or stdout", name)
	}
}
