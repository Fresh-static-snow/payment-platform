package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/events"
	ledgerconsumer "github.com/praedyth/payment-platform/internal/ledger/consumer"
	ledgerrepository "github.com/praedyth/payment-platform/internal/ledger/repository"
	ledgerservice "github.com/praedyth/payment-platform/internal/ledger/service"
	"github.com/praedyth/payment-platform/pkg/database"
	"github.com/praedyth/payment-platform/pkg/kafkax"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	runutil "github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/servicehttp"
	"github.com/praedyth/payment-platform/pkg/telemetry"
)

const (
	paymentLedgerConsumerGroup = "ledger-service-payments-v1"
	refundLedgerConsumerGroup  = "ledger-service-refunds-v1"
)

func main() {
	if err := run(); err != nil {
		logging.New("ledger-service").Error("ledger service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load("ledger-service")
	if err != nil {
		return fmt.Errorf("load ledger service config: %w", err)
	}
	logger := logging.New(cfg.ServiceName)
	ctx, cancel := runutil.Context()
	defer cancel()
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

	repository := ledgerrepository.NewPostgres(pool)
	processor := ledgerservice.NewProcessor(repository, logger)
	kafkaDialer := kafkax.NewDialer(cfg.KafkaTLSEnabled)
	paymentReader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.KafkaBrokers,
		GroupID:        paymentLedgerConsumerGroup,
		Topic:          events.PaymentCompleted,
		MinBytes:       1,
		MaxBytes:       10e6,
		MaxWait:        500 * time.Millisecond,
		CommitInterval: 0,
		QueueCapacity:  cfg.WorkerCount,
		Dialer:         kafkaDialer,
	})
	refundReader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        cfg.KafkaBrokers,
		GroupID:        refundLedgerConsumerGroup,
		Topic:          events.RefundCompleted,
		MinBytes:       1,
		MaxBytes:       10e6,
		MaxWait:        500 * time.Millisecond,
		CommitInterval: 0,
		QueueCapacity:  cfg.WorkerCount,
		Dialer:         kafkaDialer,
	})
	paymentConsumer := ledgerconsumer.New(paymentReader, processor, logger, cfg.WorkerCount, 1)
	refundConsumer := ledgerconsumer.New(refundReader, processor, logger, cfg.WorkerCount, 1)
	defer func() {
		if err := paymentConsumer.Close(); err != nil {
			logger.Warn("close payment ledger Kafka reader", "error", err)
		}
		if err := refundConsumer.Close(); err != nil {
			logger.Warn("close refund ledger Kafka reader", "error", err)
		}
	}()
	metrics := platformmetrics.New()
	healthServer := servicehttp.New(":"+cfg.HTTPPort, metrics.Registry, pool.Ping)
	healthErrors := make(chan error, 1)
	go servicehttp.Serve(healthServer, healthErrors)
	consumerErrors := make(chan error, 2)
	go func() { consumerErrors <- paymentConsumer.Run(ctx) }()
	go func() { consumerErrors <- refundConsumer.Run(ctx) }()

	logger.Info(
		"ledger service started",
		"topics", []string{events.PaymentCompleted, events.RefundCompleted},
		"consumer_groups", []string{paymentLedgerConsumerGroup, refundLedgerConsumerGroup},
		"workers", cfg.WorkerCount,
	)
	var runErr error
	remainingConsumers := 2
	select {
	case <-ctx.Done():
	case err := <-healthErrors:
		runErr = fmt.Errorf("ledger health server: %w", err)
		cancel()
	case err := <-consumerErrors:
		remainingConsumers--
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("run ledger consumer: %w", err)
		}
		cancel()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()
	for remainingConsumers > 0 {
		select {
		case err := <-consumerErrors:
			remainingConsumers--
			if err != nil && !errors.Is(err, context.Canceled) {
				runErr = errors.Join(runErr, err)
			}
		case <-shutdownCtx.Done():
			runErr = errors.Join(runErr, shutdownCtx.Err())
			remainingConsumers = 0
		}
	}
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, err)
	}
	logger.Info("ledger service stopped gracefully")
	return runErr
}
