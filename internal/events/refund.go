package events

import (
	"time"

	"github.com/google/uuid"
)

const (
	RefundRequested  = "refund.requested.v1"
	RefundProcessing = "refund.processing.v1"
	RefundCompleted  = "refund.completed.v1"
	RefundFailed     = "refund.failed.v1"
)

type RefundPayload struct {
	RefundID          uuid.UUID `json:"refund_id"`
	PaymentID         uuid.UUID `json:"payment_id"`
	UserID            uuid.UUID `json:"user_id"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	Status            string    `json:"status"`
	AggregateVersion  int64     `json:"aggregate_version"`
	ProviderReference string    `json:"provider_reference,omitempty"`
	FailureReason     string    `json:"failure_reason,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
