package workflowapi

import (
	"bytes"
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

func TestAuthenticationRejectsInvalidCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		authenticator Authenticator
		headers       http.Header
		wantStatus    int
		wantCode      string
	}{
		{name: "missing authenticator", wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL"},
		{name: "missing bearer", authenticator: fakeAuthenticator{}, wantStatus: http.StatusUnauthorized, wantCode: "UNAUTHENTICATED"},
		{
			name: "duplicate bearer", authenticator: fakeAuthenticator{},
			headers:    http.Header{"Authorization": {"Bearer first", "Bearer second"}},
			wantStatus: http.StatusUnauthorized, wantCode: "UNAUTHENTICATED",
		},
		{
			name: "forbidden", authenticator: fakeAuthenticator{err: platformauth.ErrForbidden},
			headers:    http.Header{"Authorization": {"Bearer token"}},
			wantStatus: http.StatusForbidden, wantCode: "FORBIDDEN",
		},
		{
			name: "invalid development identity", authenticator: fakeAuthenticator{disabled: true},
			wantStatus: http.StatusUnauthorized, wantCode: "UNAUTHENTICATED",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := &API{
				authenticator: test.authenticator,
				metrics:       platformmetrics.New(),
				logger:        logging.New("workflow-auth-test"),
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/refunds/"+uuid.NewString(), nil)
			request.Header = test.headers.Clone()
			response := httptest.NewRecorder()
			api.Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", body.Error.Code, test.wantCode)
			}
		})
	}
}

func TestDecodeJSONRequiresExactlyOneObject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "one object", body: `{"amount":100}`},
		{name: "two objects", body: `{"amount":100}{"amount":200}`, wantErr: true},
		{name: "trailing garbage", body: `{"amount":100} trailing`, wantErr: true},
		{name: "unknown field", body: `{"amount":100,"unexpected":true}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(test.body))
			var target struct {
				Amount int64 `json:"amount"`
			}
			err := decodeJSON(httptest.NewRecorder(), request, &target)
			if test.wantErr && err == nil {
				t.Fatal("decodeJSON() error = nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("decodeJSON() error = %v", err)
			}
		})
	}
}

func TestBearerToken(t *testing.T) {
	t.Parallel()
	if token, ok := bearerToken([]string{"bearer token"}); !ok || token != "token" {
		t.Fatalf("bearerToken() = %q, %t", token, ok)
	}
	for _, values := range [][]string{nil, {"Bearer"}, {"Basic token"}, {"Bearer one", "Bearer two"}} {
		if _, ok := bearerToken(values); ok {
			t.Fatalf("bearerToken(%q) unexpectedly accepted", values)
		}
	}
}

func TestFakeAuthenticatorContract(t *testing.T) {
	// Keep compile-time coverage of the error path used by the table above.
	var authenticator Authenticator = fakeAuthenticator{err: errors.New("rejected")}
	if _, err := authenticator.Authenticate(context.Background(), "token"); err == nil {
		t.Fatal("fake authenticator returned nil error")
	}
}
