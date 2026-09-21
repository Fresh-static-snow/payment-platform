package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"

	"github.com/praedyth/payment-platform/internal/events"
	paymentdomain "github.com/praedyth/payment-platform/internal/payment/domain"
	"github.com/praedyth/payment-platform/internal/provider"
	"github.com/praedyth/payment-platform/internal/requestctx"
	"github.com/praedyth/payment-platform/internal/resilience"
	riskdomain "github.com/praedyth/payment-platform/internal/risk/domain"
)

type transitionCall struct {
	to                paymentdomain.Status
	providerReference string
	failureReason     string
	requestID         string
	causationID       string
}

type fakePaymentRepository struct {
	payment        paymentdomain.Payment
	getCalls       int
	transitions    []transitionCall
	transitionHook func(*fakePaymentRepository, paymentdomain.Status) (paymentdomain.Payment, error)
}

func (repository *fakePaymentRepository) Get(context.Context, uuid.UUID) (paymentdomain.Payment, error) {
	repository.getCalls++
	return repository.payment, nil
}

func (repository *fakePaymentRepository) Transition(
	_ context.Context,
	_ uuid.UUID,
	expectedVersion int64,
	to paymentdomain.Status,
	providerReference string,
	failureReason string,
	requestID string,
	causationID string,
) (paymentdomain.Payment, error) {
	repository.transitions = append(repository.transitions, transitionCall{
		to: to, providerReference: providerReference, failureReason: failureReason,
		requestID: requestID, causationID: causationID,
	})
	if repository.transitionHook != nil {
		return repository.transitionHook(repository, to)
	}
	if repository.payment.Version != expectedVersion {
		return paymentdomain.Payment{}, paymentdomain.ErrConcurrentModification
	}
	updated := repository.payment
	if err := updated.Transition(to); err != nil {
		return paymentdomain.Payment{}, err
	}
	updated.ProviderReference = providerReference
	updated.FailureReason = failureReason
	repository.payment = updated
	return updated, nil
}

type fakeInbox struct {
	processed      bool
	processedErr   error
	markErr        error
	processedCalls int
	markCalls      int
}

func (inbox *fakeInbox) Processed(context.Context, string, uuid.UUID) (bool, error) {
	inbox.processedCalls++
	return inbox.processed, inbox.processedErr
}

func (inbox *fakeInbox) MarkProcessed(context.Context, string, uuid.UUID) error {
	inbox.markCalls++
	if inbox.markErr == nil {
		inbox.processed = true
	}
	return inbox.markErr
}

type fakeRiskAssessor struct {
	assessment riskdomain.Assessment
	err        error
	calls      int
}

func (risk *fakeRiskAssessor) Assess(_ context.Context, payment riskdomain.Payment) (riskdomain.Assessment, error) {
	risk.calls++
	assessment := risk.assessment
	if assessment.PaymentID == uuid.Nil {
		assessment.PaymentID = payment.ID
	}
	return assessment, risk.err
}

type fakePaymentProvider struct {
	process func(context.Context, provider.Payment) (provider.Result, error)
	calls   int
}

func (paymentProvider *fakePaymentProvider) Process(ctx context.Context, payment provider.Payment) (provider.Result, error) {
	paymentProvider.calls++
	if paymentProvider.process != nil {
		return paymentProvider.process(ctx, payment)
	}
	return provider.Result{Reference: "provider-reference"}, nil
}

type directBreaker struct {
	calls int
}

func (breaker *directBreaker) Execute(ctx context.Context, operation func(context.Context) error) error {
	breaker.calls++
	return operation(ctx)
}

func TestProcessorRiskDenyDoesNotInvokeProvider(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	inbox := &fakeInbox{}
	risk := &fakeRiskAssessor{assessment: riskdomain.Assessment{
		Decision: riskdomain.DecisionDeny,
		Reasons:  []string{"amount_threshold"},
	}}
	paymentProvider := &fakePaymentProvider{}
	processor := newTestProcessor(t, repository, inbox, risk, paymentProvider)
	event := paymentCreatedEvent(t, repository.payment)

	if err := processor.Process(context.Background(), event); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if paymentProvider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", paymentProvider.calls)
	}
	assertTransitionStatuses(t, repository.transitions, paymentdomain.StatusProcessing, paymentdomain.StatusFailed)
	if !strings.Contains(repository.transitions[1].failureReason, "risk denied") {
		t.Fatalf("failure reason = %q, want risk denial", repository.transitions[1].failureReason)
	}
	if inbox.markCalls != 1 || repository.payment.Status != paymentdomain.StatusFailed {
		t.Fatalf("mark calls = %d, payment status = %s", inbox.markCalls, repository.payment.Status)
	}
}

