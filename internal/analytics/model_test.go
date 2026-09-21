package analytics

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/events"
)

func TestPaymentEventFromEnvelope(t *testing.T) {
	t.Parallel()
	zone := time.FixedZone("EEST", 3*60*60)
	createdAt := time.Date(2026, 9, 20, 10, 0, 0, 0, zone)
	ingestedAt := createdAt.Add(5 * time.Second)
	paymentID, userID, eventID := uuid.New(), uuid.New(), uuid.New()
	payload, err := json.Marshal(events.PaymentPayload{
		PaymentID: paymentID, UserID: userID, Amount: 3499, Currency: " eur ", Description: "Annual plan",
		Status: " COMPLETED ", ProviderReference: "provider-42", AggregateVersion: 3,
		CreatedAt: createdAt, UpdatedAt: createdAt.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := PaymentEventFromEnvelope(events.Envelope{
		ID: eventID, Type: events.PaymentCompleted, Version: 1, OccurredAt: createdAt.Add(2 * time.Second),
		CorrelationID: "request-1", CausationID: "event-0", Payload: payload,
	}, events.PaymentCompleted, 2, 41, ingestedAt)
	if err != nil {
		t.Fatal(err)
	}
	if event.EventID != eventID || event.PaymentID != paymentID || event.UserID != userID {
		t.Fatalf("unexpected identity: %+v", event)
	}
	if event.Currency != "EUR" || event.Status != "completed" || event.AggregateVersion != 3 {
		t.Fatalf("unexpected normalized snapshot: %+v", event)
	}
	if event.KafkaTopic != events.PaymentCompleted || event.KafkaPartition != 2 || event.KafkaOffset != 41 {
		t.Fatalf("unexpected Kafka position: %+v", event)
	}
	if event.OccurredAt.Location() != time.UTC || event.CreatedAt.Location() != time.UTC || event.IngestedAt.Location() != time.UTC {
		t.Fatal("analytics timestamps were not normalized to UTC")
	}
}

func TestPaymentEventFromEnvelopeRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	validPayload := events.PaymentPayload{
		PaymentID: uuid.New(), UserID: uuid.New(), Amount: 100, Currency: "USD", Status: "completed",
		AggregateVersion: 2, CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	payload, _ := json.Marshal(validPayload)
	validEnvelope := events.Envelope{
		ID: uuid.New(), Type: events.PaymentCompleted, Version: 1, OccurredAt: now, Payload: payload,
	}

	tests := []struct {
		name      string
		envelope  events.Envelope
		topic     string
		partition int
		offset    int64
		ingested  time.Time
	}{
		{name: "unsupported envelope version", envelope: func() events.Envelope { value := validEnvelope; value.Version = 2; return value }(), topic: events.PaymentCompleted, partition: 0, offset: 0, ingested: now},
		{name: "empty topic", envelope: validEnvelope, partition: 0, offset: 0, ingested: now},
		{name: "negative partition", envelope: validEnvelope, topic: events.PaymentCompleted, partition: -1, offset: 0, ingested: now},
		{name: "negative offset", envelope: validEnvelope, topic: events.PaymentCompleted, partition: 0, offset: -1, ingested: now},
		{name: "zero ingestion time", envelope: validEnvelope, topic: events.PaymentCompleted, partition: 0, offset: 0},
		{name: "malformed payload", envelope: func() events.Envelope { value := validEnvelope; value.Payload = json.RawMessage("{"); return value }(), topic: events.PaymentCompleted, partition: 0, offset: 0, ingested: now},
		{name: "event status mismatch", envelope: func() events.Envelope { value := validEnvelope; value.Type = events.PaymentFailed; return value }(), topic: events.PaymentFailed, partition: 0, offset: 0, ingested: now},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := PaymentEventFromEnvelope(test.envelope, test.topic, test.partition, test.offset, test.ingested)
			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error = %v, want ErrInvalidEvent", err)
			}
		})
	}
}
