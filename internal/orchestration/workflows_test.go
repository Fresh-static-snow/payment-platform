package orchestration

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestRefundWorkflowCompletes(t *testing.T) {
	t.Parallel()
	env := new(testsuite.WorkflowTestSuite).NewTestWorkflowEnvironment()
	input := RefundWorkflowInput{RefundID: "7e5f222a-f10f-4efe-9228-fca17d381c5e", RequestID: "request-1"}
	registerRefundTestActivities(env)

	env.OnActivity(MarkRefundProcessingActivity, mock.Anything, input).Return(nil).Once()
	env.OnActivity(ExecuteProviderRefundActivity, mock.Anything, input).Return("refund_provider_1", nil).Once()
	env.OnActivity(CompleteRefundActivity, mock.Anything, CompleteRefundInput{
		RefundID: input.RefundID, RequestID: input.RequestID, ProviderReference: "refund_provider_1",
	}).Return(nil).Once()

	env.ExecuteWorkflow(RefundWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestRefundWorkflowRecordsTerminalProviderFailure(t *testing.T) {
	t.Parallel()
	env := new(testsuite.WorkflowTestSuite).NewTestWorkflowEnvironment()
	input := RefundWorkflowInput{RefundID: "c311922d-4acc-4147-a4cf-a14589d22c74", RequestID: "request-2"}
	providerError := errors.New("provider rejected refund")
	registerRefundTestActivities(env)

	env.OnActivity(MarkRefundProcessingActivity, mock.Anything, input).Return(nil).Once()
	env.OnActivity(ExecuteProviderRefundActivity, mock.Anything, input).Return("", providerError).Times(4)
	env.OnActivity(FailRefundActivity, mock.Anything, mock.MatchedBy(func(value FailInput) bool {
		return value.ID == input.RefundID && value.RequestID == input.RequestID && value.Reason != ""
	})).Return(nil).Once()

	env.ExecuteWorkflow(RefundWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestReconciliationWorkflowCompletes(t *testing.T) {
	t.Parallel()
	env := new(testsuite.WorkflowTestSuite).NewTestWorkflowEnvironment()
	input := ReconciliationWorkflowInput{RunID: "b54c83a7-41c1-4c03-82f8-80e770e06c49"}
	activities := &Activities{}
	env.RegisterActivityWithOptions(activities.ExecuteReconciliation, activity.RegisterOptions{Name: ExecuteReconciliationActivity})

	env.OnActivity(ExecuteReconciliationActivity, mock.Anything, input).Return(nil).Once()
	env.ExecuteWorkflow(ReconciliationWorkflow, input)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func registerRefundTestActivities(env *testsuite.TestWorkflowEnvironment) {
	activities := &Activities{}
	env.RegisterActivityWithOptions(activities.MarkRefundProcessing, activity.RegisterOptions{Name: MarkRefundProcessingActivity})
	env.RegisterActivityWithOptions(activities.ExecuteProviderRefund, activity.RegisterOptions{Name: ExecuteProviderRefundActivity})
	env.RegisterActivityWithOptions(activities.CompleteRefund, activity.RegisterOptions{Name: CompleteRefundActivity})
	env.RegisterActivityWithOptions(activities.FailRefund, activity.RegisterOptions{Name: FailRefundActivity})
}