func TestProcessorAlreadyClaimedDuplicateIsNoOp(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	inbox := &fakeInbox{processed: true}
	risk := &fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionAllow}}
	paymentProvider := &fakePaymentProvider{}
	processor := newTestProcessor(t, repository, inbox, risk, paymentProvider)

	if err := processor.Process(context.Background(), paymentCreatedEvent(t, repository.payment)); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if repository.getCalls != 0 || len(repository.transitions) != 0 || risk.calls != 0 || paymentProvider.calls != 0 || inbox.markCalls != 0 {
		t.Fatalf(
			"duplicate caused work: gets=%d transitions=%d risk=%d provider=%d marks=%d",
			repository.getCalls, len(repository.transitions), risk.calls, paymentProvider.calls, inbox.markCalls,
		)
	}
}

func TestProcessorTerminalReplayMarksInboxWithoutSideEffects(t *testing.T) {
	for _, status := range []paymentdomain.Status{
		paymentdomain.StatusCompleted,
		paymentdomain.StatusFailed,
		paymentdomain.StatusCancelled,
	} {
		t.Run(string(status), func(t *testing.T) {
			payment := pendingPayment()
			payment.Status = status
			payment.Version = 4
			repository := &fakePaymentRepository{payment: payment}
			inbox := &fakeInbox{}
			risk := &fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionAllow}}
			paymentProvider := &fakePaymentProvider{}
			processor := newTestProcessor(t, repository, inbox, risk, paymentProvider)

			if err := processor.Process(context.Background(), paymentCreatedEvent(t, payment)); err != nil {
				t.Fatalf("Process: %v", err)
			}
			if len(repository.transitions) != 0 || risk.calls != 0 || paymentProvider.calls != 0 || inbox.markCalls != 1 {
				t.Fatalf(
					"terminal replay side effects: transitions=%d risk=%d provider=%d marks=%d",
					len(repository.transitions), risk.calls, paymentProvider.calls, inbox.markCalls,
				)
			}
		})
	}
}

func TestProcessorAllowedPaymentCompletes(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	inbox := &fakeInbox{}
	risk := &fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionAllow}}
	paymentProvider := &fakePaymentProvider{process: func(ctx context.Context, payment provider.Payment) (provider.Result, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("provider attempt has no deadline")
		}
		if payment.ID != repository.payment.ID.String() || payment.Amount != 1000 || payment.Currency != "USD" {
			t.Fatalf("provider payment = %+v", payment)
		}
		return provider.Result{Reference: "approved-123"}, nil
	}}
	processor := newTestProcessor(t, repository, inbox, risk, paymentProvider)
	event := paymentCreatedEvent(t, repository.payment)
	ctx := requestctx.WithRequestID(context.Background(), "request-from-kafka-header")

	if err := processor.Process(ctx, event); err != nil {
		t.Fatalf("Process: %v", err)
	}
	assertTransitionStatuses(t, repository.transitions, paymentdomain.StatusProcessing, paymentdomain.StatusCompleted)
	if repository.payment.ProviderReference != "approved-123" || paymentProvider.calls != 1 || inbox.markCalls != 1 {
		t.Fatalf("payment = %+v, provider calls = %d, marks = %d", repository.payment, paymentProvider.calls, inbox.markCalls)
	}
	for _, transition := range repository.transitions {
		if transition.requestID != "request-from-kafka-header" || transition.causationID != event.ID.String() {
			t.Fatalf("transition metadata = request %q causation %q", transition.requestID, transition.causationID)
		}
	}
}

func TestProcessorUsesEnvelopeCorrelationWhenContextHasNoRequestID(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	processor := newTestProcessor(
		t,
		repository,
		&fakeInbox{},
		&fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionDeny}},
		&fakePaymentProvider{},
	)
	event := paymentCreatedEvent(t, repository.payment)
	event.CorrelationID = "request-from-envelope"
	if err := processor.Process(context.Background(), event); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if repository.transitions[0].requestID != "request-from-envelope" {
		t.Fatalf("request ID = %q, want envelope correlation", repository.transitions[0].requestID)
	}
}

func TestProcessorProviderRetriesExhaustedMarksFailed(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	inbox := &fakeInbox{}
	paymentProvider := &fakePaymentProvider{process: func(context.Context, provider.Payment) (provider.Result, error) {
		return provider.Result{}, provider.ErrProviderFailure
	}}
	processor := newTestProcessor(
		t,
		repository,
		inbox,
		&fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionAllow}},
		paymentProvider,
	)

	if err := processor.Process(context.Background(), paymentCreatedEvent(t, repository.payment)); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if paymentProvider.calls != 5 {
		t.Fatalf("provider calls = %d, want 5", paymentProvider.calls)
	}
	assertTransitionStatuses(t, repository.transitions, paymentdomain.StatusProcessing, paymentdomain.StatusFailed)
	if !strings.Contains(repository.payment.FailureReason, "provider retries exhausted") || inbox.markCalls != 1 {
		t.Fatalf("failure reason = %q, marks = %d", repository.payment.FailureReason, inbox.markCalls)
	}
}

