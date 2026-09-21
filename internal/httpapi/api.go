package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformauth "github.com/praedyth/payment-platform/internal/auth"
	"github.com/praedyth/payment-platform/internal/payment/domain"
	"github.com/praedyth/payment-platform/internal/payment/service"
	"github.com/praedyth/payment-platform/internal/requestctx"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
)

const maxRequestBody = 1 << 20

type API struct {
	service       *service.Service
	db            *pgxpool.Pool
	redis         *redis.Client
	kafkaBrokers  []string
	kafkaDialer   *kafka.Dialer
	logger        *slog.Logger
	metrics       *platformmetrics.Metrics
	rateLimiter   *RateLimiter
	authenticator Authenticator
	allowedOrigin string
}

type Authenticator interface {
	Authenticate(context.Context, string) (platformauth.Principal, error)
	Disabled() bool
}

func New(
	service *service.Service,
	db *pgxpool.Pool,
	redisClient *redis.Client,
	kafkaBrokers []string,
	kafkaDialer *kafka.Dialer,
	logger *slog.Logger,
	metrics *platformmetrics.Metrics,
	rateLimiter *RateLimiter,
	authenticator Authenticator,
	allowedOrigin string,
) *API {
	return &API{
		service: service, db: db, redis: redisClient, kafkaBrokers: kafkaBrokers, kafkaDialer: kafkaDialer,
		logger: logger, metrics: metrics, rateLimiter: rateLimiter,
		authenticator: authenticator, allowedOrigin: allowedOrigin,
	}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("GET /ready", a.ready)
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("POST /api/v1/payments", a.createPayment)
	mux.HandleFunc("GET /api/v1/payments", a.listPayments)
	mux.HandleFunc("GET /api/v1/payments/{id}", a.getPayment)
	mux.HandleFunc("POST /api/v1/payments/{id}/cancel", a.cancelPayment)
	mux.HandleFunc("POST /api/v1/payments/{id}/receipt", a.requestReceipt)
	return a.recover(a.security(a.requestID(a.observe(a.authenticate(a.rateLimit(mux))))))
}

