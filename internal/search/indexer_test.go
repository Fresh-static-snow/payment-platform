package search

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/events"
	"github.com/segmentio/kafka-go"
)

type recordingIndexer struct {
	document PaymentDocument
	err      error
}

func (i *recordingIndexer) Index(_ context.Context, document PaymentDocument) error {
	i.document = document
	return i.err
}

func TestIndexerHandlesLifecycleEnvelope(t *testing.T) {
	t.Parallel()
	envelope := searchTestEnvelope(t)
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingIndexer{}
	if err := NewIndexer(recorder).Handle(context.Background(), kafka.Message{Value: raw}); err != nil {
		t.Fatal(err)
	}
	if recorder.document.PaymentID == uuid.Nil || recorder.document.AggregateVersion != 2 || recorder.document.Status != "completed" {
		t.Fatalf("unexpected indexed document: %+v", recorder.document)
	}
}

func TestIndexerReturnsDecodeAndStorageErrors(t *testing.T) {
	t.Parallel()
	if err := NewIndexer(&recordingIndexer{}).Handle(context.Background(), kafka.Message{Value: []byte("not-json")}); err == nil {
		t.Fatal("expected malformed envelope error")
	}

	envelope := searchTestEnvelope(t)
	raw, _ := json.Marshal(envelope)
	want := errors.New("elasticsearch unavailable")
	err := NewIndexer(&recordingIndexer{err: want}).Handle(context.Background(), kafka.Message{Value: raw})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped storage error", err)
	}
}

func searchTestEnvelope(t *testing.T) events.Envelope {
	t.Helper()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(events.PaymentPayload{
		PaymentID: uuid.New(), UserID: uuid.New(), Amount: 1250, Currency: "usd", Status: "completed",
		AggregateVersion: 2, CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return events.Envelope{
		ID: uuid.New(), Type: events.PaymentCompleted, Version: 1, OccurredAt: now, Payload: payload,
	}
}
