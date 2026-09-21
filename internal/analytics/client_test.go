package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestClientInsertUsesJSONEachRowAndDeduplicationToken(t *testing.T) {
	t.Parallel()
	eventID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s", request.Method)
		}
		if query := request.URL.Query().Get("query"); query != "INSERT INTO `payment_events` FORMAT JSONEachRow" {
			t.Errorf("query = %q", query)
		}
		if request.URL.Query().Get("database") != "payment_analytics" || request.URL.Query().Get("insert_deduplication_token") != eventID.String() {
			t.Errorf("unexpected query parameters: %s", request.URL.RawQuery)
		}
		username, password, ok := request.BasicAuth()
		if !ok || username != "analytics" || password != "secret" {
			t.Errorf("unexpected basic auth: %q %q %t", username, password, ok)
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.HasSuffix(string(body), "\n") {
			t.Error("JSONEachRow payload must end with a newline")
		}
		var decoded PaymentEvent
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if decoded.EventID != eventID {
			t.Errorf("event ID = %s", decoded.EventID)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{
		URL: server.URL, Database: " payment_analytics ", Table: " payment_events ", Username: "analytics", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Insert(context.Background(), PaymentEvent{EventID: eventID, IngestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestClientPingAndServerError(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			if request.URL.Query().Get("query") != "SELECT 1" {
				t.Errorf("ping query = %q", request.URL.Query().Get("query"))
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "Code: 60. DB::Exception: table does not exist")
	}))
	defer server.Close()
	client := newAnalyticsTestClient(t, server.URL)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := client.Insert(context.Background(), PaymentEvent{EventID: uuid.New()})
	if err == nil || !strings.Contains(err.Error(), "table does not exist") {
		t.Fatalf("expected ClickHouse error body, got %v", err)
	}
}

func TestClientRejectsInvalidConfigurationAndEvent(t *testing.T) {
	t.Parallel()
	for _, config := range []ClientConfig{
		{URL: "localhost:8123", Database: "payment_analytics", Table: "payment_events"},
		{URL: "http://localhost:8123", Database: "invalid-name", Table: "payment_events"},
		{URL: "http://localhost:8123", Database: "payment_analytics", Table: "events;DROP TABLE events"},
	} {
		if _, err := NewClient(config); err == nil {
			t.Fatalf("expected invalid config error for %+v", config)
		}
	}
	client := newAnalyticsTestClient(t, "http://localhost:8123")
	if err := client.Insert(context.Background(), PaymentEvent{}); err == nil {
		t.Fatal("expected missing event ID error")
	}
}

func newAnalyticsTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{URL: serverURL, Database: "payment_analytics", Table: "payment_events"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
