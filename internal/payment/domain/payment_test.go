package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPaymentTransitions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		from    Status
		to      Status
		allowed bool
	}{
		{StatusPending, StatusProcessing, true},
		{StatusPending, StatusCancelled, true},
		{StatusProcessing, StatusCompleted, true},
		{StatusProcessing, StatusFailed, true},
		{StatusPending, StatusCompleted, false},
		{StatusCompleted, StatusFailed, false},
		{StatusCancelled, StatusProcessing, false},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.from)+"_to_"+string(test.to), func(t *testing.T) {
			payment := Payment{Status: test.from, Version: 1}
			err := payment.Transition(test.to)
			if test.allowed && err != nil {
				t.Fatalf("expected transition to succeed: %v", err)
			}
			if !test.allowed && !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("expected ErrInvalidStatusTransition, got %v", err)
			}
		})
	}
}

func TestCreateParamsValidation(t *testing.T) {
	t.Parallel()
	valid := CreateParams{IdempotencyKey: "key", UserID: uuid.New(), Amount: 100, Currency: "USD"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	invalid := valid
	invalid.Amount = 0
	if !errors.Is(invalid.Validate(), ErrInvalidPayment) {
		t.Fatal("expected invalid payment error")
	}
}
