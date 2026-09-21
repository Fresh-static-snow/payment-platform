//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/praedyth/payment-platform/pkg/awsx"
)

func TestPaymentCompletesWithLedgerReceiptAndNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	apiURL := env("API_URL", "http://payment-api:8080")
	userID := uuid.NewString()
	body := []byte(`{"amount":1000,"currency":"USD","description":"E2E payment"}`)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/api/v1/payments", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "e2e-"+uuid.NewString())
	request.Header.Set("X-User-ID", userID)
	response := do(t, request)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.StatusCode, response.Body)
	}
	var created struct {
		ID uuid.UUID `json:"id"`
	}
	decode(t, response.Body, &created)

	eventually(t, ctx, func() (bool, error) {
		get, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+"/api/v1/payments/"+created.ID.String(), nil)
		get.Header.Set("X-User-ID", userID)
		result := do(t, get)
		if result.StatusCode == http.StatusTooManyRequests {
			return false, nil
		}
		if result.StatusCode != http.StatusOK {
			return false, fmt.Errorf("get payment status=%d body=%s", result.StatusCode, result.Body)
		}
		var payment struct {
			Status string `json:"status"`
		}
		decode(t, result.Body, &payment)
		if payment.Status == "failed" {
			return false, fmt.Errorf("payment failed")
		}
		return payment.Status == "completed", nil
	})

	receiptRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/api/v1/payments/"+created.ID.String()+"/receipt", nil)
	receiptRequest.Header.Set("X-User-ID", userID)
	if result := do(t, receiptRequest); result.StatusCode != http.StatusAccepted {
		t.Fatalf("receipt status=%d body=%s", result.StatusCode, result.Body)
	}

	clients, err := awsx.New(ctx, env("AWS_ENDPOINT", "http://localstack:4566"), env("AWS_REGION", "us-east-1"), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, ctx, func() (bool, error) {
		_, err := clients.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(env("S3_BUCKET", "payment-receipts")), Key: aws.String("receipts/" + created.ID.String() + ".json")})
		return err == nil, nil
	})

	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://payment:payment@postgres:5432/payments?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	eventually(t, ctx, func() (bool, error) {
		var entries, sends int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries e JOIN ledger_journals j ON j.id=e.journal_id WHERE j.reference_type='payment' AND j.reference_id=$1`, created.ID).Scan(&entries); err != nil {
			return false, err
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_sends WHERE payment_id=$1`, created.ID).Scan(&sends); err != nil {
			return false, err
		}
		return entries == 2 && sends == 1, nil
	})

	assertDebeziumRunning(t, ctx)
	assertSearchProjection(t, ctx, userID, created.ID)
	assertAnalyticsProjection(t, ctx, created.ID)
	assertRefundAndReconciliation(t, ctx, pool, userID, created.ID)
}

func assertDebeziumRunning(t *testing.T, ctx context.Context) {
	t.Helper()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, env("DEBEZIUM_URL", "http://debezium:8083")+"/connectors/payment-platform-outbox/status", nil)
	response := do(t, request)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("debezium status=%d body=%s", response.StatusCode, response.Body)
	}
	var status struct {
		Connector struct {
			State string `json:"state"`
		} `json:"connector"`
		Tasks []struct {
			State string `json:"state"`
		} `json:"tasks"`
	}
	decode(t, response.Body, &status)
	if status.Connector.State != "RUNNING" || len(status.Tasks) != 1 || status.Tasks[0].State != "RUNNING" {
		t.Fatalf("debezium connector is not running: %s", response.Body)
	}
}

func assertSearchProjection(t *testing.T, ctx context.Context, userID string, paymentID uuid.UUID) {
	t.Helper()
	searchURL := env("SEARCH_URL", "http://payment-search-service:8089")
	eventually(t, ctx, func() (bool, error) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, searchURL+"/api/v1/payment-search?q=E2E&status=completed", nil)
		request.Header.Set("X-User-ID", userID)
		response := do(t, request)
		if response.StatusCode != http.StatusOK {
			return false, fmt.Errorf("search status=%d body=%s", response.StatusCode, response.Body)
		}
		var result struct {
			Items []struct {
				PaymentID uuid.UUID `json:"payment_id"`
			} `json:"items"`
		}
		decode(t, response.Body, &result)
		for _, item := range result.Items {
			if item.PaymentID == paymentID {
				return true, nil
			}
		}
		return false, nil
	})
}

