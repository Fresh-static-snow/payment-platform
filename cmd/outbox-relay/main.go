package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/outbox"
	"github.com/praedyth/payment-platform/pkg/database"
	"github.com/praedyth/payment-platform/pkg/kafkax"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/servicehttp"
	"github.com/praedyth/payment-platform/pkg/telemetry"
)

func main() {
	logger := logging.New("outbox-relay")
	if err := execute(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func execute(logger *slog.Logger) error {
	cfg, err := config.Load("outbox-relay")
	if err != nil {
		return err
	}
	ctx, stop := run.Context()
	defer stop()
	shutdownTelemetry, err := telemetry.Init(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	writer := kafkax.NewWriter(cfg.KafkaBrokers, kafkax.NewDialer(cfg.KafkaTLSEnabled))
	defer func() { _ = writer.Close() }()
	metrics := platformmetrics.New()
	healthServer := servicehttp.New(":"+cfg.HTTPPort, metrics.Registry, pool.Ping)
	healthErrors := make(chan error, 1)
	go servicehttp.Serve(healthServer, healthErrors)
	relayErrors := make(chan error, 1)
	go func() { relayErrors <- outbox.NewRelay(pool, writer, logger, metrics).Run(ctx) }()

	var runErr error
	relayDone := false
	select {
	case <-ctx.Done():
	case err := <-healthErrors:
		runErr = fmt.Errorf("health server: %w", err)
		stop()
	case err := <-relayErrors:
		runErr = err
		relayDone = true
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, err)
	}
	if !relayDone {
		select {
		case err := <-relayErrors:
			if err != nil {
				runErr = errors.Join(runErr, err)
			}
		case <-time.After(cfg.ShutdownTimeout):
			runErr = errors.Join(runErr, context.DeadlineExceeded)
		}
	}
	return runErr
}
