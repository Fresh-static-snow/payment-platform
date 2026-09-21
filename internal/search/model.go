package search

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/events"
)

var (
	ErrInvalidEvent = errors.New("invalid payment search event")
	ErrInvalidQuery = errors.New("invalid payment search query")
)

type PaymentDocument struct {
	PaymentID         uuid.UUID `json:"payment_id"`
	UserID            uuid.UUID `json:"user_id"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	Description       string    `json:"description"`
	Status            string    `json:"status"`
	ProviderReference string    `json:"provider_reference,omitempty"`
	FailureReason     string    `json:"failure_reason,omitempty"`
	AggregateVersion  int64     `json:"aggregate_version"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastEventID       uuid.UUID `json:"last_event_id"`
	LastEventType     string    `json:"last_event_type"`
}

func DocumentFromEnvelope(envelope events.Envelope) (PaymentDocument, error) {
	if envelope.ID == uuid.Nil || envelope.Version != 1 || envelope.OccurredAt.IsZero() || !IsPaymentLifecycleEvent(envelope.Type) {
		return PaymentDocument{}, fmt.Errorf("%w: unsupported event %q", ErrInvalidEvent, envelope.Type)
	}
	var payload events.PaymentPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return PaymentDocument{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidEvent, err)
	}
	currency := strings.ToUpper(strings.TrimSpace(payload.Currency))
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if payload.PaymentID == uuid.Nil || payload.UserID == uuid.Nil || payload.Amount <= 0 ||
		!validCurrency(currency) || !eventMatchesStatus(envelope.Type, status) ||
		payload.AggregateVersion < 1 || payload.CreatedAt.IsZero() || payload.UpdatedAt.IsZero() {
		return PaymentDocument{}, fmt.Errorf("%w: incomplete payment projection payload", ErrInvalidEvent)
	}
	return PaymentDocument{
		PaymentID: payload.PaymentID, UserID: payload.UserID, Amount: payload.Amount,
		Currency: currency, Description: payload.Description, Status: status,
		ProviderReference: payload.ProviderReference, FailureReason: payload.FailureReason,
		AggregateVersion: payload.AggregateVersion, CreatedAt: payload.CreatedAt.UTC(), UpdatedAt: payload.UpdatedAt.UTC(),
		LastEventID: envelope.ID, LastEventType: envelope.Type,
	}, nil
}

func validCurrency(value string) bool {
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

func eventMatchesStatus(eventType, status string) bool {
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

func IsPaymentLifecycleEvent(eventType string) bool {
	switch eventType {
	case events.PaymentCreated, events.PaymentProcessing, events.PaymentCompleted, events.PaymentFailed, events.PaymentCancelled:
		return true
	default:
		return false
	}
}

type Cursor struct {
	CreatedAt time.Time `json:"created_at"`
	PaymentID uuid.UUID `json:"payment_id"`
}

func EncodeCursor(cursor Cursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeCursor(value string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: decode cursor: %v", ErrInvalidQuery, err)
	}
	var cursor Cursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.CreatedAt.IsZero() || cursor.PaymentID == uuid.Nil {
		return Cursor{}, fmt.Errorf("%w: malformed cursor", ErrInvalidQuery)
	}
	return cursor, nil
}

type Query struct {
	Text        string
	UserID      *uuid.UUID
	Statuses    []string
	Currency    string
	AmountMin   *int64
	AmountMax   *int64
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	Limit       int
	Cursor      *Cursor
}

type Result struct {
	Items      []PaymentDocument `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Total      int64             `json:"total"`
}
