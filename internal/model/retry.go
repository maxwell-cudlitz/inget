// HTTP retry logic for OpenAI-compatible API calls.
//
// Exponential backoff with full jitter on 429 and 5xx, honouring Retry-After. This is the
// single retry path shared by both roles. Every wait is interruptible: a run cancelled by
// SIGTERM must not sit in a backoff sleep.
package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	maxAttempts   = 5
	backoffFactor = 2.0
	// maxRetryAfter bounds what a server may ask us to wait. Beyond this the request
	// fails immediately: parking a worker for an hour looks like a hang, and the operator
	// needs to see that the account is out of quota.
	maxRetryAfter = 2 * time.Minute
	// errBodyLimit caps how much of an error response is read for the error message.
	errBodyLimit = 1024
)

// Backoff bounds are variables rather than constants so tests can shrink the clock. They
// are never written outside of tests, and the tests that do run sequentially.
var (
	baseBackoff = 500 * time.Millisecond
	maxBackoff  = 30 * time.Second
)

// doWithRetry executes an HTTP POST, retrying on 429, 5xx and transport errors. On
// success it returns the response body, which the caller must close.
func doWithRetry(ctx context.Context, client *http.Client, url, apiKey string, body []byte) (io.ReadCloser, error) {
	var (
		lastErr error
		delay   time.Duration
	)

	for attempt := range maxAttempts {
		// Waiting happens before a retry rather than after a failure, so the final
		// attempt never sleeps on its way out.
		if attempt > 0 {
			slog.Warn("model: retrying request",
				"url", url, "attempt", attempt+1, "attempts", maxAttempts,
				"delay", delay.String(), "cause", lastErr.Error())
			if err := sleep(ctx, delay); err != nil {
				return nil, fmt.Errorf("model: %s: retry aborted: %w", url, errors.Join(err, lastErr))
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("model: building request for %s: %w", url, err)
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("model: request to %s: %w", url, err)
			}
			lastErr = fmt.Errorf("model: request to %s: %w", url, err)
			delay = backoffDuration(attempt, 0)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if attempt > 0 {
				slog.Debug("model: request succeeded after retry", "url", url, "attempts", attempt+1)
			}
			return resp.Body, nil
		}

		// Keep a bounded excerpt of the error body for context, then release the
		// connection before waiting.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		_ = resp.Body.Close()
		statusErr := fmt.Errorf("model: %s returned %d: %s", url, resp.StatusCode, bytes.TrimSpace(errBody))

		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return nil, statusErr
		}

		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		if retryAfter > maxRetryAfter {
			return nil, fmt.Errorf("model: %s asked to retry after %v, longer than the %v this client "+
				"will wait: %w", url, retryAfter, maxRetryAfter, statusErr)
		}

		lastErr = statusErr
		delay = backoffDuration(attempt, retryAfter)
	}

	return nil, fmt.Errorf("model: giving up on %s after %d attempts: %w", url, maxAttempts, lastErr)
}

// backoffDuration computes the wait before the next attempt: exponential growth with full
// jitter, floored at a server-specified Retry-After when one was given.
func backoffDuration(attempt int, retryAfter time.Duration) time.Duration {
	exp := min(time.Duration(float64(baseBackoff)*math.Pow(backoffFactor, float64(attempt))), maxBackoff)
	// Full jitter: uniform [0, exp).
	return max(time.Duration(rand.Int64N(int64(exp))), retryAfter)
}

// parseRetryAfter interprets the Retry-After header as either seconds or an HTTP-date.
// A past date or unparseable value yields zero, leaving plain jittered backoff.
func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// sleep waits for d, returning the context error if the wait is cut short. Returning it
// rather than swallowing it is what stops a cancelled run from issuing another attempt.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting %v: %w", d, ctx.Err())
	case <-timer.C:
		return nil
	}
}
