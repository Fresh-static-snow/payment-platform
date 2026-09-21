package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	riskv1 "github.com/praedyth/payment-platform/gen/risk/v1"
	"github.com/praedyth/payment-platform/internal/config"
	"github.com/praedyth/payment-platform/internal/risk/domain"
	"github.com/praedyth/payment-platform/internal/risk/repository"
	riskservice "github.com/praedyth/payment-platform/internal/risk/service"
	riskgrpc "github.com/praedyth/payment-platform/internal/risk/transport/grpc"
	"github.com/praedyth/payment-platform/pkg/database"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	runutil "github.com/praedyth/payment-platform/pkg/run"
	"github.com/praedyth/payment-platform/pkg/servicehttp"
	"github.com/praedyth/payment-platform/pkg/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	logger := logging.New("risk-service")
	if err := run(logger); err != nil {
		logger.Error("risk service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, cancel := runutil.Context()
	defer cancel()

	cfg, err := config.Load("risk-service")
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
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

	riskService, err := riskservice.New(repository.NewPostgres(pool), domain.DefaultPolicy())
	if err != nil {
		return fmt.Errorf("construct risk service: %w", err)
	}
	listener, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		return fmt.Errorf("listen on gRPC port %s: %w", cfg.GRPCPort, err)
	}
	defer listener.Close()

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(riskgrpc.RequestIDUnaryServerInterceptor()),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)
	riskv1.RegisterRiskServiceServer(grpcServer, riskgrpc.NewServer(riskService))
	healthServer := health.NewServer()
	healthv1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus(riskv1.RiskService_ServiceDesc.ServiceName, healthv1.HealthCheckResponse_SERVING)

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- grpcServer.Serve(listener)
	}()
	metrics := platformmetrics.New()
	healthHTTP := servicehttp.New(":"+cfg.HTTPPort, metrics.Registry, pool.Ping)
	healthErrors := make(chan error, 1)
	go servicehttp.Serve(healthHTTP, healthErrors)
	logger.Info("risk gRPC server started", "address", listener.Addr().String())

	var runErr error
	select {
	case serveErr := <-serveErrors:
		if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			runErr = fmt.Errorf("serve gRPC: %w", serveErr)
		}
		cancel()
	case err := <-healthErrors:
		runErr = fmt.Errorf("serve health HTTP: %w", err)
		cancel()
	case <-ctx.Done():
	}
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_NOT_SERVING)
	healthServer.SetServingStatus(riskv1.RiskService_ServiceDesc.ServiceName, healthv1.HealthCheckResponse_NOT_SERVING)
	if err := stopServer(ctx, cfg.ShutdownTimeout, grpcServer); err != nil {
		runErr = errors.Join(runErr, err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()
	if err := healthHTTP.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, err)
	}
	return runErr
}

func stopServer(parent context.Context, timeout time.Duration, server *grpc.Server) error {
	// Kept as a separate helper so GracefulStop cannot block shutdown forever.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-shutdownCtx.Done():
		server.Stop()
		return fmt.Errorf("graceful gRPC shutdown: %w", shutdownCtx.Err())
	}
}