func assertAnalyticsProjection(t *testing.T, ctx context.Context, paymentID uuid.UUID) {
	t.Helper()
	clickHouseURL := env("CLICKHOUSE_URL", "http://clickhouse:8123")
	query := "SELECT countDistinct(event_id) FROM payment_analytics.payment_events WHERE payment_id = toUUID('" + paymentID.String() + "') FORMAT TabSeparated"
	eventually(t, ctx, func() (bool, error) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, clickHouseURL+"/?query="+url.QueryEscape(query), nil)
		request.SetBasicAuth(env("CLICKHOUSE_USERNAME", "analytics"), env("CLICKHOUSE_PASSWORD", "analytics"))
		response := do(t, request)
		if response.StatusCode != http.StatusOK {
			return false, fmt.Errorf("clickhouse status=%d body=%s", response.StatusCode, response.Body)
		}
		return strings.TrimSpace(string(response.Body)) == "3", nil
	})
}

func assertRefundAndReconciliation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID string, paymentID uuid.UUID) {
	t.Helper()
	workflowURL := env("WORKFLOW_URL", "http://workflow-service:8087")
	request, _ := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		workflowURL+"/api/v1/payments/"+paymentID.String()+"/refunds",
		bytes.NewReader([]byte(`{"amount":400}`)),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "e2e-refund-"+uuid.NewString())
	request.Header.Set("X-User-ID", userID)
	response := do(t, request)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("refund status=%d body=%s", response.StatusCode, response.Body)
	}
	var refund struct {
		ID     uuid.UUID `json:"id"`
		Status string    `json:"status"`
	}
	decode(t, response.Body, &refund)

	eventually(t, ctx, func() (bool, error) {
		get, _ := http.NewRequestWithContext(ctx, http.MethodGet, workflowURL+"/api/v1/refunds/"+refund.ID.String(), nil)
		get.Header.Set("X-User-ID", userID)
		result := do(t, get)
		if result.StatusCode != http.StatusOK {
			return false, fmt.Errorf("get refund status=%d body=%s", result.StatusCode, result.Body)
		}
		decode(t, result.Body, &refund)
		if refund.Status == "failed" {
			return false, errors.New("refund failed")
		}
		return refund.Status == "completed", nil
	})

	eventually(t, ctx, func() (bool, error) {
		var entries int
		err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM ledger_entries e
			JOIN ledger_journals j ON j.id=e.journal_id
			WHERE j.reference_type='refund' AND j.reference_id=$1
		`, refund.ID).Scan(&entries)
		return entries == 2, err
	})

	reconcile, _ := http.NewRequestWithContext(ctx, http.MethodPost, workflowURL+"/api/v1/reconciliation/runs", nil)
	reconcile.Header.Set("X-User-ID", userID)
	reconcileResponse := do(t, reconcile)
	if reconcileResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("reconciliation status=%d body=%s", reconcileResponse.StatusCode, reconcileResponse.Body)
	}
	var run struct {
		ID         uuid.UUID `json:"id"`
		Status     string    `json:"status"`
		IssueCount int       `json:"issue_count"`
	}
	decode(t, reconcileResponse.Body, &run)
	eventually(t, ctx, func() (bool, error) {
		get, _ := http.NewRequestWithContext(ctx, http.MethodGet, workflowURL+"/api/v1/reconciliation/runs/"+run.ID.String(), nil)
		get.Header.Set("X-User-ID", userID)
		result := do(t, get)
		if result.StatusCode != http.StatusOK {
			return false, fmt.Errorf("get reconciliation status=%d body=%s", result.StatusCode, result.Body)
		}
		var payload struct {
			Run struct {
				Status     string `json:"status"`
				IssueCount int    `json:"issue_count"`
			} `json:"run"`
		}
		decode(t, result.Body, &payload)
		if payload.Run.Status == "failed" {
			return false, errors.New("reconciliation failed")
		}
		return payload.Run.Status == "completed" && payload.Run.IssueCount == 0, nil
	})
}

type result struct {
	StatusCode int
	Body       []byte
}

func do(t *testing.T, request *http.Request) result {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return result{StatusCode: response.StatusCode, Body: body}
}

func decode(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func eventually(t *testing.T, ctx context.Context, condition func() (bool, error)) {
	t.Helper()
	// Keep asynchronous probes below the API's production-like sliding-window
	// rate limit. The previous 250ms loop could turn a healthy slow dependency
	// into a wall of 429 responses and hide the real system state.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ok, err := condition()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("condition not met: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
