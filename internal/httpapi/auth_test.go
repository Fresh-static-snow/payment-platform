package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	platformauth "github.com/praedyth/payment-platform/internal/auth"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
)

type fakeAuthenticator struct {
	disabled  bool
	principal platformauth.Principal
	err       error
}

func (f fakeAuthenticator) Disabled() bool { return f.disabled }

func (f fakeAuthenticator) Authenticate(context.Context, string) (platformauth.Principal, error) {
	return f.principal, f.err
}

func TestAuthenticationErrorsAreStructured(t *testing.T) {
	tests := []struct {
		name          string
		authenticator Authenticator
		header        string
		wantStatus    int
		wantCode      string
	}{
		{name: "missing bearer token", authenticator: fakeAuthenticator{}, wantStatus: http.StatusUnauthorized, wantCode: "UNAUTHENTICATED"},
		{name: "invalid bearer token", authenticator: fakeAuthenticator{err: platformauth.ErrUnauthenticated}, header: "Bearer invalid", wantStatus: http.StatusUnauthorized, wantCode: "UNAUTHENTICATED"},
		{name: "required role missing", authenticator: fakeAuthenticator{err: platformauth.ErrForbidden}, header: "Bearer valid", wantStatus: http.StatusForbidden, wantCode: "FORBIDDEN"},
		{name: "invalid development identity", authenticator: fakeAuthenticator{disabled: true}, wantStatus: http.StatusUnauthorized, wantCode: "UNAUTHENTICATED"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &API{
				authenticator: tc.authenticator,
				logger:        logging.New("auth-test"), metrics: platformmetrics.New(),
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/payments", nil)
			request.Header.Set("X-Request-ID", "request-123")
			if tc.header != "" {
				request.Header.Set("Authorization", tc.header)
			}
			response := httptest.NewRecorder()
			api.Handler().ServeHTTP(response, request)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tc.wantStatus, response.Body.String())
			}
			var body struct {
				Error struct {
					Code      string `json:"code"`
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Code != tc.wantCode || body.Error.RequestID != "request-123" {
				t.Fatalf("error = %#v", body.Error)
			}
		})
	}
}

func TestAuthenticationSkipsOperationalEndpoints(t *testing.T) {
	api := &API{
		authenticator: fakeAuthenticator{err: errors.New("must not be called")},
		logger:        logging.New("auth-test"), metrics: platformmetrics.New(),
	}
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.Code)
	}
}

func TestAuthenticateAddsPrincipalToContext(t *testing.T) {
	userID := uuid.MustParse("507c0651-a2b2-45b6-b699-61c03b93d52d")
	api := &API{authenticator: fakeAuthenticator{principal: platformauth.Principal{UserID: userID, Subject: userID.String()}}}
	handler := api.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := platformauth.PrincipalFromContext(r.Context())
		if !ok || principal.UserID != userID {
			t.Fatalf("principal = %#v, ok = %t", principal, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/payments", nil)
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("X-User-ID", uuid.NewString())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}

func TestBearerTokenRejectsAmbiguousHeaders(t *testing.T) {
	tests := []struct {
		values []string
		ok     bool
	}{
		{values: []string{"Bearer token"}, ok: true},
		{values: []string{"bearer token"}, ok: true},
		{values: nil},
		{values: []string{"Basic token"}},
		{values: []string{"Bearer"}},
		{values: []string{"Bearer one", "Bearer two"}},
	}
	for _, tc := range tests {
		_, ok := bearerToken(tc.values)
		if ok != tc.ok {
			t.Fatalf("bearerToken(%q) ok = %t, want %t", tc.values, ok, tc.ok)
		}
	}
}
