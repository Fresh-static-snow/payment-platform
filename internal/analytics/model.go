package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/events"
)

var ErrInvalidEvent = errors.New("invalid analytics event")

type PaymentEvent struct {
	EventID           uuid.UUID `json:"event_id"`
	EventType         string    `json:"event_type"`
	OccurredAt        time.Time `json:"occurred_at"`
	PaymentID         uuid.UUID `json:"payment_id"`
	UserID            uuid.UUID `json:"user_id"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	Description       string    `json:"description"`
	Status            string    `json:"status"`
	ProviderReference string    `json:"provider_reference"`
	FailureReason     string    `json:"failure_reason"`
	AggregateVersion  int64     `json:"aggregate_version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	CorrelationID     string    `json:"correlation_id"`
	CausationID       string    `json:"causation_id"`
	KafkaTopic        string    `json:"kafka_topic"`
	KafkaPartition    int       `json:"kafka_partition"`
	KafkaOffset       int64     `json:"kafka_offset"`
	IngestedAt        time.Time `json:"ingested_at"`
}

func PaymentEventFromEnvelope(envelope events.Envelope, topic string, partition int, offset int64, now time.Time) (PaymentEvent, error) {
	if envelope.ID == uuid.Nil || envelope.Version != 1 || !isPaymentLifecycleEvent(envelope.Type) || envelope.OccurredAt.IsZero() ||
		strings.TrimSpace(topic) == "" || partition < 0 || offset < 0 || now.IsZero() {
		return PaymentEvent{}, fmt.Errorf("%w: unsupported or incomplete envelope", ErrInvalidEvent)
	}
	var payload events.PaymentPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return PaymentEvent{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidEvent, err)
	}
	currency := strings.ToUpper(strings.TrimSpace(payload.Currency))
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if payload.PaymentID == uuid.Nil || payload.UserID == uuid.Nil || payload.Amount <= 0 || payload.AggregateVersion < 1 ||
		!validPaymentCurrency(currency) || !eventMatchesPaymentStatus(envelope.Type, status) ||
		payload.CreatedAt.IsZero() || payload.UpdatedAt.IsZero() {
		return PaymentEvent{}, fmt.Errorf("%w: incomplete payment payload", ErrInvalidEvent)
	}
	return PaymentEvent{
		EventID: envelope.ID, EventType: envelope.Type, OccurredAt: envelope.OccurredAt.UTC(),
		PaymentID: payload.PaymentID, UserID: payload.UserID, Amount: payload.Amount,
		Currency: currency, Description: payload.Description, Status: status,
		ProviderReference: payload.ProviderReference, FailureReason: payload.FailureReason,
		AggregateVersion: payload.AggregateVersion, CreatedAt: payload.CreatedAt.UTC(), UpdatedAt: payload.UpdatedAt.UTC(),
		CorrelationID: envelope.CorrelationID, CausationID: envelope.CausationID,
		KafkaTopic: topic, KafkaPartition: partition, KafkaOffset: offset, IngestedAt: now.UTC(),
	}, nil
}

func validPaymentCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

func eventMatchesPaymentStatus(eventType, status string) bool {
	switch eventType {
	case events.PaymentCreated:
		return status == "pending"
	case events.PaymentProcessing:
		return status == "processing"
	case events.PaymentCompleted:
		return status == "completed"
	case events.PaymentFailed:
		return status == "failed"
	case events.PaymentCancelled:
		return status == "cancelled"
	default:
		return false
	}
}

func isPaymentLifecycleEvent(eventType string) bool {
	switch eventType {
	case events.PaymentCreated, events.PaymentProcessing, events.PaymentCompleted, events.PaymentFailed, events.PaymentCancelled:
		return true
	default:
		return false
	}
}
