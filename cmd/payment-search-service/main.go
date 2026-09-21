package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	platformauth "github.com/praedyth/payment-platform/internal/auth"
	"github.com/praedyth/payment-platform/internal/config"
	searchprojection "github.com/praedyth/payment-platform/internal/search"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/telemetry"
)

func main() {
	logger := logging.New("payment-search-service")
	if err := execute(); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func execute() error {
	baseConfig, err := config.Load("payment-search-service")
	if err != nil {
		return err
	}
	searchConfig := searchprojection.LoadServiceConfig()
	ctx, cancel := run.Context()
	defer cancel()
	authenticator, err := platformauth.New(ctx, platformauth.Config{
		Disabled:     baseConfig.AuthDisabled,
		IssuerURL:    baseConfig.AuthIssuerURL,
		Audience:     baseConfig.AuthAudience,
		JWKSURL:      baseConfig.AuthJWKSURL,
		RequiredRole: baseConfig.AuthRequiredRole,
		RoleClientID: baseConfig.AuthRoleClientID,
	})
	if err != nil {
		return fmt.Errorf("configure search authentication: %w", err)
	}
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
	handler := searchprojection.NewHTTPHandler(client)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, request *http.Request) {
		readyCtx, readyCancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer readyCancel()
		if err := client.Ping(readyCtx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	mux.Handle("GET /api/v1/payment-search", authenticatedSearch(authenticator, handler))
	mux.Handle("GET /v1/payments/search", authenticatedSearch(authenticator, handler))
	server := &http.Server{
		Addr: ":" + baseConfig.HTTPPort, Handler: otelhttp.NewHandler(mux, "payment-search-http"),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	errorsCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsCh <- err
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-errorsCh:
		return err
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), baseConfig.ShutdownTimeout)
	defer shutdownCancel()
	return server.Shutdown(shutdownCtx)
}

func authenticatedSearch(authenticator *platformauth.Verifier, handler *searchprojection.HTTPHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if authenticator.Disabled() {
			userID, err := uuid.Parse(strings.TrimSpace(request.Header.Get("X-User-ID")))
			if err != nil || userID == uuid.Nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "X-User-ID must be a UUID while AUTH_DISABLED=true"})
				return
			}
			handler.SearchForUser(w, request, userID)
			return
		}

		parts := strings.Fields(request.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="payment-search"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Bearer access token is required"})
			return
		}
		principal, err := authenticator.Authenticate(request.Context(), parts[1])
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, platformauth.ErrForbidden) {
				status = http.StatusForbidden
			}
			writeJSON(w, status, map[string]string{"error": http.StatusText(status)})
			return
		}
		if principal.HasRole("payment-admin") {
			handler.Search(w, request)
			return
		}
		handler.SearchForUser(w, request, principal.UserID)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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
