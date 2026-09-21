package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEnsureIndexCreatesMappingAndAlias(t *testing.T) {
	t.Parallel()
	steps := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		steps++
		switch steps {
		case 1:
			if request.Method != http.MethodHead || request.URL.Path != "/payments-read-v1" {
				t.Errorf("unexpected first request: %s %s", request.Method, request.URL.Path)
			}
			w.WriteHeader(http.StatusNotFound)
		case 2:
			body, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPut || !strings.Contains(string(body), `"dynamic": "strict"`) {
				t.Errorf("mapping request is not strict: %s %s", request.Method, body)
			}
			w.WriteHeader(http.StatusOK)
		case 3:
			if request.Method != http.MethodHead || request.URL.Path != "/_alias/payments-read" {
				t.Errorf("unexpected alias probe: %s %s", request.Method, request.URL.Path)
			}
			w.WriteHeader(http.StatusNotFound)
		case 4:
			body, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPost || request.URL.Path != "/_aliases" || !strings.Contains(string(body), `"is_write_index":true`) {
				t.Errorf("unexpected alias request: %s %s %s", request.Method, request.URL.Path, body)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected extra request")
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	if err := client.EnsureIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	if steps != 4 {
		t.Fatalf("got %d requests, want 4", steps)
	}
}

func TestEnsureIndexAcceptsConcurrentCreate(t *testing.T) {
	t.Parallel()
	steps := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		steps++
		switch steps {
		case 1:
			w.WriteHeader(http.StatusNotFound)
		case 2:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"resource_already_exists_exception"},"status":400}`)
		case 3:
			if request.URL.Path != "/_alias/payments-read" {
				t.Errorf("unexpected alias path: %s", request.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected extra request")
		}
	}))
	defer server.Close()

	if err := newTestClient(t, server.URL).EnsureIndex(context.Background()); err != nil {
		t.Fatalf("concurrent index creation should be idempotent: %v", err)
	}
	if steps != 3 {
		t.Fatalf("got %d requests, want 3", steps)
	}
}

func TestEnsureIndexRejectsUnexpectedCreateFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"mapper_parsing_exception"}}`)
	}))
	defer server.Close()

	if err := newTestClient(t, server.URL).EnsureIndex(context.Background()); err == nil || !strings.Contains(err.Error(), "mapper_parsing_exception") {
		t.Fatalf("expected mapping error, got %v", err)
	}
}

func TestIndexUsesAggregateExternalVersionAndIgnoresConflict(t *testing.T) {
	t.Parallel()
	paymentID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/payments-read/_doc/"+paymentID.String() {
			t.Errorf("path = %s", request.URL.Path)
		}
		if request.URL.Query().Get("version") != "7" || request.URL.Query().Get("version_type") != "external" {
			t.Errorf("unexpected version query: %s", request.URL.RawQuery)
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	if err := client.Index(context.Background(), PaymentDocument{PaymentID: paymentID, AggregateVersion: 7}); err != nil {
		t.Fatalf("stale/duplicate event must be idempotent: %v", err)
	}
}

func TestSearchBuildsFiltersAndSearchAfter(t *testing.T) {
	t.Parallel()
	paymentID, userID := uuid.New(), uuid.New()
	createdAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if int(body["size"].(float64)) != 2 {
			t.Errorf("size = %v", body["size"])
		}
		if _, ok := body["search_after"]; !ok {
			t.Error("search_after is missing")
		}
		sortValues := body["sort"].([]any)
		createdAtSort := sortValues[0].(map[string]any)["created_at"].(map[string]any)
		if createdAtSort["format"] != "strict_date_optional_time_nanos" {
			t.Errorf("created_at sort format = %v", createdAtSort["format"])
		}
		filters := body["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
		if len(filters) != 4 {
			t.Errorf("filter count = %d, want 4", len(filters))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"hits":{"total":{"value":2},"hits":[`+
			`{"_source":{"payment_id":"`+paymentID.String()+`","user_id":"`+userID.String()+`","amount":100,"currency":"USD","description":"one","status":"completed","aggregate_version":3,"created_at":"`+createdAt.Format(time.RFC3339Nano)+`","updated_at":"`+createdAt.Format(time.RFC3339Nano)+`","last_event_id":"`+uuid.NewString()+`","last_event_type":"payment.completed.v1"},"sort":["`+createdAt.Format(time.RFC3339Nano)+`","`+paymentID.String()+`"]},`+
			`{"_source":{"payment_id":"`+uuid.NewString()+`"},"sort":["`+createdAt.Add(-time.Second).Format(time.RFC3339Nano)+`","`+uuid.NewString()+`"]}`+
			`]}}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	minAmount, maxAmount := int64(50), int64(150)
	result, err := client.Search(context.Background(), Query{
		Text: "annual plan", UserID: &userID, Statuses: []string{"completed", "failed"}, Currency: "usd",
		AmountMin: &minAmount, AmountMax: &maxAmount, Limit: 1, Cursor: &Cursor{CreatedAt: createdAt.Add(time.Hour), PaymentID: uuid.New()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].PaymentID != paymentID || result.Total != 2 || result.NextCursor == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := DecodeCursor(result.NextCursor); err != nil {
		t.Fatalf("next cursor is invalid: %v", err)
	}
}

func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{URL: serverURL, Index: "payments-read-v1", Alias: "payments-read"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestBuildSearchRequestRejectsLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 101} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			if _, err := buildSearchRequest(Query{Limit: limit}); err == nil {
				t.Fatal("expected invalid limit error")
			}
		})
	}
}

func TestNewClientValidatesConfiguration(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		config ClientConfig
	}{
		{name: "invalid URL", config: ClientConfig{URL: "localhost:9200", Index: "v1", Alias: "read"}},
		{name: "missing index", config: ClientConfig{URL: "http://localhost:9200", Alias: "read"}},
		{name: "alias equals index", config: ClientConfig{URL: "http://localhost:9200", Index: "same", Alias: "same"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewClient(test.config); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}
