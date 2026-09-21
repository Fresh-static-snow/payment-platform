package search

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type stubSearcher struct {
	query Query
	err   error
}

func (s *stubSearcher) Search(_ context.Context, query Query) (Result, error) {
	s.query = query
	return Result{Items: []PaymentDocument{}, Total: 0}, s.err
}

func TestHTTPHandlerMapsSearchFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid query", err: ErrInvalidQuery, want: http.StatusBadRequest},
		{name: "backend failure", err: errors.New("connection refused"), want: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			NewHTTPHandler(&stubSearcher{err: test.err}).Search(response, httptest.NewRequest(http.MethodGet, "/api/v1/payment-search", nil))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestHTTPHandlerParsesSearchFilters(t *testing.T) {
	t.Parallel()
	searcher := &stubSearcher{}
	handler := NewHTTPHandler(searcher)
	userID := uuid.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/payment-search?user_id="+userID.String()+"&status=completed,failed&currency=usd&amount_min=100&amount_max=500&limit=25", nil)
	response := httptest.NewRecorder()
	handler.Search(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if searcher.query.UserID == nil || *searcher.query.UserID != userID || searcher.query.Limit != 25 || searcher.query.Currency != "USD" {
		t.Fatalf("unexpected parsed query: %+v", searcher.query)
	}
	if len(searcher.query.Statuses) != 2 || *searcher.query.AmountMin != 100 || *searcher.query.AmountMax != 500 {
		t.Fatalf("unexpected filters: %+v", searcher.query)
	}
}

func TestHTTPHandlerRejectsMalformedQuery(t *testing.T) {
	t.Parallel()
	handler := NewHTTPHandler(&stubSearcher{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/payment-search?status=unknown", nil)
	response := httptest.NewRecorder()
	handler.Search(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestHTTPHandlerForcesAuthenticatedUserScope(t *testing.T) {
	t.Parallel()
	searcher := &stubSearcher{}
	handler := NewHTTPHandler(searcher)
	authenticatedUser := uuid.New()
	requestedUser := uuid.New()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/payment-search?user_id="+requestedUser.String(),
		nil,
	)
	response := httptest.NewRecorder()

	handler.SearchForUser(response, request, authenticatedUser)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if searcher.query.UserID == nil || *searcher.query.UserID != authenticatedUser {
		t.Fatalf("user scope = %v, want %s", searcher.query.UserID, authenticatedUser)
	}
}
