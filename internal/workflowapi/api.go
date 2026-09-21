package workflowapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	platformauth "github.com/praedyth/payment-platform/internal/auth"
	"github.com/praedyth/payment-platform/internal/orchestration"
	"github.com/praedyth/payment-platform/internal/reconciliation"
	refunddomain "github.com/praedyth/payment-platform/internal/refund/domain"
	refundrepository "github.com/praedyth/payment-platform/internal/refund/repository"
	"github.com/praedyth/payment-platform/internal/requestctx"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
)

const maxBody = 1 << 20

type API struct {
	refunds        *refundrepository.Store
	reconciliation *reconciliation.Repository
	starter        *orchestration.Starter
	db             *pgxpool.Pool
	authenticator  Authenticator
	metrics        *platformmetrics.Metrics
	logger         *slog.Logger
}

type Authenticator interface {
	Disabled() bool
	Authenticate(context.Context, string) (platformauth.Principal, error)
}

func New(
	refunds *refundrepository.Store,
	reconciliationRepository *reconciliation.Repository,
	starter *orchestration.Starter,
	db *pgxpool.Pool,
	authenticator Authenticator,
	metrics *platformmetrics.Metrics,
	logger *slog.Logger,
) *API {
	return &API{
		refunds: refunds, reconciliation: reconciliationRepository, starter: starter,
		db: db, authenticator: authenticator, metrics: metrics, logger: logger,
	}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("GET /ready", a.ready)
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /api/v1/payments/{id}/refunds", a.createRefund)
	mux.HandleFunc("GET /api/v1/refunds/{id}", a.getRefund)
	mux.HandleFunc("POST /api/v1/reconciliation/runs", a.createReconciliation)
	mux.HandleFunc("GET /api/v1/reconciliation/runs/{id}", a.getReconciliation)
	return a.recover(a.requestID(a.observe(a.authenticate(mux))))
}

