package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"

	"github.com/praedyth/payment-platform/internal/reconciliation"
	refunddomain "github.com/praedyth/payment-platform/internal/refund/domain"
	refundrepository "github.com/praedyth/payment-platform/internal/refund/repository"
)

type Activities struct {
	Refunds        *refundrepository.Store
	Reconciliation *reconciliation.Repository
	ProviderMode   string
	ProviderDelay  time.Duration
}

func (a *Activities) MarkRefundProcessing(ctx context.Context, input RefundWorkflowInput) error {
	id, err := uuid.Parse(input.RefundID)
	if err != nil {
		return fmt.Errorf("parse refund id: %w", err)
	}
	_, err = a.Refunds.Transition(ctx, id, refunddomain.StatusProcessing, "", "", input.RequestID)
	return err
}

func (a *Activities) ExecuteProviderRefund(ctx context.Context, input RefundWorkflowInput) (string, error) {
	id, err := uuid.Parse(input.RefundID)
	if err != nil {
		return "", fmt.Errorf("parse refund id: %w", err)
	}
	info := activity.GetInfo(ctx)
	activity.RecordHeartbeat(ctx, map[string]any{"refund_id": id.String(), "attempt": info.Attempt})
	mode := a.ProviderMode
	if mode == "" {
		mode = "success"
	}
	if mode == "slow" {
		delay := a.ProviderDelay
		if delay <= 0 {
			delay = 3 * time.Second
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		heartbeat := time.NewTicker(time.Second)
		defer heartbeat.Stop()
		waiting := true
		for waiting {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-heartbeat.C:
				activity.RecordHeartbeat(ctx, map[string]any{"refund_id": id.String(), "attempt": info.Attempt})
			case <-timer.C:
				waiting = false
			}
		}
	}
	if mode == "fail" || (mode == "by_refund_id" && id[0]%5 == 0) {
		return "", errors.New("fake refund provider rejected request")
	}
	digest := sha256.Sum256([]byte(id.String()))
	return "refund_" + hex.EncodeToString(digest[:8]), nil
}

func (a *Activities) CompleteRefund(ctx context.Context, input CompleteRefundInput) error {
	id, err := uuid.Parse(input.RefundID)
	if err != nil {
		return fmt.Errorf("parse refund id: %w", err)
	}
	_, err = a.Refunds.Transition(
		ctx, id, refunddomain.StatusCompleted, input.ProviderReference, "", input.RequestID,
	)
	return err
}

func (a *Activities) FailRefund(ctx context.Context, input FailInput) error {
	id, err := uuid.Parse(input.ID)
	if err != nil {
		return fmt.Errorf("parse refund id: %w", err)
	}
	_, err = a.Refunds.Transition(ctx, id, refunddomain.StatusFailed, "", input.Reason, input.RequestID)
	return err
}

func (a *Activities) ExecuteReconciliation(ctx context.Context, input ReconciliationWorkflowInput) error {
	id, err := uuid.Parse(input.RunID)
	if err != nil {
		return fmt.Errorf("parse reconciliation run id: %w", err)
	}
	_, err = a.Reconciliation.Execute(ctx, id)
	return err
}

func (a *Activities) FailReconciliation(ctx context.Context, input FailInput) error {
	id, err := uuid.Parse(input.ID)
	if err != nil {
		return fmt.Errorf("parse reconciliation run id: %w", err)
	}
	return a.Reconciliation.Fail(ctx, id, input.Reason)
}
