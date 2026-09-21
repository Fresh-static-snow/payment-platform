package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/praedyth/payment-platform/internal/reconciliation"
	refunddomain "github.com/praedyth/payment-platform/internal/refund/domain"
	refundrepository "github.com/praedyth/payment-platform/internal/refund/repository"
)

type Starter struct {
	client         client.Client
	refunds        *refundrepository.Store
	reconciliation *reconciliation.Repository
}

func NewStarter(
	temporalClient client.Client,
	refunds *refundrepository.Store,
	reconciliationRepository *reconciliation.Repository,
) *Starter {
	return &Starter{
		client: temporalClient, refunds: refunds, reconciliation: reconciliationRepository,
	}
}

func (s *Starter) StartRefund(ctx context.Context, refund refunddomain.Refund, requestID string) error {
	_, err := s.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    refund.WorkflowID,
		TaskQueue:             TaskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, RefundWorkflowName, RefundWorkflowInput{RefundID: refund.ID.String(), RequestID: requestID})
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if err != nil && !errors.As(err, &alreadyStarted) {
		return fmt.Errorf("start refund workflow: %w", err)
	}
	if err := s.refunds.MarkWorkflowStarted(ctx, refund.ID); err != nil {
		return err
	}
	return nil
}

func (s *Starter) StartReconciliation(ctx context.Context, run reconciliation.Run) error {
	_, err := s.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    run.WorkflowID,
		TaskQueue:             TaskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	}, ReconciliationWorkflowName, ReconciliationWorkflowInput{RunID: run.ID.String()})
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if err != nil && !errors.As(err, &alreadyStarted) {
		return fmt.Errorf("start reconciliation workflow: %w", err)
	}
	if err := s.reconciliation.MarkWorkflowStarted(ctx, run.ID); err != nil {
		return err
	}
	return nil
}

func (s *Starter) DispatchPendingRefunds(ctx context.Context, limit int) error {
	refunds, err := s.refunds.ListUnstarted(ctx, limit)
	if err != nil {
		return err
	}
	var dispatchErrors []error
	for _, refund := range refunds {
		if err := ctx.Err(); err != nil {
			dispatchErrors = append(dispatchErrors, err)
			break
		}
		if err := s.StartRefund(ctx, refund, "recovery:"+uuid.NewString()); err != nil {
			dispatchErrors = append(dispatchErrors, fmt.Errorf("refund %s: %w", refund.ID, err))
		}
	}
	return errors.Join(dispatchErrors...)
}

func (s *Starter) DispatchPendingReconciliations(ctx context.Context, limit int) error {
	runs, err := s.reconciliation.ListUnstarted(ctx, limit)
	if err != nil {
		return err
	}
	var dispatchErrors []error
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			dispatchErrors = append(dispatchErrors, err)
			break
		}
		if err := s.StartReconciliation(ctx, run); err != nil {
			dispatchErrors = append(dispatchErrors, fmt.Errorf("reconciliation %s: %w", run.ID, err))
		}
	}
	return errors.Join(dispatchErrors...)
}

func (s *Starter) Ready(ctx context.Context) error {
	_, err := s.client.CheckHealth(ctx, &client.CheckHealthRequest{})
	return err
}
