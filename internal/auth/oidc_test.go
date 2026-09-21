package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVerifierAuthenticatesKeycloakClaims(t *testing.T) {
	issuer, jwksURL, privateKey := newOIDCTestServer(t)
	verifier, err := New(context.Background(), Config{
		IssuerURL: issuer, Audience: "payment-api", JWKSURL: jwksURL,
		RequiredRole: "payment-user", RoleClientID: "payment-api",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	subject := "d8ec4b0a-7714-4a06-ac12-f8e3566484c9"
	token := signTestToken(t, privateKey, map[string]any{
		"iss": issuer, "aud": "payment-api", "sub": subject,
		"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"realm_access": map[string]any{"roles": []string{"payment-user", "merchant"}},
		"resource_access": map[string]any{
			"payment-api": map[string]any{"roles": []string{"merchant", "payments:read"}},
		},
	})

	principal, err := verifier.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.UserID.String() != subject || principal.Subject != subject {
		t.Fatalf("principal subject = %#v", principal)
	}
	wantRoles := "merchant,payment-user,payments:read"
	if got := strings.Join(principal.Roles, ","); got != wantRoles {
		t.Fatalf("roles = %q, want %q", got, wantRoles)
	}
}

func TestVerifierRejectsInvalidIdentityAndAuthorization(t *testing.T) {
	issuer, jwksURL, privateKey := newOIDCTestServer(t)
	verifier, err := New(context.Background(), Config{
		IssuerURL: issuer, Audience: "payment-api", JWKSURL: jwksURL,
		RequiredRole: "payment-user",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	baseClaims := func() map[string]any {
		return map[string]any{
			"iss": issuer, "aud": "payment-api",
			"sub": "3acee4e9-d2bb-468b-b7aa-260a3d2fb787",
			"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix(),
			"realm_access": map[string]any{"roles": []string{"payment-user"}},
		}
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{name: "wrong audience", mutate: func(c map[string]any) { c["aud"] = "another-api" }, want: ErrUnauthenticated},
		{name: "expired", mutate: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, want: ErrUnauthenticated},
		{name: "non UUID subject", mutate: func(c map[string]any) { c["sub"] = "alice" }, want: ErrUnauthenticated},
		{name: "required role absent", mutate: func(c map[string]any) {
			c["realm_access"] = map[string]any{"roles": []string{"merchant"}}
		}, want: ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := baseClaims()
			tc.mutate(claims)
			_, err := verifier.Authenticate(context.Background(), signTestToken(t, privateKey, claims))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Authenticate() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewUsesOIDCDiscoveryAndValidatesConfiguration(t *testing.T) {
	issuer, _, _ := newOIDCTestServer(t)
	if _, err := New(context.Background(), Config{IssuerURL: issuer, Audience: "payment-api"}); err != nil {
		t.Fatalf("New() with discovery error = %v", err)
	}
	if _, err := New(context.Background(), Config{Audience: "payment-api"}); err == nil {
		t.Fatal("New() without issuer returned nil error")
	}
	if _, err := New(context.Background(), Config{IssuerURL: issuer}); err == nil {
		t.Fatal("New() without audience returned nil error")
	}
	disabled, err := New(context.Background(), Config{Disabled: true})
	if err != nil || !disabled.Disabled() {
		t.Fatalf("disabled verifier = %#v, error = %v", disabled, err)
	}
}

func newOIDCTestServer(t *testing.T) (issuer, jwksURL string, privateKey *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	var serverURL string
	handler := http.NewServeMux()
	handler.HandleFunc("/realms/payments/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                serverURL + "/realms/payments",
			"jwks_uri":                              serverURL + "/jwks",
			"authorization_endpoint":                serverURL + "/authorize",
			"token_endpoint":                        serverURL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	handler.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		exponent := big.NewInt(int64(privateKey.PublicKey.E)).Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": "test-key", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(exponent),
		}}})
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	serverURL = server.URL
	return server.URL + "/realms/payments", server.URL + "/jwks", privateKey
}

func signTestToken(t *testing.T, privateKey *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := crypto.SHA256.New()
	_, _ = digest.Write([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest.Sum(nil))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return fmt.Sprintf("%s.%s", encoded, base64.RawURLEncoding.EncodeToString(signature))
}