func TestProcessorRiskErrorLeavesPaymentForKafkaRetry(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	inbox := &fakeInbox{}
	riskErr := errors.New("risk unavailable")
	paymentProvider := &fakePaymentProvider{}
	processor := newTestProcessor(
		t,
		repository,
		inbox,
		&fakeRiskAssessor{err: riskErr},
		paymentProvider,
	)

	err := processor.Process(context.Background(), paymentCreatedEvent(t, repository.payment))
	if !errors.Is(err, riskErr) {
		t.Fatalf("Process error = %v, want risk error", err)
	}
	assertTransitionStatuses(t, repository.transitions, paymentdomain.StatusProcessing)
	if paymentProvider.calls != 0 || inbox.markCalls != 0 || repository.payment.Status != paymentdomain.StatusProcessing {
		t.Fatalf("provider calls = %d, marks = %d, status = %s", paymentProvider.calls, inbox.markCalls, repository.payment.Status)
	}
}

func TestProcessorConcurrentTransitionToTerminalIsReplaySafe(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	repository.transitionHook = func(repository *fakePaymentRepository, to paymentdomain.Status) (paymentdomain.Payment, error) {
		if to != paymentdomain.StatusProcessing {
			t.Fatalf("transition = %s, want processing", to)
		}
		repository.payment.Status = paymentdomain.StatusCompleted
		repository.payment.Version++
		return paymentdomain.Payment{}, paymentdomain.ErrConcurrentModification
	}
	inbox := &fakeInbox{}
	risk := &fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionAllow}}
	paymentProvider := &fakePaymentProvider{}
	processor := newTestProcessor(t, repository, inbox, risk, paymentProvider)

	if err := processor.Process(context.Background(), paymentCreatedEvent(t, repository.payment)); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if risk.calls != 0 || paymentProvider.calls != 0 || inbox.markCalls != 1 {
		t.Fatalf("risk calls = %d, provider calls = %d, marks = %d", risk.calls, paymentProvider.calls, inbox.markCalls)
	}
}

func TestProcessorInboxFailureIsRecoveredByTerminalReplay(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	inboxErr := errors.New("inbox unavailable")
	inbox := &fakeInbox{markErr: inboxErr}
	risk := &fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionAllow}}
	paymentProvider := &fakePaymentProvider{}
	processor := newTestProcessor(t, repository, inbox, risk, paymentProvider)
	event := paymentCreatedEvent(t, repository.payment)

	if err := processor.Process(context.Background(), event); !errors.Is(err, inboxErr) {
		t.Fatalf("first Process error = %v, want inbox error", err)
	}
	if repository.payment.Status != paymentdomain.StatusCompleted {
		t.Fatalf("status = %s, want completed", repository.payment.Status)
	}
	firstProviderCalls := paymentProvider.calls
	inbox.markErr = nil
	if err := processor.Process(context.Background(), event); err != nil {
		t.Fatalf("replay Process: %v", err)
	}
	if paymentProvider.calls != firstProviderCalls || inbox.markCalls != 2 {
		t.Fatalf("provider calls = %d, marks = %d", paymentProvider.calls, inbox.markCalls)
	}
}

func TestProcessorRejectsMismatchedRiskAssessment(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	paymentProvider := &fakePaymentProvider{}
	processor := newTestProcessor(
		t,
		repository,
		&fakeInbox{},
		&fakeRiskAssessor{assessment: riskdomain.Assessment{PaymentID: uuid.New(), Decision: riskdomain.DecisionAllow}},
		paymentProvider,
	)
	err := processor.Process(context.Background(), paymentCreatedEvent(t, repository.payment))
	if err == nil || !strings.Contains(err.Error(), "payment mismatch") {
		t.Fatalf("Process error = %v, want payment mismatch", err)
	}
	if paymentProvider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", paymentProvider.calls)
	}
}

func TestProcessorHandleDecodesKafkaMessage(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	processor := newTestProcessor(
		t,
		repository,
		&fakeInbox{},
		&fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionDeny}},
		&fakePaymentProvider{},
	)
	event := paymentCreatedEvent(t, repository.payment)
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := processor.Handle(context.Background(), kafka.Message{Value: raw}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if err := processor.Handle(context.Background(), kafka.Message{Value: []byte("{")}); err == nil {
		t.Fatal("expected malformed Kafka message error")
	}
}

