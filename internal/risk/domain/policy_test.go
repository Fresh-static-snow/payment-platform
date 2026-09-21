package domain

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestPolicyEvaluate(t *testing.T) {
	t.Parallel()
	policy := DefaultPolicy()
	base := Payment{ID: uuid.New(), UserID: uuid.New(), Amount: 1_000, Currency: "USD"}

	tests := []struct {
		name     string
		payment  Payment
		score    int
		decision Decision
		reasons  []string
	}{
		{
			name:     "ordinary payment is allowed",
			payment:  base,
			score:    0,
			decision: DecisionAllow,
			reasons:  []string{ReasonNoRiskSignals},
		},
		{
			name: "high amount in common currency remains below deny threshold",
			payment: Payment{
				ID: uuid.New(), UserID: uuid.New(), Amount: 100_000, Currency: "EUR",
			},
			score:    35,
			decision: DecisionAllow,
			reasons:  []string{ReasonHighAmount},
		},
		{
			name: "high amount plus uncommon currency is denied",
			payment: Payment{
				ID: uuid.New(), UserID: uuid.New(), Amount: 100_000, Currency: "JPY",
			},
			score:    75,
			decision: DecisionDeny,
			reasons:  []string{ReasonHighAmount, ReasonUncommonCurrency},
		},
		{
			name: "critical amount is denied",
			payment: Payment{
				ID: uuid.New(), UserID: uuid.New(), Amount: 1_000_000, Currency: "USD",
			},
			score:    80,
			decision: DecisionDeny,
			reasons:  []string{ReasonCriticalAmount},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := policy.Evaluate(test.payment)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if got.Score != test.score || got.Decision != test.decision || !reflect.DeepEqual(got.Reasons, test.reasons) {
				t.Fatalf("Evaluate() = %#v, want score=%d decision=%s reasons=%v", got, test.score, test.decision, test.reasons)
			}

			again, err := policy.Evaluate(test.payment)
			if err != nil {
				t.Fatalf("second Evaluate() error = %v", err)
			}
			if !reflect.DeepEqual(got, again) {
				t.Fatalf("policy is not deterministic: first=%#v second=%#v", got, again)
			}
		})
	}
}

func TestPolicyRejectsInvalidPayment(t *testing.T) {
	t.Parallel()
	_, err := DefaultPolicy().Evaluate(Payment{ID: uuid.New(), UserID: uuid.New(), Currency: "USD"})
	if !errors.Is(err, ErrInvalidPayment) {
		t.Fatalf("expected ErrInvalidPayment, got %v", err)
	}
}

func TestNewPolicyValidation(t *testing.T) {
	t.Parallel()
	_, err := NewPolicy(PolicyConfig{})
	if !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("expected ErrInvalidPolicy, got %v", err)
	}
}
