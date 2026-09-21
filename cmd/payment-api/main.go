package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	platformauth "github.com/praedyth/payment-platform/internal/auth"
	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/httpapi"
	"github.com/praedyth/payment-platform/internal/payment/repository"
	"github.com/praedyth/payment-platform/internal/payment/service"
	"github.com/praedyth/payment-platform/pkg/database"
	"github.com/praedyth/payment-platform/pkg/kafkax"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/praedyth/payment-platform/pkg/redisx"
	"github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	logger := logging.New("payment-api")
	if err := execute(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func execute(logger *slog.Logger) error {
	cfg, err := config.Load("payment-api")
	if err != nil {
		return err
	}
	ctx, stop := run.Context()
	defer stop()
	authenticator, err := platformauth.New(ctx, platformauth.Config{
		Disabled:     cfg.AuthDisabled,
		IssuerURL:    cfg.AuthIssuerURL,
		Audience:     cfg.AuthAudience,
		JWKSURL:      cfg.AuthJWKSURL,
		RequiredRole: cfg.AuthRequiredRole,
		RoleClientID: cfg.AuthRoleClientID,
	})
	if err != nil {
		return err
	}
	if authenticator.Disabled() {
		logger.Warn("authentication is disabled; X-User-ID is trusted for local development")
	}
	shutdownTelemetry, err := telemetry.Init(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

	metrics := platformmetrics.New()
	store := repository.New(pool)
	paymentService := service.New(store, redisClient)
	api := httpapi.New(
		paymentService, pool, redisClient, cfg.KafkaBrokers, kafkax.NewDialer(cfg.KafkaTLSEnabled), logger, metrics,
		httpapi.NewRateLimiter(redisClient, 100, time.Minute), authenticator,
		os.Getenv("CORS_ALLOWED_ORIGIN"),
	)
	server := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           otelhttp.NewHandler(api.Handler(), "payment-api"),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	serveErrors := make(chan error, 1)
	go func() {
		logger.Info("http server started", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
		close(serveErrors)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serveErrors:
		return err
	}
}