type createPaymentRequest struct {
	UserID      string `json:"user_id,omitempty"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Description string `json:"description"`
}

func (a *API) createPayment(w http.ResponseWriter, r *http.Request) {
	userID, ok := a.userID(w, r)
	if !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "Idempotency-Key header is required")
		return
	}
	var body createPaymentRequest
	if err := decodeJSON(w, r, &body); err != nil {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if body.UserID != "" {
		bodyUserID, err := uuid.Parse(body.UserID)
		if err != nil || bodyUserID != userID {
			a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "body user_id must match authenticated user")
			return
		}
	}
	payment, created, err := a.service.Create(r.Context(), service.CreateInput{
		IdempotencyKey: idempotencyKey, UserID: userID, Amount: body.Amount,
		Currency: body.Currency, Description: body.Description,
		RequestID: requestctx.RequestID(r.Context()),
	})
	if err != nil {
		a.handleDomainError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		a.metrics.PaymentsCreated.Inc()
	}
	a.writeJSON(w, status, payment)
}

func (a *API) getPayment(w http.ResponseWriter, r *http.Request) {
	userID, ok := a.userID(w, r)
	if !ok {
		return
	}
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	payment, err := a.service.Get(r.Context(), id, userID)
	if err != nil {
		a.handleDomainError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, payment)
}

func (a *API) listPayments(w http.ResponseWriter, r *http.Request) {
	userID, ok := a.userID(w, r)
	if !ok {
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	items, next, err := a.service.List(r.Context(), userID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (a *API) cancelPayment(w http.ResponseWriter, r *http.Request) {
	userID, ok := a.userID(w, r)
	if !ok {
		return
	}
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	payment, err := a.service.Cancel(r.Context(), id, userID, requestctx.RequestID(r.Context()))
	if err != nil {
		a.handleDomainError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, payment)
}

func (a *API) requestReceipt(w http.ResponseWriter, r *http.Request) {
	userID, ok := a.userID(w, r)
	if !ok {
		return
	}
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	created, err := a.service.RequestReceipt(r.Context(), id, userID, requestctx.RequestID(r.Context()))
	if err != nil {
		a.handleDomainError(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]any{"payment_id": id, "accepted": created})
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]string{"postgres": "ok", "redis": "ok", "kafka": "ok"}
	ready := true
	if err := a.db.Ping(ctx); err != nil {
		checks["postgres"], ready = err.Error(), false
	}
	if err := a.redis.Ping(ctx).Err(); err != nil {
		checks["redis"], ready = err.Error(), false
	}
	if len(a.kafkaBrokers) == 0 {
		checks["kafka"], ready = "no brokers configured", false
	} else {
		dialer := a.kafkaDialer
		if dialer == nil {
			dialer = &kafka.Dialer{Timeout: 10 * time.Second, DualStack: true}
		}
		connection, err := dialer.DialContext(ctx, "tcp", a.kafkaBrokers[0])
		if err != nil {
			checks["kafka"], ready = err.Error(), false
		} else {
			_ = connection.Close()
		}
	}
	if !ready {
		a.writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "checks": checks})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
}

func (a *API) userID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	principal, ok := platformauth.PrincipalFromContext(r.Context())
	if !ok || principal.UserID == uuid.Nil {
		a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "authenticated identity is unavailable")
		return uuid.Nil, false
	}
	return principal.UserID, true
}

func (a *API) pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	value, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		a.writeError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "payment id must be a valid UUID")
		return uuid.Nil, false
	}
	return value, true
}

func (a *API) handleDomainError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrPaymentNotFound):
		a.writeError(w, r, http.StatusNotFound, "NOT_FOUND", "payment not found")
	case errors.Is(err, domain.ErrIdempotencyConflict):
		a.writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error())
	case errors.Is(err, domain.ErrConcurrentModification):
		a.writeError(w, r, http.StatusConflict, "CONCURRENT_MODIFICATION", err.Error())
	case errors.Is(err, domain.ErrInvalidPayment), errors.Is(err, domain.ErrInvalidStatusTransition):
		a.writeError(w, r, http.StatusUnprocessableEntity, "INVALID_ARGUMENT", err.Error())
	default:
		logging.WithContext(r.Context(), a.logger).Error("request failed", "error", err)
		a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "internal server error")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func (a *API) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	a.writeJSON(w, status, map[string]any{"error": map[string]string{
		"code": code, "message": message, "request_id": requestctx.RequestID(r.Context()),
	}})
}

func (a *API) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (a *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(requestctx.WithRequestID(r.Context(), requestID)))
	})
}

func (a *API) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		route := normalizedRoute(r)
		a.metrics.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(recorder.status)).Inc()
		a.metrics.HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(started).Seconds())
		logging.WithContext(r.Context(), a.logger).Info("http request", "method", r.Method, "route", route, "status", recorder.status, "duration", time.Since(started))
	})
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
			userID, err := uuid.Parse(strings.TrimSpace(r.Header.Get("X-User-ID")))
			if err != nil || userID == uuid.Nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="payment-api"`)
				a.writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "X-User-ID must be a valid UUID while AUTH_DISABLED=true")
				return
			}
			principal := platformauth.Principal{UserID: userID, Subject: userID.String()}
			next.ServeHTTP(w, r.WithContext(platformauth.WithPrincipal(r.Context(), principal)))
			return
		}

		rawToken, ok := bearerToken(r.Header.Values("Authorization"))
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="payment-api"`)
			a.writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Bearer access token is required")
			return
		}
		principal, err := a.authenticator.Authenticate(r.Context(), rawToken)
		if err != nil {
			logging.WithContext(r.Context(), a.logger).Warn("access token rejected", "error", err)
			if errors.Is(err, platformauth.ErrForbidden) {
				a.writeError(w, r, http.StatusForbidden, "FORBIDDEN", "required role is missing")
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="payment-api", error="invalid_token"`)
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

func (a *API) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			subject := ""
			if principal, ok := platformauth.PrincipalFromContext(r.Context()); ok {
				subject = principal.Subject
			}
			if subject == "" {
				subject = r.RemoteAddr
			}
			if !a.rateLimiter.Allow(r.Context(), subject, requestctx.RequestID(r.Context())) {
				a.writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "request rate limit exceeded")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if a.allowedOrigin != "" && r.Header.Get("Origin") == a.allowedOrigin {
			w.Header().Set("Access-Control-Allow-Origin", a.allowedOrigin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-User-ID, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				logging.WithContext(r.Context(), a.logger).Error("panic recovered", "panic", value)
				a.writeError(w, r, http.StatusInternalServerError, "INTERNAL", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func normalizedRoute(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/api/v1/payments/") {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			return "/api/v1/payments/{id}/cancel"
		case strings.HasSuffix(r.URL.Path, "/receipt"):
			return "/api/v1/payments/{id}/receipt"
		default:
			return "/api/v1/payments/{id}"
		}
	}
	return r.URL.Path
}
