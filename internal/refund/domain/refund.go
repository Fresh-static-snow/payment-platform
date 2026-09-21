package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidRefund           = errors.New("invalid refund")
	ErrRefundNotFound          = errors.New("refund not found")
	ErrRefundAmountExceeded    = errors.New("refund amount exceeds remaining payment amount")
	ErrIdempotencyConflict     = errors.New("idempotency key was already used with another refund request")
	ErrInvalidStatusTransition = errors.New("invalid refund status transition")
	ErrTransitionConflict      = errors.New("refund transition conflicts with the persisted state")
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

type Refund struct {
	ID                uuid.UUID  `json:"id"`
	PaymentID         uuid.UUID  `json:"payment_id"`
	UserID            uuid.UUID  `json:"user_id"`
	Amount            int64      `json:"amount"`
	Currency          string     `json:"currency"`
	Status            Status     `json:"status"`
	ProviderReference string     `json:"provider_reference,omitempty"`
	FailureReason     string     `json:"failure_reason,omitempty"`
	WorkflowID        string     `json:"workflow_id"`
	WorkflowStartedAt *time.Time `json:"workflow_started_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Version           int64      `json:"version"`
}

type CreateParams struct {
	PaymentID      uuid.UUID
	UserID         uuid.UUID
	Amount         int64
	IdempotencyKey string
	RequestHash    []byte
	RequestID      string
}

func (p CreateParams) Validate() error {
	if p.PaymentID == uuid.Nil || p.UserID == uuid.Nil {
		return fmt.Errorf("%w: payment_id and user_id are required", ErrInvalidRefund)
	}
	if p.Amount <= 0 {
		return fmt.Errorf("%w: amount must be greater than zero", ErrInvalidRefund)
	}
	if key := strings.TrimSpace(p.IdempotencyKey); key == "" || len(key) > 255 {
		return fmt.Errorf("%w: idempotency key is required and must not exceed 255 characters", ErrInvalidRefund)
	}
	if len(p.RequestHash) != 32 {
		return fmt.Errorf("%w: request hash must be a SHA-256 digest", ErrInvalidRefund)
	}
	return nil
}

func (r Refund) CanTransition(to Status) bool {
	switch r.Status {
	case StatusPending:
		return to == StatusProcessing || to == StatusFailed
	case StatusProcessing:
		return to == StatusCompleted || to == StatusFailed
	default:
		return false
	}
}

func (r Refund) ValidateTransition(to Status) error {
	if !r.CanTransition(to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidStatusTransition, r.Status, to)
	}
	return nil
}
