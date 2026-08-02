// The retry and classification layer under client.go.
//
// One place decides what a failed response means, so no call site has to. Retries cover the
// transient classes only: a 5xx, and a throttle the limiter has already turned into a pause.
// A 4xx that is not a throttle is returned as it stands, because retrying a 404 or a bad
// token just spends quota to fail again.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/maxwellcudlitz/inget/internal/ratelimit"
)

// errNotFound reports a 404. Callers branch on it: a repository named in config that no
// longer exists is a warning, not a failed run.
var errNotFound = errors.New("not found")

// do issues one request with retries, returning the response with its body unread. The
// caller closes the body.
//
// timeout is applied per attempt through the context rather than through the client, so a
// stalled tarball fails its own attempt instead of the whole run.
func (c *client) do(ctx context.Context, ref, accept string, timeout time.Duration) (*http.Response, error) {
	target, err := c.resolve(ref)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, again, err := c.attempt(ctx, target, accept, timeout)
		switch {
		case err == nil:
			return resp, nil
		case !again:
			return nil, err
		}
		lastErr = err
		if attempt == maxAttempts {
			break
		}
		slog.WarnContext(ctx, "retrying github request", "url", target, "attempt", attempt, "error", err.Error())
		if err := sleep(ctx, backoffBase<<(attempt-1)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("github request %s failed after %d attempts: %w", target, maxAttempts, lastErr)
}

// attempt issues one request and classifies the outcome. The boolean reports whether the
// error is worth another attempt.
func (c *client) attempt(ctx context.Context, target, accept string, timeout time.Duration) (*http.Response, bool, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, false, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		cancel()
		return nil, false, fmt.Errorf("building request for %s: %w", target, err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", c.version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		// A cancelled parent context is the caller stopping, not a transient fault.
		return nil, ctx.Err() == nil, fmt.Errorf("requesting %s: %w", target, err)
	}
	c.limiter.Observe(resp)

	if resp.StatusCode == http.StatusOK {
		// The body outlives this function, so the per-attempt cancel must not fire
		// until the caller has read and closed it.
		resp.Body = deferredCloser{ReadCloser: resp.Body, after: cancel}
		return resp, false, nil
	}

	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	return nil, retryable(resp), statusError(target, resp)
}

// retryable reports whether a non-200 response is worth another attempt. A throttle is:
// the limiter has already recorded how long to wait. A server fault is. Everything else
// would fail the same way next time.
func retryable(resp *http.Response) bool {
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	// GitHub reports a secondary rate limit as 403 with retry-after; a 403 without one is
	// a permission problem and retrying it is pointless.
	_, throttled := ratelimit.RetryAfter(resp.Header, time.Now())
	return resp.StatusCode == http.StatusForbidden && throttled
}

// statusError renders a failed response, including the API's own message where it sent one.
// The body read is capped: an error path is not where an unbounded read belongs.
func statusError(target string, resp *http.Response) error {
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %w", target, errNotFound)
	}
	var payload struct {
		Message string `json:"message"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	_ = json.Unmarshal(body, &payload)
	if payload.Message != "" {
		return fmt.Errorf("%s: github returned %s: %s", target, resp.Status, payload.Message)
	}
	return fmt.Errorf("%s: github returned %s", target, resp.Status)
}

// nextLink extracts the rel="next" target from a Link header, or "" when there is none.
// GitHub paginates every collection this way, so following it is the only correct way to
// walk one; constructing page numbers by hand misses the endpoints that cap total results.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(strings.TrimSpace(part), ";")
		if len(segments) < 2 {
			continue
		}
		target := strings.TrimSpace(segments[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, param := range segments[1:] {
			if strings.EqualFold(strings.TrimSpace(param), `rel="next"`) {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}

// sleep waits for d, or returns early when the context ends.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting to retry: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// deferredCloser runs after when the body is closed, which is how a per-attempt context
// outlives its attempt without leaking: closing the body is what releases it.
type deferredCloser struct {
	io.ReadCloser
	after func()
}

// Close closes the underlying body and then releases the request context. The context is
// released even when the close failed, since leaking it would be the worse outcome.
func (d deferredCloser) Close() error {
	err := d.ReadCloser.Close()
	d.after()
	if err != nil {
		return fmt.Errorf("closing response body: %w", err)
	}
	return nil
}
