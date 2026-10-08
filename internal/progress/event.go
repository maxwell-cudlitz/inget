// Package progress reports optional, ephemeral command progress through the context.
// Events contain identities and counters, never source content or credential values.
// Without an observer they are no-ops and do not change logs, state, or signatures.
package progress

import "context"

// Event is one lifecycle, item, stage, or counter update. Count is an increment;
// Total is the known item count, or -1 while enumeration is still incomplete.
type Event struct {
	Phase, Datatype, Kind, Item, Stage, Detail string
	Total, Count                               int
}

// Observer receives updates from concurrent workers and must be concurrency-safe.
type Observer interface {
	Observe(Event)
}

type observerKey struct{}

// WithObserver attaches an optional progress observer without changing cancellation.
func WithObserver(ctx context.Context, observer Observer) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, observerKey{}, observer)
}

// Emit sends an update only when the command has installed an observer.
func Emit(ctx context.Context, event Event) {
	if observer, ok := ctx.Value(observerKey{}).(Observer); ok {
		observer.Observe(event)
	}
}

// Close releases a display attached to a command, including its periodic refresh.
// Other observers are not owned by this package and are left alone.
func Close(ctx context.Context) {
	if display, ok := ctx.Value(observerKey{}).(*Display); ok {
		display.Close()
	}
}
