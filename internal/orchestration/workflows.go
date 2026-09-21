package orchestration

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue                     = "payment-platform-workflows-v1"
	RefundWorkflowName            = "payment-platform.refund.v1"
	ReconciliationWorkflowName    = "payment-platform.reconciliation.v1"
	MarkRefundProcessingActivity  = "refund.mark-processing"
	ExecuteProviderRefundActivity = "refund.execute-provider"
	CompleteRefundActivity        = "refund.complete"
	FailRefundActivity            = "refund.fail"
	ExecuteReconciliationActivity = "reconciliation.execute"
	FailReconciliationActivity    = "reconciliation.fail"
)

type RefundWorkflowInput struct {
	RefundID  string `json:"refund_id"`
	RequestID string `json:"request_id"`
}

type CompleteRefundInput struct {
	RefundID          string `json:"refund_id"`
	RequestID         string `json:"request_id"`
	ProviderReference string `json:"provider_reference"`
}

type FailInput struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id,omitempty"`
	Reason    string `json:"reason"`
}

type ReconciliationWorkflowInput struct {
	RunID string `json:"run_id"`
}

// RefundWorkflow makes every external step explicit and replay-safe. Business
// state is changed only by idempotent activities; workflow code itself remains
// deterministic and performs no I/O.
func RefundWorkflow(ctx workflow.Context, input RefundWorkflowInput) error {
	stateCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    10 * time.Second,
			// State persistence is idempotent and must eventually converge after
			// an arbitrarily long database outage.
			MaximumAttempts: 0,
		},
	})
	if err := workflow.ExecuteActivity(stateCtx, MarkRefundProcessingActivity, input).Get(stateCtx, nil); err != nil {
		return err
	}

	providerCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Second,
		HeartbeatTimeout:    5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    500 * time.Millisecond,
			BackoffCoefficient: 2,
			MaximumInterval:    5 * time.Second,
			MaximumAttempts:    4,
		},
	})
	var providerReference string
	if err := workflow.ExecuteActivity(providerCtx, ExecuteProviderRefundActivity, input).Get(providerCtx, &providerReference); err != nil {
		failCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 20 * time.Second,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 0},
		})
		_ = workflow.ExecuteActivity(failCtx, FailRefundActivity, FailInput{
			ID: input.RefundID, RequestID: input.RequestID, Reason: err.Error(),
		}).Get(failCtx, nil)
		return err
	}

	return workflow.ExecuteActivity(stateCtx, CompleteRefundActivity, CompleteRefundInput{
		RefundID: input.RefundID, RequestID: input.RequestID, ProviderReference: providerReference,
	}).Get(stateCtx, nil)
}

func ReconciliationWorkflow(ctx workflow.Context, input ReconciliationWorkflowInput) error {
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    5,
		},
	})
	if err := workflow.ExecuteActivity(activityCtx, ExecuteReconciliationActivity, input).Get(activityCtx, nil); err != nil {
		failCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 20 * time.Second,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 0},
		})
		_ = workflow.ExecuteActivity(failCtx, FailReconciliationActivity, FailInput{
			ID: input.RunID, Reason: err.Error(),
		}).Get(failCtx, nil)
		return err
	}
	return nil
}
