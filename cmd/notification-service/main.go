package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/internal/notification"
	"github.com/praedyth/payment-platform/pkg/awsx"
	"github.com/praedyth/payment-platform/pkg/database"
	"github.com/praedyth/payment-platform/pkg/kafkax"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/servicehttp"
	"github.com/praedyth/payment-platform/pkg/telemetry"
)

func main() {
	logger := logging.New("notification-service")
	if err := execute(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func execute(logger *slog.Logger) error {
	cfg, err := config.Load("notification-service")
	if err != nil {
		return err
	}
	ctx, stop := run.Context()
	defer stop()
	shutdownTelemetry, err := telemetry.Init(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = shutdownTelemetry(shutdownCtx)
	}()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	awsClients, err := awsx.New(ctx, cfg.AWSEndpoint, cfg.AWSRegion, cfg.AWSAccessKey, cfg.AWSSecretKey)
	if err != nil {
		return err
	}
	service := notification.NewService(pool, awsClients.SNS, awsClients.SQS, cfg.SNSTopicARN, cfg.SQSQueueURL, logger)
	metrics := platformmetrics.New()
	healthServer := servicehttp.New(":"+cfg.HTTPPort, metrics.Registry, pool.Ping)
	healthErrors := make(chan error, 1)
	go servicehttp.Serve(healthServer, healthErrors)
	errorsCh := make(chan error, 3)
	go func() { errorsCh <- service.RunPublisher(ctx) }()
	go func() { errorsCh <- service.RunSQSConsumer(ctx) }()
	go func() {
		errorsCh <- kafkax.RunConsumerGroup(ctx, kafkax.ConsumerConfig{
			Brokers: cfg.KafkaBrokers, Topic: events.PaymentCompleted, GroupID: "notification-service-v1",
			Workers: cfg.WorkerCount, MaxAttempts: 4, Logger: logger, Handler: service.HandlePaymentCompleted, Metrics: metrics,
			Dialer: kafkax.NewDialer(cfg.KafkaTLSEnabled),
		})
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return healthServer.Shutdown(shutdownCtx)
	case err := <-healthErrors:
		return fmt.Errorf("health server: %w", err)
	case err := <-errorsCh:
		return err
	}
}
