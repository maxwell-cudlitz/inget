// Package ratelimit paces outbound requests to a rate-limited third-party API.
//
// Two mechanisms, because real APIs need both. A proactive token bucket keeps a client
// under a configured request rate without ever being told to slow down. A reactive pause
// responds to what the server actually said — a quota counter at zero, or an explicit
// Retry-After. Neither is sufficient alone: a bucket tuned to the average limit still
// exhausts a burst quota, and reacting only to a 429 means the 429 already happened.
//
// Per-API header vocabularies live in their own files, so a connector holds a Limiter and
// never parses a rate-limit header itself. github.go is here; Monday's complexity limiter
// arrives with that connector.
//
//	ratelimit.go  the Limiter interface and the header parsers every API shares
//	github.go     GitHub's primary and secondary limits
package ratelimit

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// Limiter paces requests. Wait blocks until the next request may be sent; Observe feeds a
// response's rate-limit headers back so the limiter can slow down before the API forces
// it to. Implementations are safe for concurrent use.
type Limiter interface {
	Wait(ctx context.Context) error
	Observe(resp *http.Response)
}

// Unlimited never waits. It is what a test uses, and what an endpoint with no published
// limit gets, so no caller needs a nil check.
type Unlimited struct{}

// Wait returns immediately.
func (Unlimited) Wait(context.Context) error { return nil }

// Observe ignores the response.
func (Unlimited) Observe(*http.Response) {}

// RetryAfter reads the Retry-After header, which RFC 9110 permits as either a delay in
// seconds or an HTTP date. A negative or unparseable value reports false rather than
// zero, so a caller can tell "wait no time" from "the server did not say".
func RetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	raw := h.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	if delay := when.Sub(now); delay > 0 {
		return delay, true
	}
	return 0, true
}

// UnixReset reads a header holding a Unix-second reset time and returns how long from now
// that is. GitHub's x-ratelimit-reset is the case this exists for.
func UnixReset(h http.Header, name string, now time.Time) (time.Duration, bool) {
	raw := h.Get(name)
	if raw == "" {
		return 0, false
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	if delay := time.Unix(seconds, 0).Sub(now); delay > 0 {
		return delay, true
	}
	// A reset already in the past is still an answer: the window has rolled over, so
	// there is nothing to wait for.
	return 0, true
}

// intHeader reads a non-negative integer header, reporting false when it is absent or not
// a number.
func intHeader(h http.Header, name string) (int, bool) {
	raw := h.Get(name)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return value, true
}
