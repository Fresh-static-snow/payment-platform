package logging

import (
	"context"
	"log/slog"
	"os"

	"github.com/praedyth/payment-platform/internal/requestctx"
)

func New(service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).With("service", service)
}

func WithContext(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if requestID := requestctx.RequestID(ctx); requestID != "" {
		return logger.With("request_id", requestID)
	}
	return logger
}
