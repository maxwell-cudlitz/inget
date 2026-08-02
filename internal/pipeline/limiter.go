// The bound on how many generator calls a run may have in flight at once.
//
// It is a run-scoped semaphore rather than a per-item errgroup limit because the fan-out is
// nested: the item pool spawns fragment derivations, so a limit applied at each level would
// multiply into limit² concurrent requests against a provider that was configured for
// limit. One shared permit set means models.generator.concurrency means what it says.
package pipeline

import (
	"context"
	"fmt"
)

// limiter is a counting semaphore over a buffered channel. A nil limiter is unbounded,
// which is what a passthrough datatype that never calls a generator wants.
type limiter struct {
	permits chan struct{}
}

// newLimiter returns a limiter with n permits, or nil when n <= 0.
func newLimiter(n int) *limiter {
	if n <= 0 {
		return nil
	}
	return &limiter{permits: make(chan struct{}, n)}
}

// acquire takes a permit, or returns ctx's error if the context ends while it waits.
func (l *limiter) acquire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	select {
	case l.permits <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("acquiring a generator permit: %w", ctx.Err())
	}
}

// release returns a permit. Calling it without a matching acquire is a bug that would
// let the limit drift upward, so it is always deferred immediately after acquire.
func (l *limiter) release() {
	if l == nil {
		return
	}
	<-l.permits
}
