package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/events"
	searchprojection "github.com/praedyth/payment-platform/internal/search"
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
	logger := logging.New("payment-search-indexer")
	if err := execute(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func execute(logger *slog.Logger) error {
	baseConfig, err := config.Load("payment-search-indexer")
	if err != nil {
		return err
	}
	searchConfig := searchprojection.LoadServiceConfig()
	ctx, cancel := run.Context()
	defer cancel()
	shutdownTelemetry, err := telemetry.Init(ctx, baseConfig.ServiceName, baseConfig.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() { _ = shutdownTelemetry(context.Background()) }()

	client, err := searchprojection.NewClient(searchprojection.ClientConfig{
		URL: searchConfig.ElasticsearchURL, Index: searchConfig.ElasticsearchIndex, Alias: searchConfig.ElasticsearchAlias,
		Username: searchConfig.ElasticsearchUsername, Password: searchConfig.ElasticsearchPassword,
	})
	if err != nil {
		return err
	}
	if err := waitForElasticsearch(ctx, client, 45*time.Second); err != nil {
		return err
	}
	if err := client.EnsureIndex(ctx); err != nil {
		return fmt.Errorf("ensure payment search index: %w", err)
	}

	metrics := platformmetrics.New()
	healthServer := servicehttp.New(":"+baseConfig.HTTPPort, metrics.Registry, client.Ping)
	healthErrors := make(chan error, 1)
	go servicehttp.Serve(healthServer, healthErrors)

	indexer := searchprojection.NewIndexer(client)
	consumerErrors := make(chan error, len(lifecycleTopics))
	for _, topic := range lifecycleTopics {
		topic := topic
		go func() {
			groupID := "payment-search-indexer-v1-" + strings.ReplaceAll(topic, ".", "-")
			consumerErrors <- kafkax.RunConsumerGroup(ctx, kafkax.ConsumerConfig{
				Brokers: baseConfig.KafkaBrokers, Topic: topic, GroupID: groupID,
				Workers: searchConfig.Workers, MaxAttempts: 5, Logger: logger, Handler: indexer.Handle, Metrics: metrics,
				Dialer: kafkax.NewDialer(baseConfig.KafkaTLSEnabled),
			})
		}()
	}
	logger.Info("payment search indexer started", "topics", lifecycleTopics, "index", searchConfig.ElasticsearchIndex, "alias", searchConfig.ElasticsearchAlias)

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-healthErrors:
		runErr = fmt.Errorf("health server: %w", err)
		cancel()
	case err := <-consumerErrors:
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("search index consumer: %w", err)
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

type elasticsearchReadiness interface {
	Ping(context.Context) error
}

func waitForElasticsearch(ctx context.Context, client elasticsearchReadiness, timeout time.Duration) error {
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
			return fmt.Errorf("wait for Elasticsearch: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
