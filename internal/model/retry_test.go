package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The retry path is shared by both roles; these tests drive it through the generator
// because it is the cheaper of the two to set up.

// shortBackoff shrinks the backoff clock for tests that assert retry mechanics rather than
// wait durations, and restores it afterwards.
func shortBackoff(t *testing.T) {
	t.Helper()
	origBase, origMax := baseBackoff, maxBackoff
	baseBackoff, maxBackoff = time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { baseBackoff, maxBackoff = origBase, origMax })
}

func TestRetry429ThenSuccess(t *testing.T) {
	shortBackoff(t)

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			// Only the first refusal names a delay, so the test covers both the
			// server-specified floor and plain jittered backoff.
			if n == 1 {
				w.Header().Set("Retry-After", "1")
			}
			w.WriteHeader(http.StatusTooManyRequests)
			writeString(t, w, `{"error":"rate limited"}`)
			return
		}
		writeJSON(t, w, chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "ok"}}},
			Usage:   chatUsageBlock{TotalTokens: 1},
		})
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 10 * time.Second})

	start := time.Now()
	text, _, err := g.Generate(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("finished in %v; Retry-After: 1 should have been honoured", elapsed)
	}
}

func TestRetry5xxThenSuccess(t *testing.T) {
	shortBackoff(t)

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			writeString(t, w, "bad gateway")
			return
		}
		writeJSON(t, w, chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "recovered"}}},
			Usage:   chatUsageBlock{TotalTokens: 2},
		})
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 10 * time.Second})

	text, _, err := g.Generate(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if text != "recovered" {
		t.Errorf("text = %q, want %q", text, "recovered")
	}
}

func TestRetryNonRetryable4xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		writeString(t, w, `{"error":"bad request"}`)
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 5 * time.Second})

	err := generateErr(t, g, context.Background())
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should report the status, got: %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

func TestRetryExhausted(t *testing.T) {
	shortBackoff(t)

	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		writeString(t, w, `{"error":"boom"}`)
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 30 * time.Second})

	err := generateErr(t, g, context.Background())
	if !strings.Contains(err.Error(), "giving up") {
		t.Errorf("error should say it gave up, got: %v", err)
	}
	if got := attempts.Load(); got != maxAttempts {
		t.Errorf("attempts = %d, want %d", got, maxAttempts)
	}
}

// A Retry-After beyond what this client will wait fails immediately rather than parking a
// worker for the duration: an exhausted quota should be visible, not look like a hang.
func TestRetryAfterBeyondBound(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		writeString(t, w, `{"error":"daily quota exhausted"}`)
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 10 * time.Second})

	err := generateErr(t, g, context.Background())
	if !strings.Contains(err.Error(), "retry after") {
		t.Errorf("error should explain the wait, got: %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

// Cancelling mid-backoff aborts instead of issuing the next attempt.
func TestRetryContextCancelledDuringBackoff(t *testing.T) {
	var attempts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		cancel() // cancel while the client is waiting to retry
	}))
	defer srv.Close()

	g := NewGenerator(OpenAIGeneratorConfig{BaseURL: srv.URL, Model: "m", Timeout: 10 * time.Second})

	start := time.Now()
	err := generateErr(t, g, ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap context.Canceled, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %v before aborting; cancellation should be immediate", elapsed)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

// generateErr calls Generate expecting failure and returns the error.
func generateErr(t *testing.T, g Generator, ctx context.Context) error {
	t.Helper()
	_, _, err := g.Generate(ctx, "test")
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

// writeJSON encodes v to w, failing the test if the response cannot be written.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("writing response: %v", err)
	}
}

// writeString writes a literal body, used where the test needs exact JSON.
func writeString(t *testing.T, w http.ResponseWriter, s string) {
	t.Helper()
	if _, err := w.Write([]byte(s)); err != nil {
		t.Errorf("writing response: %v", err)
	}
}
