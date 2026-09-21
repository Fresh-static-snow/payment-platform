package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/praedyth/payment-platform/internal/analytics"
	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/pkg/kafkax"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/servicehttp"
	"github.com/praedyth/payment-platform/pkg/telemetry"
)

var lifecycleTopics = []string{
	events.PaymentCreated,
	events.PaymentProcessing,
	events.PaymentCompleted,
	events.PaymentFailed,
	events.PaymentCancelled,
}

func main() {
	logger := logging.New("analytics-ingestor")
	if err := execute(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func execute(logger *slog.Logger) error {
	baseConfig, err := config.Load("analytics-ingestor")
	if err != nil {
		return err
	}
	analyticsConfig := analytics.LoadServiceConfig()
	ctx, cancel := run.Context()
	defer cancel()
	shutdownTelemetry, err := telemetry.Init(ctx, baseConfig.ServiceName, baseConfig.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()
	client, err := analytics.NewClient(analytics.ClientConfig{
		URL: analyticsConfig.URL, Database: analyticsConfig.Database, Table: analyticsConfig.Table,
		Username: analyticsConfig.Username, Password: analyticsConfig.Password,
	})
	if err != nil {
		return err
	}
	if err := waitForClickHouse(ctx, client, 45*time.Second); err != nil {
		return err
	}
	metrics := platformmetrics.New()
	healthServer := servicehttp.New(":"+baseConfig.HTTPPort, metrics.Registry, client.Ping)
	healthErrors := make(chan error, 1)
	go servicehttp.Serve(healthServer, healthErrors)
	ingestor := analytics.NewIngestor(client)
	consumerErrors := make(chan error, len(lifecycleTopics))
	for _, topic := range lifecycleTopics {
		topic := topic
		go func() {
			groupID := "analytics-ingestor-v1-" + strings.ReplaceAll(topic, ".", "-")
			consumerErrors <- kafkax.RunConsumerGroup(ctx, kafkax.ConsumerConfig{
				Brokers: baseConfig.KafkaBrokers, Topic: topic, GroupID: groupID,
				Workers: analyticsConfig.Workers, MaxAttempts: 5, Logger: logger, Handler: ingestor.Handle, Metrics: metrics,
				Dialer: kafkax.NewDialer(baseConfig.KafkaTLSEnabled),
			})
		}()
	}
	logger.Info("analytics ingestor started", "topics", lifecycleTopics, "database", analyticsConfig.Database, "table", analyticsConfig.Table)
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-healthErrors:
		runErr = fmt.Errorf("health server: %w", err)
		cancel()
	case err := <-consumerErrors:
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("analytics consumer: %w", err)
		}
		cancel()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), baseConfig.ShutdownTimeout)
	defer shutdownCancel()
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, err)
	}
	return runErr
}

type clickHouseReadiness interface {
	Ping(context.Context) error
}

func waitForClickHouse(ctx context.Context, client clickHouseReadiness, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := client.Ping(waitCtx); err == nil {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait for ClickHouse: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
