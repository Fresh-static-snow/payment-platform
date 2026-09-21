package analytics

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

type recordingEventWriter struct {
	event PaymentEvent
	err   error
}

func (w *recordingEventWriter) Insert(_ context.Context, event PaymentEvent) error {
	w.event = event
	return w.err
}

func TestIngestorAddsKafkaMetadata(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	raw := analyticsTestEnvelope(t, now)
	writer := &recordingEventWriter{}
	ingestor := NewIngestor(writer)
	ingestor.now = func() time.Time { return now.Add(time.Second) }
	if err := ingestor.Handle(context.Background(), kafka.Message{
		Topic: events.PaymentCompleted, Partition: 3, Offset: 81, Value: raw,
	}); err != nil {
		t.Fatal(err)
	}
	if writer.event.KafkaTopic != events.PaymentCompleted || writer.event.KafkaPartition != 3 || writer.event.KafkaOffset != 81 {
		t.Fatalf("unexpected event metadata: %+v", writer.event)
	}
	if !writer.event.IngestedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("ingested_at = %s", writer.event.IngestedAt)
	}
}

func TestIngestorReturnsDecodeAndWriterErrors(t *testing.T) {
	t.Parallel()
	if err := NewIngestor(&recordingEventWriter{}).Handle(context.Background(), kafka.Message{Value: []byte("bad-json")}); err == nil {
		t.Fatal("expected malformed envelope error")
	}

	now := time.Now().UTC()
	want := errors.New("ClickHouse unavailable")
	ingestor := NewIngestor(&recordingEventWriter{err: want})
	ingestor.now = func() time.Time { return now }
	err := ingestor.Handle(context.Background(), kafka.Message{
		Topic: events.PaymentCompleted, Partition: 0, Offset: 1, Value: analyticsTestEnvelope(t, now),
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped writer error", err)
	}
}

func analyticsTestEnvelope(t *testing.T, now time.Time) []byte {
	t.Helper()
	payload, err := json.Marshal(events.PaymentPayload{
		PaymentID: uuid.New(), UserID: uuid.New(), Amount: 500, Currency: "USD", Status: "completed",
		AggregateVersion: 2, CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := json.Marshal(events.Envelope{
		ID: uuid.New(), Type: events.PaymentCompleted, Version: 1, OccurredAt: now, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
