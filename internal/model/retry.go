// HTTP retry logic for OpenAI-compatible API calls.
//
// Implements exponential backoff with full jitter on 429 (rate limit) and 5xx (server
// error) responses, honouring Retry-After headers. This is the single retry path shared
// by both generator and embedder.
package model

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	maxRetries    = 5
	baseBackoff   = 500 * time.Millisecond
	maxBackoff    = 30 * time.Second
	backoffFactor = 2.0
)

// doWithRetry executes an HTTP POST with exponential backoff on 429 and 5xx. It honours
// Retry-After headers when present.
func doWithRetry(ctx context.Context, client *http.Client, url, apiKey string, body []byte) (io.ReadCloser, error) {
	var lastErr error
	for attempt := range maxRetries {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("model: building request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("model: request to %s: %w", url, err)
			if ctx.Err() != nil {
				return nil, lastErr
			}
			sleep(ctx, backoffDuration(attempt, 0))
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp.Body, nil
		}

		// Read error body for context, then close.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()

		retryable := resp.StatusCode == 429 || resp.StatusCode >= 500
		if !retryable || attempt == maxRetries-1 {
			return nil, fmt.Errorf("model: %s returned %d: %s", url, resp.StatusCode, string(errBody))
		}

		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		lastErr = fmt.Errorf("model: %s returned %d (attempt %d)", url, resp.StatusCode, attempt+1)
		sleep(ctx, backoffDuration(attempt, retryAfter))
	}
	return nil, lastErr
}

// backoffDuration computes the wait time with exponential increase and full jitter,
// respecting a server-specified minimum.
func backoffDuration(attempt int, retryAfter time.Duration) time.Duration {
	exp := time.Duration(float64(baseBackoff) * math.Pow(backoffFactor, float64(attempt)))
	if exp > maxBackoff {
		exp = maxBackoff
	}
	// Full jitter: uniform [0, exp).
	jittered := time.Duration(rand.Int64N(int64(exp)))
	if jittered < retryAfter {
		return retryAfter
	}
	return jittered
}

// parseRetryAfter interprets the Retry-After header as either seconds or an HTTP-date.
func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(val); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

// sleep waits for d or until ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
