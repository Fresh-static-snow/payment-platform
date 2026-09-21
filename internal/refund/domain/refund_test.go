package domain

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestCreateParamsValidate(t *testing.T) {
	hash := sha256.Sum256([]byte("request"))
	valid := CreateParams{
		PaymentID: uuid.New(), UserID: uuid.New(), Amount: 100,
		IdempotencyKey: "refund-1", RequestHash: hash[:],
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid params: %v", err)
	}
	valid.Amount = 0
	if err := valid.Validate(); !errors.Is(err, ErrInvalidRefund) {
		t.Fatalf("got %v, want ErrInvalidRefund", err)
	}
	valid.Amount = 100
	valid.RequestHash = []byte("short")
	if err := valid.Validate(); !errors.Is(err, ErrInvalidRefund) {
		t.Fatalf("got %v, want ErrInvalidRefund for a non-SHA-256 hash", err)
	}
}

func TestRefundTransitions(t *testing.T) {
	refund := Refund{Status: StatusPending}
	if err := refund.ValidateTransition(StatusProcessing); err != nil {
		t.Fatal(err)
	}
	refund.Status = StatusCompleted
	if err := refund.ValidateTransition(StatusProcessing); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("got %v, want ErrInvalidStatusTransition", err)
	}
}
