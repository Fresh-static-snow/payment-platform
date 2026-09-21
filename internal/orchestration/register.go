package orchestration

import (
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func Register(w worker.Worker, activities *Activities) {
	w.RegisterWorkflowWithOptions(RefundWorkflow, workflow.RegisterOptions{Name: RefundWorkflowName})
	w.RegisterWorkflowWithOptions(ReconciliationWorkflow, workflow.RegisterOptions{Name: ReconciliationWorkflowName})
	w.RegisterActivityWithOptions(activities.MarkRefundProcessing, activity.RegisterOptions{Name: MarkRefundProcessingActivity})
	w.RegisterActivityWithOptions(activities.ExecuteProviderRefund, activity.RegisterOptions{Name: ExecuteProviderRefundActivity})
	w.RegisterActivityWithOptions(activities.CompleteRefund, activity.RegisterOptions{Name: CompleteRefundActivity})
	w.RegisterActivityWithOptions(activities.FailRefund, activity.RegisterOptions{Name: FailRefundActivity})
	w.RegisterActivityWithOptions(activities.ExecuteReconciliation, activity.RegisterOptions{Name: ExecuteReconciliationActivity})
	w.RegisterActivityWithOptions(activities.FailReconciliation, activity.RegisterOptions{Name: FailReconciliationActivity})
}
