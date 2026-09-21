package servicehttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type ReadyFunc func(context.Context) error

func New(address string, registry *prometheus.Registry, ready ReadyFunc) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready != nil {
			if err := ready(ctx); err != nil {
				write(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
				return
			}
		}
		write(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	if registry != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	}
	return &http.Server{
		Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
}

func Serve(server *http.Server, errorsCh chan<- error) {
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errorsCh <- err
	}
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