func (a *API) createRefund(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.principal(w, r)
	if !ok {
		return
	}
	paymentID, err := uuid.Parse(r.PathValue("id"))
	if err != nil || paymentID == uuid.Nil {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "payment id must be a UUID")
		return
	}
	key, ok := singleHeader(r.Header.Values("Idempotency-Key"))
	if !ok {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "Idempotency-Key header is required")
		return
	}
	var body struct {
		Amount int64 `json:"amount"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", principal.UserID, paymentID, body.Amount)))
	refund, created, err := a.refunds.Create(r.Context(), refunddomain.CreateParams{
		PaymentID: paymentID, UserID: principal.UserID, Amount: body.Amount,
		IdempotencyKey: key, RequestHash: hash[:], RequestID: requestctx.RequestID(r.Context()),
	})
	if err != nil {
		a.handleRefundError(w, r, err)
		return
	}
	if err := a.starter.StartRefund(r.Context(), refund, requestctx.RequestID(r.Context())); err != nil {
		logging.WithContext(r.Context(), a.logger).Error("refund workflow start deferred", "refund_id", refund.ID, "error", err)
		// The persisted pending refund is recovered by the background dispatcher.
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	a.writeJSON(w, status, refund)
}

func (a *API) getRefund(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.principal(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil || id == uuid.Nil {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "refund id must be a UUID")
		return
	}
	refund, err := a.refunds.GetForUser(r.Context(), id, principal.UserID)
	if err != nil {
		a.handleRefundError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, refund)
}

func (a *API) createReconciliation(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.principal(w, r)
	if !ok {
		return
	}
	if !principal.HasRole("payment-admin") && !a.authenticator.Disabled() {
		a.writeError(w, r, http.StatusForbidden, "FORBIDDEN", "payment-admin role is required")
		return
	}
	run, err := a.reconciliation.CreateRun(r.Context(), principal.Subject)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "could not create reconciliation run")
		return
	}
	if err := a.starter.StartReconciliation(r.Context(), run); err != nil {
		logging.WithContext(r.Context(), a.logger).Error(
			"reconciliation workflow start deferred", "run_id", run.ID, "error", err,
		)
		// The persisted pending run is recovered by the background dispatcher.
	}
	a.writeJSON(w, http.StatusAccepted, run)
}

func (a *API) getReconciliation(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.principal(w, r)
	if !ok {
		return
	}
	if !principal.HasRole("payment-admin") && !a.authenticator.Disabled() {
		a.writeError(w, r, http.StatusForbidden, "FORBIDDEN", "payment-admin role is required")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil || id == uuid.Nil {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "run id must be a UUID")
		return
	}
	run, err := a.reconciliation.GetRun(r.Context(), id)
	if errors.Is(err, reconciliation.ErrRunNotFound) {
		a.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "reconciliation run not found")
		return
	}
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "could not load reconciliation run")
		return
	}
	issues, err := a.reconciliation.ListIssues(r.Context(), id)
	if err != nil {
		a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "could not load reconciliation issues")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"run": run, "issues": issues})
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]string{"postgres": "ok", "temporal": "ok"}
	ready := true
	if err := a.db.Ping(ctx); err != nil {
		checks["postgres"], ready = err.Error(), false
	}
	if err := a.starter.Ready(ctx); err != nil {
		checks["temporal"], ready = err.Error(), false
	}
	if !ready {
		a.writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if a.authenticator == nil {
			a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "authentication is not configured")
			return
		}
		if a.authenticator.Disabled() {
			rawUserID, ok := singleHeader(r.Header.Values("X-User-ID"))
			id, err := uuid.Parse(rawUserID)
			if !ok || err != nil || id == uuid.Nil {
				a.writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "X-User-ID must be a UUID while AUTH_DISABLED=true")
				return
			}
			principal := platformauth.Principal{UserID: id, Subject: id.String(), Roles: []string{"payment-user", "payment-admin"}}
			next.ServeHTTP(w, r.WithContext(platformauth.WithPrincipal(r.Context(), principal)))
			return
		}
		rawToken, ok := bearerToken(r.Header.Values("Authorization"))
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="workflow-service"`)
			a.writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Bearer access token is required")
			return
		}
		principal, err := a.authenticator.Authenticate(r.Context(), rawToken)
		if err != nil {
			if errors.Is(err, platformauth.ErrForbidden) {
				a.writeError(w, r, http.StatusForbidden, "FORBIDDEN", "required role is missing")
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="workflow-service", error="invalid_token"`)
			a.writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid or expired access token")
			return
		}
		next.ServeHTTP(w, r.WithContext(platformauth.WithPrincipal(r.Context(), principal)))
	})
}

func bearerToken(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func singleHeader(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, value != ""
}

func (a *API) principal(w http.ResponseWriter, r *http.Request) (platformauth.Principal, bool) {
	principal, ok := platformauth.PrincipalFromContext(r.Context())
	if !ok || principal.UserID == uuid.Nil {
		a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "authenticated identity unavailable")
		return platformauth.Principal{}, false
	}
	return principal, true
}

func (a *API) handleRefundError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, refunddomain.ErrRefundNotFound):
		a.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "refund or payment not found")
	case errors.Is(err, refunddomain.ErrIdempotencyConflict):
		a.writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error())
	case errors.Is(err, refunddomain.ErrRefundAmountExceeded), errors.Is(err, refunddomain.ErrInvalidRefund):
		a.writeError(w, r, http.StatusUnprocessableEntity, "INVALID_ARGUMENT", err.Error())
	default:
		logging.WithContext(r.Context(), a.logger).Error("refund request failed", "error", err)
		a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "internal server error")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func (a *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if id == "" || len(id) > 128 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(requestctx.WithRequestID(r.Context(), id)))
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (a *API) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		a.metrics.HTTPRequests.WithLabelValues(r.Method, normalizedRoute(r), strconv.Itoa(recorder.status)).Inc()
		a.metrics.HTTPRequestDuration.WithLabelValues(r.Method, normalizedRoute(r)).Observe(time.Since(start).Seconds())
	})
}

func normalizedRoute(r *http.Request) string {
	switch {
	case strings.HasSuffix(r.URL.Path, "/refunds"):
		return "/api/v1/payments/{id}/refunds"
	case strings.HasPrefix(r.URL.Path, "/api/v1/refunds/"):
		return "/api/v1/refunds/{id}"
	case strings.HasPrefix(r.URL.Path, "/api/v1/reconciliation/runs/"):
		return "/api/v1/reconciliation/runs/{id}"
	default:
		return r.URL.Path
	}
}

func (a *API) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("panic recovered", "panic", recovered)
				a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *API) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	a.writeJSON(w, status, map[string]any{"error": map[string]string{
		"code": code, "message": message, "request_id": requestctx.RequestID(r.Context()),
	}})
}

func (a *API) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
