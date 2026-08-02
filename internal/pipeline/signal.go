// Signal handling for graceful pipeline shutdown.
//
// NotifyShutdown wires OS signals (SIGTERM, SIGINT) to the shutdown channel that workers
// observe. On the first signal, new work claims stop and in-flight items drain. A second
// signal cancels the context immediately, which is the "hard" shutdown escape hatch.
package pipeline

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// NotifyShutdown returns a context and a shutdown channel. The first SIGTERM/SIGINT
// closes the channel (graceful); the second cancels the context (immediate).
func NotifyShutdown(parent context.Context) (context.Context, context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(parent)
	shutdownCh := make(chan struct{})

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		defer signal.Stop(sigCh)
		select {
		case sig := <-sigCh:
			slog.Info("received signal, starting graceful shutdown", "signal", sig)
			close(shutdownCh)
		case <-ctx.Done():
			return
		}
		// Second signal: immediate cancel.
		select {
		case sig := <-sigCh:
			slog.Warn("received second signal, forcing shutdown", "signal", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx, cancel, shutdownCh
}
