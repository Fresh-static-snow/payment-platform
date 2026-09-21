package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
)

// Config defines the trust boundary for access tokens accepted by the API.
// JWKSURL is optional: when omitted, the key URL is obtained through OIDC
// discovery from IssuerURL.
type Config struct {
	Disabled     bool
	IssuerURL    string
	Audience     string
	JWKSURL      string
	RequiredRole string
	RoleClientID string
}

// Principal is the authenticated identity made available to HTTP handlers.
type Principal struct {
	UserID  uuid.UUID
	Subject string
	Roles   []string
}

func (p Principal) HasRole(role string) bool {
	for _, candidate := range p.Roles {
		if candidate == role {
			return true
		}
	}
	return false
}

type tokenVerifier interface {
	Verify(context.Context, string) (*oidc.IDToken, error)
}

// Verifier validates Keycloak access tokens. It intentionally does not accept
// unsigned tokens or trust user identity headers while authentication is on.
type Verifier struct {
	disabled     bool
	verifier     tokenVerifier
	requiredRole string
	roleClientID string
}

func New(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.Disabled {
		return &Verifier{disabled: true}, nil
	}

	cfg.IssuerURL = strings.TrimRight(strings.TrimSpace(cfg.IssuerURL), "/")
	cfg.Audience = strings.TrimSpace(cfg.Audience)
	cfg.JWKSURL = strings.TrimSpace(cfg.JWKSURL)
	cfg.RequiredRole = strings.TrimSpace(cfg.RequiredRole)
	cfg.RoleClientID = strings.TrimSpace(cfg.RoleClientID)
	if cfg.IssuerURL == "" {
		return nil, errors.New("AUTH_ISSUER_URL is required when authentication is enabled")
	}
	if cfg.Audience == "" {
		return nil, errors.New("AUTH_AUDIENCE is required when authentication is enabled")
	}
	if cfg.RoleClientID == "" {
		cfg.RoleClientID = cfg.Audience
	}

	var verifier tokenVerifier
	if cfg.JWKSURL != "" {
		keySet := oidc.NewRemoteKeySet(ctx, cfg.JWKSURL)
		verifier = oidc.NewVerifier(cfg.IssuerURL, keySet, &oidc.Config{ClientID: cfg.Audience})
	} else {
		provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
		if err != nil {
			return nil, fmt.Errorf("discover OIDC provider: %w", err)
		}
		verifier = provider.Verifier(&oidc.Config{ClientID: cfg.Audience})
	}

	return &Verifier{
		verifier: verifier, requiredRole: cfg.RequiredRole, roleClientID: cfg.RoleClientID,
	}, nil
}

func (v *Verifier) Disabled() bool {
	return v != nil && v.disabled
}

func (v *Verifier) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	if v == nil || v.disabled || v.verifier == nil {
		return Principal{}, fmt.Errorf("%w: token verifier is not configured", ErrUnauthenticated)
	}

	token, err := v.verifier.Verify(ctx, strings.TrimSpace(rawToken))
	if err != nil {
		return Principal{}, fmt.Errorf("%w: verify access token: %v", ErrUnauthenticated, err)
	}
	var claims keycloakClaims
	if err := token.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: decode access token claims: %v", ErrUnauthenticated, err)
	}
	userID, err := uuid.Parse(strings.TrimSpace(token.Subject))
	if err != nil || userID == uuid.Nil {
		return Principal{}, fmt.Errorf("%w: access token subject must be a UUID", ErrUnauthenticated)
	}

	roles := claims.roles(v.roleClientID)
	principal := Principal{UserID: userID, Subject: token.Subject, Roles: roles}
	if v.requiredRole != "" && !principal.HasRole(v.requiredRole) {
		return Principal{}, fmt.Errorf("%w: required role %q is missing", ErrForbidden, v.requiredRole)
	}
	return principal, nil
}

type keycloakClaims struct {
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
	ResourceAccess map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

func (c keycloakClaims) roles(clientID string) []string {
	unique := make(map[string]struct{}, len(c.RealmAccess.Roles))
	for _, role := range c.RealmAccess.Roles {
		if role = strings.TrimSpace(role); role != "" {
			unique[role] = struct{}{}
		}
	}
	if client, ok := c.ResourceAccess[clientID]; ok {
		for _, role := range client.Roles {
			if role = strings.TrimSpace(role); role != "" {
				unique[role] = struct{}{}
			}
		}
	}
	roles := make([]string, 0, len(unique))
	for role := range unique {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}
