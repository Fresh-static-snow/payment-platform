package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/internal/receipt"
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
	logger := logging.New("receipt-service")
	if err := execute(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func execute(logger *slog.Logger) error {
	cfg, err := config.Load("receipt-service")
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
	metrics := platformmetrics.New()
	receiptService := receipt.NewService(pool, awsClients.S3, cfg.S3Bucket, logger)
	healthServer := servicehttp.New(":"+cfg.HTTPPort, metrics.Registry, func(readyCtx context.Context) error {
		if err := pool.Ping(readyCtx); err != nil {
			return err
		}
		_, err := awsClients.S3.HeadBucket(readyCtx, &s3.HeadBucketInput{Bucket: &cfg.S3Bucket})
		return err
	})
	healthErrors := make(chan error, 1)
	go servicehttp.Serve(healthServer, healthErrors)
	consumerErrors := make(chan error, 1)
	go func() {
		consumerErrors <- kafkax.RunConsumerGroup(ctx, kafkax.ConsumerConfig{
			Brokers: cfg.KafkaBrokers, Topic: events.ReceiptRequested, GroupID: "receipt-service-v1",
			Workers: cfg.WorkerCount, MaxAttempts: 4, Logger: logger, Handler: receiptService.Handle, Metrics: metrics,
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
	case err := <-consumerErrors:
		return err
	}
}
