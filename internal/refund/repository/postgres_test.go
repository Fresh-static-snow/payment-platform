package repository

import (
	"errors"
	"testing"

	refunddomain "github.com/praedyth/payment-platform/internal/refund/domain"
)

func TestValidateTransitionDetails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		status            refunddomain.Status
		providerReference string
		failureReason     string
		wantErr           error
	}{
		{name: "processing", status: refunddomain.StatusProcessing},
		{name: "completed", status: refunddomain.StatusCompleted, providerReference: "refund_provider_1"},
		{name: "failed", status: refunddomain.StatusFailed, failureReason: "provider rejected"},
		{name: "completed missing reference", status: refunddomain.StatusCompleted, wantErr: refunddomain.ErrInvalidRefund},
		{name: "failed missing reason", status: refunddomain.StatusFailed, wantErr: refunddomain.ErrInvalidRefund},
		{name: "processing with details", status: refunddomain.StatusProcessing, providerReference: "unexpected", wantErr: refunddomain.ErrInvalidRefund},
		{name: "unsupported target", status: refunddomain.StatusPending, wantErr: refunddomain.ErrInvalidStatusTransition},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateTransitionDetails(test.status, test.providerReference, test.failureReason)
			if test.wantErr == nil && err != nil {
				t.Fatalf("validateTransitionDetails() error = %v", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("validateTransitionDetails() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
