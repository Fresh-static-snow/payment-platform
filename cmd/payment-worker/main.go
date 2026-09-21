package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/internal/payment/repository"
	"github.com/praedyth/payment-platform/internal/payment/worker"
	"github.com/praedyth/payment-platform/internal/provider"
	"github.com/praedyth/payment-platform/internal/resilience"
	riskgrpc "github.com/praedyth/payment-platform/internal/risk/transport/grpc"
	"github.com/praedyth/payment-platform/pkg/database"
	"github.com/praedyth/payment-platform/pkg/kafkax"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/praedyth/payment-platform/pkg/redisx"
	runutil "github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/servicehttp"
	"github.com/praedyth/payment-platform/pkg/telemetry"
)

func main() {
	logger := logging.New("payment-worker")
	if err := execute(logger); err != nil {
		logger.Error("payment worker stopped", "error", err)
		os.Exit(1)
	}
}

func execute(logger *slog.Logger) error {
	cfg, err := config.Load("payment-worker")
	if err != nil {
		return fmt.Errorf("load payment worker config: %w", err)
	}
	ctx, stop := runutil.Context()
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
	redisClient, err := redisx.OpenWithConfig(ctx, redisx.Config{
		Address: cfg.RedisAddr, Username: cfg.RedisUsername,
		Password: cfg.RedisPassword, TLSEnabled: cfg.RedisTLSEnabled,
	})
	if err != nil {
		return err
	}
	defer func() { _ = redisClient.Close() }()

	riskClient, err := riskgrpc.Dial(cfg.RiskAddress, 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("dial risk service: %w", err)
	}
	defer func() {
		if err := riskClient.Close(); err != nil {
			logger.Warn("close risk client", "error", err)
		}
	}()

	fakeProvider, err := provider.NewFakeProvider()
	if err != nil {
		return fmt.Errorf("construct fake payment provider: %w", err)
	}
	retrier, err := resilience.NewRetrier(worker.DefaultRetryPolicy())
	if err != nil {
		return fmt.Errorf("construct provider retrier: %w", err)
	}
	breaker, err := resilience.NewCircuitBreaker(worker.DefaultCircuitBreakerConfig())
	if err != nil {
		return fmt.Errorf("construct provider circuit breaker: %w", err)
	}
	metrics := platformmetrics.New()
	paymentRepository := worker.NewCacheInvalidatingRepository(repository.New(pool), redisClient)
	processor, err := worker.NewProcessor(
		paymentRepository,
		worker.NewPostgresInbox(pool),
		riskClient,
		fakeProvider,
		retrier,
		breaker,
		worker.Config{ProviderTimeout: cfg.ProviderTimeout, Logger: logger, Metrics: metrics},
	)
	if err != nil {
		return fmt.Errorf("construct payment processor: %w", err)
	}

	healthServer := servicehttp.New(":"+cfg.HTTPPort, metrics.Registry, func(readyCtx context.Context) error {
		if err := pool.Ping(readyCtx); err != nil {
			return err
		}
		if err := redisClient.Ping(readyCtx).Err(); err != nil {
			return err
		}
		return riskClient.Ready(readyCtx)
	})
	healthErrors := make(chan error, 1)
	go servicehttp.Serve(healthServer, healthErrors)
	consumerErrors := make(chan error, 1)
	go func() {
		consumerErrors <- kafkax.RunConsumerGroup(ctx, kafkax.ConsumerConfig{
			Brokers: cfg.KafkaBrokers, Topic: events.PaymentCreated, GroupID: worker.ConsumerName,
			Workers: cfg.WorkerCount, MaxAttempts: 4, Logger: logger, Handler: processor.Handle, Metrics: metrics,
			Dialer: kafkax.NewDialer(cfg.KafkaTLSEnabled),
		})
	}()

	logger.Info(
		"payment worker started",
		"topic", events.PaymentCreated,
		"consumer_group", worker.ConsumerName,
		"workers", cfg.WorkerCount,
	)
	var (
		runErr       error
		consumerDone bool
	)
	select {
	case <-ctx.Done():
	case err := <-healthErrors:
		runErr = fmt.Errorf("payment worker health server: %w", err)
		stop()
	case err := <-consumerErrors:
		consumerDone = true
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("payment worker consumer: %w", err)
		}
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if !consumerDone {
		select {
		case err := <-consumerErrors:
			if err != nil && !errors.Is(err, context.Canceled) {
				runErr = errors.Join(runErr, fmt.Errorf("stop payment worker consumer: %w", err))
			}
		case <-shutdownCtx.Done():
			runErr = errors.Join(runErr, fmt.Errorf("stop payment worker consumer: %w", shutdownCtx.Err()))
		}
	}
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("shutdown health server: %w", err))
	}
	logger.Info("payment worker stopped gracefully")
	return runErr
}
