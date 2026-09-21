package search

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/events"
)

func TestDocumentFromEnvelope(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, time.September, 20, 8, 30, 0, 0, time.FixedZone("EEST", 3*60*60))
	paymentID, userID, eventID := uuid.New(), uuid.New(), uuid.New()
	payload, err := json.Marshal(events.PaymentPayload{
		PaymentID: paymentID, UserID: userID, Amount: 1250, Currency: "usd", Description: "Annual plan",
		Status: "completed", ProviderReference: "provider-42", AggregateVersion: 3,
		CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := DocumentFromEnvelope(events.Envelope{
		ID: eventID, Type: events.PaymentCompleted, Version: 1, OccurredAt: createdAt.Add(time.Second), Payload: payload,
	})
	if err != nil {
		t.Fatalf("DocumentFromEnvelope returned error: %v", err)
	}
	if document.PaymentID != paymentID || document.UserID != userID || document.Currency != "USD" {
		t.Fatalf("unexpected document identity: %+v", document)
	}
	if document.AggregateVersion != 3 || document.LastEventID != eventID || document.LastEventType != events.PaymentCompleted {
		t.Fatalf("unexpected version metadata: %+v", document)
	}
	if document.CreatedAt.Location() != time.UTC || document.UpdatedAt.Location() != time.UTC {
		t.Fatal("timestamps were not normalized to UTC")
	}
}

func TestDocumentFromEnvelopeRejectsUnversionedPayload(t *testing.T) {
	t.Parallel()
	payload, _ := json.Marshal(events.PaymentPayload{PaymentID: uuid.New(), UserID: uuid.New(), Amount: 1, Currency: "USD", Status: "pending"})
	_, err := DocumentFromEnvelope(events.Envelope{ID: uuid.New(), Type: events.PaymentCreated, Version: 1, OccurredAt: time.Now(), Payload: payload})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("got %v, want ErrInvalidEvent", err)
	}
}

func TestDocumentFromEnvelopeRejectsInconsistentLifecycleEvent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	payload, _ := json.Marshal(events.PaymentPayload{
		PaymentID: uuid.New(), UserID: uuid.New(), Amount: 100, Currency: "US", Status: "failed",
		AggregateVersion: 2, CreatedAt: now, UpdatedAt: now,
	})
	_, err := DocumentFromEnvelope(events.Envelope{
		ID: uuid.New(), Type: events.PaymentCompleted, Version: 1, OccurredAt: now, Payload: payload,
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("got %v, want ErrInvalidEvent", err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()
	want := Cursor{CreatedAt: time.Date(2026, 9, 20, 12, 0, 0, 123, time.UTC), PaymentID: uuid.New()}
	got, err := DecodeCursor(EncodeCursor(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.PaymentID != want.PaymentID || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("cursor mismatch: got %+v, want %+v", got, want)
	}
	if _, err := DecodeCursor("not-base64!"); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("invalid cursor error = %v", err)
	}
}
