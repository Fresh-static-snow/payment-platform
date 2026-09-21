package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidPayment   = errors.New("invalid payment for risk assessment")
	ErrInvalidPolicy    = errors.New("invalid risk policy")
	ErrDecisionNotFound = errors.New("risk decision not found")
	ErrInvalidDecision  = errors.New("invalid risk decision")
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

func (d Decision) Valid() bool {
	return d == DecisionAllow || d == DecisionDeny
}

// Payment is the immutable input used by the risk policy. Monetary values are
// expressed in the currency's minor units.
type Payment struct {
	ID       uuid.UUID
	UserID   uuid.UUID
	Amount   int64
	Currency string
}

func (p Payment) Validate() error {
	if p.ID == uuid.Nil {
		return fmt.Errorf("%w: payment_id must be a valid UUID", ErrInvalidPayment)
	}
	if p.UserID == uuid.Nil {
		return fmt.Errorf("%w: user_id must be a valid UUID", ErrInvalidPayment)
	}
	if p.Amount <= 0 {
		return fmt.Errorf("%w: amount must be greater than zero", ErrInvalidPayment)
	}
	currency := strings.TrimSpace(p.Currency)
	if len(currency) != 3 || strings.ToUpper(currency) != currency {
		return fmt.Errorf("%w: currency must be a three-letter uppercase code", ErrInvalidPayment)
	}
	return nil
}

// Assessment is the durable result of evaluating one payment with one policy
// version. The (PaymentID, RulesVersion) pair is its idempotency identity.
type Assessment struct {
	ID           uuid.UUID
	PaymentID    uuid.UUID
	RulesVersion string
	Score        int
	Decision     Decision
	Reasons      []string
	CreatedAt    time.Time
}

func (a Assessment) Validate() error {
	if a.ID == uuid.Nil {
		return fmt.Errorf("%w: id must be a valid UUID", ErrInvalidDecision)
	}
	if a.PaymentID == uuid.Nil {
		return fmt.Errorf("%w: payment_id must be a valid UUID", ErrInvalidDecision)
	}
	if strings.TrimSpace(a.RulesVersion) == "" {
		return fmt.Errorf("%w: rules_version is required", ErrInvalidDecision)
	}
	if a.Score < 0 || a.Score > 100 {
		return fmt.Errorf("%w: score must be between 0 and 100", ErrInvalidDecision)
	}
	if !a.Decision.Valid() {
		return fmt.Errorf("%w: unsupported decision %q", ErrInvalidDecision, a.Decision)
	}
	if a.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at is required", ErrInvalidDecision)
	}
	return nil
}
