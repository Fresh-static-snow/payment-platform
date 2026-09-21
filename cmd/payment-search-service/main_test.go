package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	platformauth "github.com/praedyth/payment-platform/internal/auth"
	searchprojection "github.com/praedyth/payment-platform/internal/search"
)

type capturingSearcher struct {
	query searchprojection.Query
}

func (s *capturingSearcher) Search(_ context.Context, query searchprojection.Query) (searchprojection.Result, error) {
	s.query = query
	return searchprojection.Result{Items: []searchprojection.PaymentDocument{}}, nil
}

func TestAuthenticatedSearchScopesDevelopmentIdentity(t *testing.T) {
	t.Parallel()
	verifier, err := platformauth.New(context.Background(), platformauth.Config{Disabled: true})
	if err != nil {
		t.Fatal(err)
	}
	searcher := &capturingSearcher{}
	handler := authenticatedSearch(verifier, searchprojection.NewHTTPHandler(searcher))
	userID := uuid.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/payment-search?user_id="+uuid.NewString(), nil)
	request.Header.Set("X-User-ID", userID.String())
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if searcher.query.UserID == nil || *searcher.query.UserID != userID {
		t.Fatalf("query user = %v, want %s", searcher.query.UserID, userID)
	}
}

func TestAuthenticatedSearchRejectsMissingDevelopmentIdentity(t *testing.T) {
	t.Parallel()
	verifier, err := platformauth.New(context.Background(), platformauth.Config{Disabled: true})
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticatedSearch(verifier, searchprojection.NewHTTPHandler(&capturingSearcher{}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/payment-search", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}
