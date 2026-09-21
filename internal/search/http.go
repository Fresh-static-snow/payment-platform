package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Searcher interface {
	Search(context.Context, Query) (Result, error)
}

type HTTPHandler struct {
	searcher Searcher
}

func NewHTTPHandler(searcher Searcher) *HTTPHandler { return &HTTPHandler{searcher: searcher} }

func (h *HTTPHandler) Search(w http.ResponseWriter, request *http.Request) {
	h.search(w, request, nil)
}

// SearchForUser applies an ownership boundary after parsing all public query
// parameters. A caller therefore cannot escape its OIDC subject scope by
// supplying another user_id value in the URL.
func (h *HTTPHandler) SearchForUser(w http.ResponseWriter, request *http.Request, userID uuid.UUID) {
	h.search(w, request, &userID)
}

func (h *HTTPHandler) search(w http.ResponseWriter, request *http.Request, forcedUserID *uuid.UUID) {
	query, err := queryFromRequest(request)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if forcedUserID != nil {
		query.UserID = forcedUserID
	}
	result, err := h.searcher.Search(request.Context(), query)
	if err != nil {
		if errors.Is(err, ErrInvalidQuery) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment search is temporarily unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func queryFromRequest(request *http.Request) (Query, error) {
	values := request.URL.Query()
	limit := 20
	if raw := values.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return Query{}, errors.New("limit must be between 1 and 100")
		}
		limit = parsed
	}
	query := Query{Text: strings.TrimSpace(values.Get("q")), Limit: limit}
	if len(query.Text) > 200 {
		return Query{}, errors.New("q must not exceed 200 characters")
	}
	if raw := values.Get("user_id"); raw != "" {
		value, err := uuid.Parse(raw)
		if err != nil {
			return Query{}, errors.New("user_id must be a UUID")
		}
		query.UserID = &value
	}
	if raw := values.Get("status"); raw != "" {
		seen := map[string]bool{}
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(strings.ToLower(value))
			if !validStatus(value) {
				return Query{}, errors.New("status contains an unsupported value")
			}
			if !seen[value] {
				query.Statuses = append(query.Statuses, value)
				seen[value] = true
			}
		}
	}
	if raw := strings.ToUpper(strings.TrimSpace(values.Get("currency"))); raw != "" {
		if len(raw) != 3 {
			return Query{}, errors.New("currency must be a three-letter code")
		}
		query.Currency = raw
	}
	var err error
	if query.AmountMin, err = optionalInt64(values.Get("amount_min"), "amount_min"); err != nil {
		return Query{}, err
	}
	if query.AmountMax, err = optionalInt64(values.Get("amount_max"), "amount_max"); err != nil {
		return Query{}, err
	}
	if query.AmountMin != nil && query.AmountMax != nil && *query.AmountMin > *query.AmountMax {
		return Query{}, errors.New("amount_min must not exceed amount_max")
	}
	if query.CreatedFrom, err = optionalTime(values.Get("created_from"), "created_from"); err != nil {
		return Query{}, err
	}
	if query.CreatedTo, err = optionalTime(values.Get("created_to"), "created_to"); err != nil {
		return Query{}, err
	}
	if query.CreatedFrom != nil && query.CreatedTo != nil && query.CreatedFrom.After(*query.CreatedTo) {
		return Query{}, errors.New("created_from must not be after created_to")
	}
	if raw := values.Get("cursor"); raw != "" {
		cursor, err := DecodeCursor(raw)
		if err != nil {
			return Query{}, err
		}
		query.Cursor = &cursor
	}
	return query, nil
}

func optionalInt64(raw, name string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil, errors.New(name + " must be a non-negative integer")
	}
	return &value, nil
}

func optionalTime(raw, name string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errors.New(name + " must be RFC3339")
	}
	return &value, nil
}

func validStatus(value string) bool {
	switch value {
	case "pending", "processing", "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