func TestProcessorValidatesEvents(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	processor := newTestProcessor(
		t,
		repository,
		&fakeInbox{},
		&fakeRiskAssessor{assessment: riskdomain.Assessment{Decision: riskdomain.DecisionAllow}},
		&fakePaymentProvider{},
	)
	event := paymentCreatedEvent(t, repository.payment)
	tests := []struct {
		name   string
		mutate func(*events.Envelope)
	}{
		{name: "missing event id", mutate: func(event *events.Envelope) { event.ID = uuid.Nil }},
		{name: "wrong type", mutate: func(event *events.Envelope) { event.Type = events.PaymentCompleted }},
		{name: "bad payload", mutate: func(event *events.Envelope) { event.Payload = []byte("{") }},
		{name: "missing payment id", mutate: func(event *events.Envelope) { event.Payload = []byte(`{}`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := event
			test.mutate(&candidate)
			if err := processor.Process(context.Background(), candidate); err == nil {
				t.Fatal("expected event validation error")
			}
		})
	}
}

func TestProcessorConfigurationValidation(t *testing.T) {
	repository := &fakePaymentRepository{payment: pendingPayment()}
	inbox := &fakeInbox{}
	risk := &fakeRiskAssessor{}
	paymentProvider := &fakePaymentProvider{}
	retrier := newNoWaitRetrier(t)
	breaker := &directBreaker{}
	config := Config{ProviderTimeout: time.Second}

	tests := []struct {
		name string
		new  func() (*Processor, error)
	}{
		{name: "repository", new: func() (*Processor, error) {
			return NewProcessor(nil, inbox, risk, paymentProvider, retrier, breaker, config)
		}},
		{name: "inbox", new: func() (*Processor, error) {
			return NewProcessor(repository, nil, risk, paymentProvider, retrier, breaker, config)
		}},
		{name: "risk", new: func() (*Processor, error) {
			return NewProcessor(repository, inbox, nil, paymentProvider, retrier, breaker, config)
		}},
		{name: "provider", new: func() (*Processor, error) {
			return NewProcessor(repository, inbox, risk, nil, retrier, breaker, config)
		}},
		{name: "retrier", new: func() (*Processor, error) {
			return NewProcessor(repository, inbox, risk, paymentProvider, nil, breaker, config)
		}},
		{name: "breaker", new: func() (*Processor, error) {
			return NewProcessor(repository, inbox, risk, paymentProvider, retrier, nil, config)
		}},
		{name: "timeout", new: func() (*Processor, error) {
			return NewProcessor(repository, inbox, risk, paymentProvider, retrier, breaker, Config{})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.new(); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}

func newTestProcessor(
	t *testing.T,
	repository PaymentRepository,
	inbox Inbox,
	risk RiskAssessor,
	paymentProvider PaymentProvider,
) *Processor {
	t.Helper()
	processor, err := NewProcessor(
		repository,
		inbox,
		risk,
		paymentProvider,
		newNoWaitRetrier(t),
		&directBreaker{},
		Config{ProviderTimeout: time.Second},
	)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	return processor
}

func newNoWaitRetrier(t *testing.T) *resilience.Retrier {
	t.Helper()
	retrier, err := resilience.NewRetrier(
		DefaultRetryPolicy(),
		resilience.WithRetryWaitFunc(func(context.Context, time.Duration) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewRetrier: %v", err)
	}
	return retrier
}

func pendingPayment() paymentdomain.Payment {
	return paymentdomain.Payment{
		ID:             uuid.New(),
		IdempotencyKey: "key",
		UserID:         uuid.New(),
		Amount:         1000,
		Currency:       "USD",
		Status:         paymentdomain.StatusPending,
		Version:        1,
	}
}

func paymentCreatedEvent(t *testing.T, payment paymentdomain.Payment) events.Envelope {
	t.Helper()
	payload, err := json.Marshal(events.PaymentPayload{
		PaymentID: payment.ID,
		UserID:    payment.UserID,
		Amount:    payment.Amount,
		Currency:  payment.Currency,
		Status:    string(paymentdomain.StatusPending),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return events.Envelope{
		ID:            uuid.New(),
		Type:          events.PaymentCreated,
		Version:       1,
		OccurredAt:    time.Now().UTC(),
		CorrelationID: "request-123",
		Payload:       payload,
	}
}

func assertTransitionStatuses(t *testing.T, calls []transitionCall, want ...paymentdomain.Status) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("transition count = %d, want %d: %+v", len(calls), len(want), calls)
	}
	for index, status := range want {
		if calls[index].to != status {
			t.Fatalf("transition %d = %s, want %s", index, calls[index].to, status)
		}
	}
}
