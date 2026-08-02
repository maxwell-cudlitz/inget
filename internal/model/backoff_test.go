package model

import (
	"net/http"
	"testing"
	"time"
)

// Delay arithmetic, tested without HTTP: header parsing and the jittered backoff bounds.

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "12", 12 * time.Second},
		{"zero seconds", "0", 0},
		{"past http date", "Mon, 02 Jan 2006 15:04:05 GMT", 0},
		{"unparseable", "soon", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfter(tt.value); got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}

	// An HTTP-date in the future resolves to a positive wait.
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 30*time.Second {
		t.Errorf("parseRetryAfter(future) = %v, want a positive duration up to 30s", got)
	}
}

func TestBackoffDuration(t *testing.T) {
	// Full jitter keeps every wait under the exponential ceiling, and the ceiling itself is
	// capped so a long retry chain cannot stall a run.
	for attempt := range maxAttempts {
		got := backoffDuration(attempt, 0)
		if got < 0 || got > maxBackoff {
			t.Errorf("backoffDuration(%d, 0) = %v, want within [0, %v]", attempt, got, maxBackoff)
		}
	}

	// A server-specified wait acts as a floor.
	if got := backoffDuration(0, 5*time.Second); got < 5*time.Second {
		t.Errorf("backoffDuration(0, 5s) = %v, want at least 5s", got)
	}
}
