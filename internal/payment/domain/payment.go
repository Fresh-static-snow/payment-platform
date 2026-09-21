package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrPaymentNotFound         = errors.New("payment not found")
	ErrInvalidStatusTransition = errors.New("invalid payment status transition")
	ErrIdempotencyConflict     = errors.New("idempotency key was already used with another request")
	ErrPaymentAlreadyCompleted = errors.New("payment already completed")
	ErrConcurrentModification  = errors.New("payment was modified concurrently")
	ErrInvalidPayment          = errors.New("invalid payment")
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

var transitions = map[Status]map[Status]struct{}{
	StatusPending: {
		StatusProcessing: {},
		StatusCancelled:  {},
	},
	StatusProcessing: {
		StatusCompleted: {},
		StatusFailed:    {},
	},
}

type Payment struct {
	ID                uuid.UUID `json:"id"`
	IdempotencyKey    string    `json:"-"`
	UserID            uuid.UUID `json:"user_id"`
	Amount            int64     `json:"amount"`
	Currency          string    `json:"currency"`
	Description       string    `json:"description"`
	Status            Status    `json:"status"`
	ProviderReference string    `json:"provider_reference,omitempty"`
	FailureReason     string    `json:"failure_reason,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	Version           int64     `json:"version"`
}

type CreateParams struct {
	IdempotencyKey string
	RequestHash    []byte
	UserID         uuid.UUID
	Amount         int64
	Currency       string
	Description    string
	RequestID      string
}

func (p CreateParams) Validate() error {
	if strings.TrimSpace(p.IdempotencyKey) == "" || len(p.IdempotencyKey) > 255 {
		return fmt.Errorf("%w: idempotency key is required and must not exceed 255 characters", ErrInvalidPayment)
	}
	if p.UserID == uuid.Nil {
		return fmt.Errorf("%w: user_id must be a valid UUID", ErrInvalidPayment)
	}
	if p.Amount <= 0 {
		return fmt.Errorf("%w: amount must be greater than zero", ErrInvalidPayment)
	}
	if !ValidCurrency(p.Currency) {
		return fmt.Errorf("%w: unsupported currency", ErrInvalidPayment)
	}
	if len(p.Description) > 500 {
		return fmt.Errorf("%w: description must not exceed 500 characters", ErrInvalidPayment)
	}
	return nil
}

func ValidCurrency(value string) bool {
	switch strings.ToUpper(value) {
	case "USD", "EUR", "GBP", "UAH", "PLN":
		return true
	default:
		return false
	}
}

func (p Payment) CanTransition(to Status) bool {
	_, ok := transitions[p.Status][to]
	return ok
}

func (p *Payment) Transition(to Status) error {
	if !p.CanTransition(to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidStatusTransition, p.Status, to)
	}
	p.Status = to
	p.Version++
	p.UpdatedAt = time.Now().UTC()
	return nil
}
