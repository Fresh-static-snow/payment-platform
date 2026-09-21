package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/internal/ledger/domain"
)

type recordingStore struct {
	created  bool
	err      error
	called   int
	consumer string
	journal  domain.Journal
}

func (store *recordingStore) Store(_ context.Context, consumer string, journal domain.Journal) (bool, error) {
	store.called++
	store.consumer = consumer
	store.journal = journal
	return store.created, store.err
}

func TestProcessorPostsCompletedPayment(t *testing.T) {
	paymentID := uuid.New()
	event := completedPaymentEvent(t, paymentID)
	store := &recordingStore{created: true}
	processor := NewProcessor(store, nil)

	if err := processor.Process(context.Background(), event); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if store.called != 1 {
		t.Fatalf("Store() calls = %d, want 1", store.called)
	}
	if store.consumer != ConsumerName {
		t.Fatalf("consumer = %q, want %q", store.consumer, ConsumerName)
	}
	if store.journal.EventID != event.ID || store.journal.PaymentID != paymentID {
		t.Fatalf("stored journal identifiers do not match event")
	}
	if err := store.journal.Validate(); err != nil {
		t.Fatalf("stored journal is invalid: %v", err)
	}
}

func TestProcessorTreatsInboxDuplicateAsSuccess(t *testing.T) {
	store := &recordingStore{created: false}
	processor := NewProcessor(store, nil)

	if err := processor.Process(context.Background(), completedPaymentEvent(t, uuid.New())); err != nil {
		t.Fatalf("Process() duplicate error = %v", err)
	}
}

func TestProcessorPostsCompletedRefund(t *testing.T) {
	paymentID, refundID := uuid.New(), uuid.New()
	payload, err := json.Marshal(events.RefundPayload{
		RefundID: refundID, PaymentID: paymentID, UserID: uuid.New(), Amount: 300,
		Currency: "USD", Status: "completed", AggregateVersion: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := events.Envelope{ID: uuid.New(), Type: events.RefundCompleted, Version: 1, OccurredAt: time.Now().UTC(), Payload: payload}
	store := &recordingStore{created: true}
	if err := NewProcessor(store, nil).Process(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if store.journal.ReferenceType != "refund" || store.journal.ReferenceID != refundID || store.journal.PaymentID != paymentID {
		t.Fatalf("unexpected refund journal: %+v", store.journal)
	}
}

func TestProcessorRejectsInvalidEventsBeforeStorage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*events.Envelope)
	}{
		{name: "missing event id", mutate: func(event *events.Envelope) { event.ID = uuid.Nil }},
		{name: "wrong type", mutate: func(event *events.Envelope) { event.Type = events.PaymentFailed }},
		{name: "wrong version", mutate: func(event *events.Envelope) { event.Version = 2 }},
		{name: "missing occurred at", mutate: func(event *events.Envelope) { event.OccurredAt = time.Time{} }},
		{name: "malformed payload", mutate: func(event *events.Envelope) { event.Payload = json.RawMessage("{") }},
		{name: "wrong status", mutate: func(event *events.Envelope) {
			payload := events.PaymentPayload{PaymentID: uuid.New(), Amount: 10, Currency: "USD", Status: "failed"}
			event.Payload, _ = json.Marshal(payload)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := completedPaymentEvent(t, uuid.New())
			test.mutate(&event)
			store := &recordingStore{}
			processor := NewProcessor(store, nil)
			if err := processor.Process(context.Background(), event); err == nil {
				t.Fatal("Process() error = nil, want validation error")
			}
			if store.called != 0 {
				t.Fatalf("Store() calls = %d, want 0", store.called)
			}
		})
	}
}

func TestProcessorWrapsStoreFailure(t *testing.T) {
	wantErr := errors.New("database unavailable")
	processor := NewProcessor(&recordingStore{err: wantErr}, nil)

	err := processor.Process(context.Background(), completedPaymentEvent(t, uuid.New()))
	if !errors.Is(err, wantErr) {
		t.Fatalf("Process() error = %v, want wrapped store error", err)
	}
}

func completedPaymentEvent(t *testing.T, paymentID uuid.UUID) events.Envelope {
	t.Helper()
	payload, err := json.Marshal(events.PaymentPayload{
		PaymentID: paymentID,
		UserID:    uuid.New(),
		Amount:    1000,
		Currency:  "USD",
		Status:    "completed",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return events.Envelope{
		ID:            uuid.New(),
		Type:          events.PaymentCompleted,
		Version:       1,
		OccurredAt:    time.Now().UTC(),
		CorrelationID: "request-123",
		Payload:       payload,
	}
}
