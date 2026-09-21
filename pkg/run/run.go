package run

import (
	"context"
	"errors"
	"log/slog"
	"os/signal"
	"syscall"
	"time"
)

func Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

func UntilCancelled(ctx context.Context, logger *slog.Logger, shutdownTimeout time.Duration, shutdown func(context.Context) error) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
