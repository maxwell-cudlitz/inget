// GitHub's two rate limits.
//
// The primary limit is a quota per hour, reported on every response as x-ratelimit-limit,
// x-ratelimit-remaining and x-ratelimit-reset. Exhausting it returns 403 or 429 and stays
// exhausted until the reset, so the only correct response is to wait for it.
//
// The secondary limit is undocumented in its thresholds and is what a client that sends
// too much at once hits well before the quota runs out. It answers with retry-after, which
// GitHub's own guidance says to honour exactly rather than to retry through. Both arrive
// here through Observe, so a connector that never reads a header still backs off.
//
// Reference: https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Header names, lowercase as GitHub sends them. http.Header canonicalizes on lookup, so
// the case here is documentation rather than protocol.
const (
	HeaderRemaining = "x-ratelimit-remaining"
	HeaderReset     = "x-ratelimit-reset"
)

// Defaults for a GitHub limiter constructed without configured limits. Ten requests a
// second is well inside the 5000-per-hour authenticated quota over a sustained run while
// still finishing a small domain quickly.
const (
	DefaultRPS   = 10
	DefaultBurst = 4
)

// pauseWarnThreshold is how long a reactive pause has to be before it is worth a log line.
// A sub-second bucket wait is normal; a minute of waiting is something an operator
// watching a stalled run wants to see a reason for.
const pauseWarnThreshold = 5 * time.Second

// GitHub paces requests to the GitHub REST API. It is safe for concurrent use, which it
// has to be: the connector fetches repositories in parallel and they share one quota.
type GitHub struct {
	bucket *rate.Limiter

	mu    sync.Mutex
	until time.Time // reactive pause; zero when not paused

	// now is injectable so the tests can assert what was decided rather than sleep
	// through it.
	now func() time.Time
}

// NewGitHub builds a limiter allowing rps requests per second with the given burst. Zero
// or negative values fall back to the documented defaults, so a source whose limits block
// omits them still gets paced.
func NewGitHub(rps float64, burst int) *GitHub {
	if rps <= 0 {
		rps = DefaultRPS
	}
	if burst <= 0 {
		burst = DefaultBurst
	}
	return &GitHub{
		bucket: rate.NewLimiter(rate.Limit(rps), burst),
		now:    time.Now,
	}
}

// Wait blocks until the next request may be sent: first through any reactive pause the
// last response asked for, then through the token bucket.
//
// The pause is re-read after each sleep because a concurrent Observe may have extended it,
// which is exactly what happens when several in-flight requests all come back throttled.
func (g *GitHub) Wait(ctx context.Context) error {
	for {
		delay := g.remainingPause()
		if delay <= 0 {
			break
		}
		if delay >= pauseWarnThreshold {
			slog.WarnContext(ctx, "waiting out a github rate limit", "delay", delay.String())
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting out a github rate limit: %w", ctx.Err())
		case <-timer.C:
		}
	}
	if err := g.bucket.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for a github request slot: %w", err)
	}
	return nil
}

// Observe reads the rate-limit headers of a response and pauses the limiter when they say
// to. Retry-After wins over the quota counter: it is the server naming a duration, where
// the counter only says the window is empty.
func (g *GitHub) Observe(resp *http.Response) {
	if resp == nil {
		return
	}
	now := g.now()
	if delay, ok := RetryAfter(resp.Header, now); ok {
		g.pause(now.Add(delay))
		return
	}
	remaining, ok := intHeader(resp.Header, HeaderRemaining)
	if !ok || remaining > 0 {
		return
	}
	if delay, ok := UnixReset(resp.Header, HeaderReset, now); ok {
		g.pause(now.Add(delay))
	}
}

// pause extends the reactive pause to at least until. It never shortens it, so a response
// carrying no headers cannot undo a throttle another response reported.
func (g *GitHub) pause(until time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until.After(g.until) {
		g.until = until
	}
}

// remainingPause reports how much of the reactive pause is left.
func (g *GitHub) remainingPause() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.until.IsZero() {
		return 0
	}
	return g.until.Sub(g.now())
}
