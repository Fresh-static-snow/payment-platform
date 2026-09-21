package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

const (
	PaymentCreated        = "payment.created.v1"
	PaymentProcessing     = "payment.processing.v1"
	PaymentCompleted      = "payment.completed.v1"
	PaymentFailed         = "payment.failed.v1"
	PaymentCancelled      = "payment.cancelled.v1"
	ReceiptRequested      = "receipt.requested.v1"
	ReceiptCreated        = "receipt.created.v1"
	NotificationRequested = "notification.requested.v1"
	LedgerPosted          = "ledger.posted.v1"
)

// Envelope gives every message a stable identity and tracing lineage. Consumers
// use ID as their inbox key, so redelivery is safe rather than exceptional.
type Envelope struct {
	ID            uuid.UUID         `json:"event_id"`
	Type          string            `json:"event_type"`
	Version       int               `json:"version"`
	OccurredAt    time.Time         `json:"occurred_at"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	CausationID   string            `json:"causation_id,omitempty"`
	TraceContext  map[string]string `json:"trace_context,omitempty"`
	Payload       json.RawMessage   `json:"payload"`
}

func New(eventType, correlationID, causationID string, payload any) (Envelope, error) {
	return NewContext(context.Background(), eventType, correlationID, causationID, payload)
}

func NewContext(ctx context.Context, eventType, correlationID, causationID string, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal %s payload: %w", eventType, err)
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return Envelope{
		ID:            uuid.New(),
		Type:          eventType,
		Version:       1,
		OccurredAt:    time.Now().UTC(),
		CorrelationID: correlationID,
		CausationID:   causationID,
		TraceContext:  map[string]string(carrier),
		Payload:       raw,
	}, nil
}

type PaymentPayload struct {
	PaymentID         uuid.UUID `json:"payment_id"`
	UserID            uuid.UUID `json:"user_id"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	Description       string    `json:"description,omitempty"`
	Status            string    `json:"status"`
	ProviderReference string    `json:"provider_reference,omitempty"`
	FailureReason     string    `json:"failure_reason,omitempty"`
	AggregateVersion  int64     `json:"aggregate_version,omitempty"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

type ReceiptPayload struct {
	PaymentID uuid.UUID `json:"payment_id"`
	UserID    uuid.UUID `json:"user_id"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	ObjectKey string    `json:"object_key,omitempty"`
}
