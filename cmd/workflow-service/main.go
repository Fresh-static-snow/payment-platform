package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	platformauth "github.com/praedyth/payment-platform/internal/auth"
	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/orchestration"
	"github.com/praedyth/payment-platform/internal/reconciliation"
	refundrepository "github.com/praedyth/payment-platform/internal/refund/repository"
	"github.com/praedyth/payment-platform/internal/workflowapi"
	"github.com/praedyth/payment-platform/pkg/database"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	runutil "github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/telemetry"
)

const defaultDispatchInterval = 5 * time.Second

func main() {
	logger := logging.New("workflow-service")
	if err := execute(logger); err != nil {
		logger.Error("workflow service stopped", "error", err)
		os.Exit(1)
	}
}

func execute(logger *slog.Logger) error {
	cfg, err := config.Load("workflow-service")
	if err != nil {
		return fmt.Errorf("load workflow service config: %w", err)
	}
	ctx, cancel := runutil.Context()
	defer cancel()

	authenticator, err := platformauth.New(ctx, platformauth.Config{
		Disabled:     cfg.AuthDisabled,
		IssuerURL:    cfg.AuthIssuerURL,
		Audience:     cfg.AuthAudience,
		JWKSURL:      cfg.AuthJWKSURL,
		RequiredRole: cfg.AuthRequiredRole,
		RoleClientID: cfg.AuthRoleClientID,
	})
	if err != nil {
		return fmt.Errorf("configure authentication: %w", err)
	}
	if authenticator.Disabled() {
		logger.Warn("authentication is disabled; X-User-ID is trusted for local development")
	}

	shutdownTelemetry, err := telemetry.Init(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = shutdownTelemetry(shutdownCtx)
	}()

	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	temporalClient, err := client.Dial(client.Options{
		HostPort:  env("TEMPORAL_ADDRESS", client.DefaultHostPort),
		Namespace: env("TEMPORAL_NAMESPACE", client.DefaultNamespace),
	})
	if err != nil {
		return fmt.Errorf("connect to Temporal: %w", err)
	}
	defer temporalClient.Close()

	refunds := refundrepository.New(pool)
	reconciliationRepository := reconciliation.NewRepository(pool)
	activities := &orchestration.Activities{
		Refunds:        refunds,
		Reconciliation: reconciliationRepository,
		ProviderMode:   env("REFUND_PROVIDER_MODE", "success"),
		ProviderDelay:  envDuration("REFUND_PROVIDER_DELAY", 3*time.Second),
	}
	temporalWorker := worker.New(temporalClient, orchestration.TaskQueue, worker.Options{})
	orchestration.Register(temporalWorker, activities)
	if err := temporalWorker.Start(); err != nil {
		return fmt.Errorf("start Temporal worker: %w", err)
	}
	defer temporalWorker.Stop()

	starter := orchestration.NewStarter(temporalClient, refunds, reconciliationRepository)
	metrics := platformmetrics.New()
	api := workflowapi.New(
		refunds, reconciliationRepository, starter, pool, authenticator, metrics, logger,
	)
	server := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           otelhttp.NewHandler(api.Handler(), "workflow-service"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serveErrors := make(chan error, 1)
	go func() {
		logger.Info(
			"workflow service started",
			"address", server.Addr,
			"temporal_address", env("TEMPORAL_ADDRESS", client.DefaultHostPort),
			"task_queue", orchestration.TaskQueue,
		)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
		close(serveErrors)
	}()

	dispatchInterval := envDuration("WORKFLOW_DISPATCH_INTERVAL", defaultDispatchInterval)
	dispatchTicker := time.NewTicker(dispatchInterval)
	defer dispatchTicker.Stop()
	dispatchPending := func() {
		dispatchCtx, dispatchCancel := context.WithTimeout(ctx, dispatchInterval)
		defer dispatchCancel()
		dispatchErrors := make(chan error, 2)
		go func() { dispatchErrors <- starter.DispatchPendingRefunds(dispatchCtx, 100) }()
		go func() { dispatchErrors <- starter.DispatchPendingReconciliations(dispatchCtx, 100) }()
		if err := errors.Join(<-dispatchErrors, <-dispatchErrors); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("pending workflow dispatch failed", "error", err)
		}
	}
	dispatchPending()

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer shutdownCancel()
			return server.Shutdown(shutdownCtx)
		case err := <-serveErrors:
			return err
		case <-dispatchTicker.C:
			dispatchPending()
		}
	}
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(env(name, fallback.String()))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
