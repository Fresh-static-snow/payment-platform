// Package worker contains the payment-created event processor. Kafka delivery,
// database persistence, risk assessment, and provider calls are represented by
// narrow interfaces so the state machine can be tested without infrastructure.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"

	"github.com/praedyth/payment-platform/internal/events"
	paymentdomain "github.com/praedyth/payment-platform/internal/payment/domain"
	"github.com/praedyth/payment-platform/internal/provider"
	"github.com/praedyth/payment-platform/internal/requestctx"
	"github.com/praedyth/payment-platform/internal/resilience"
	riskdomain "github.com/praedyth/payment-platform/internal/risk/domain"
	"github.com/praedyth/payment-platform/pkg/logging"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
)

const ConsumerName = "payment-worker-v1"

var ErrInvalidConfiguration = errors.New("invalid payment worker configuration")

type PaymentRepository interface {
	Get(context.Context, uuid.UUID) (paymentdomain.Payment, error)
	Transition(
		ctx context.Context,
		id uuid.UUID,
		expectedVersion int64,
		to paymentdomain.Status,
		providerReference string,
		failureReason string,
		requestID string,
		causationID string,
	) (paymentdomain.Payment, error)
}

type Inbox interface {
	Processed(context.Context, string, uuid.UUID) (bool, error)
	MarkProcessed(context.Context, string, uuid.UUID) error
}

type RiskAssessor interface {
	Assess(context.Context, riskdomain.Payment) (riskdomain.Assessment, error)
}

type PaymentProvider interface {
	Process(context.Context, provider.Payment) (provider.Result, error)
}

type Retrier interface {
	Do(context.Context, func(context.Context) error) error
}

type Breaker interface {
	Execute(context.Context, func(context.Context) error) error
}

type Config struct {
	ProviderTimeout time.Duration
	Logger          *slog.Logger
	Metrics         *platformmetrics.Metrics
}

type Processor struct {
	repository      PaymentRepository
	inbox           Inbox
	risk            RiskAssessor
	provider        PaymentProvider
	retrier         Retrier
	breaker         Breaker
	providerTimeout time.Duration
	logger          *slog.Logger
	metrics         *platformmetrics.Metrics
}

func NewProcessor(
	repository PaymentRepository,
	inbox Inbox,
	risk RiskAssessor,
	paymentProvider PaymentProvider,
	retrier Retrier,
	breaker Breaker,
	config Config,
) (*Processor, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: payment repository is required", ErrInvalidConfiguration)
	}
	if inbox == nil {
		return nil, fmt.Errorf("%w: inbox is required", ErrInvalidConfiguration)
	}
	if risk == nil {
		return nil, fmt.Errorf("%w: risk assessor is required", ErrInvalidConfiguration)
	}
	if paymentProvider == nil {
		return nil, fmt.Errorf("%w: payment provider is required", ErrInvalidConfiguration)
	}
	if retrier == nil {
		return nil, fmt.Errorf("%w: retrier is required", ErrInvalidConfiguration)
	}
	if breaker == nil {
		return nil, fmt.Errorf("%w: circuit breaker is required", ErrInvalidConfiguration)
	}
	if config.ProviderTimeout <= 0 {
		return nil, fmt.Errorf("%w: provider timeout must be positive", ErrInvalidConfiguration)
	}
	return &Processor{
		repository:      repository,
		inbox:           inbox,
		risk:            risk,
		provider:        paymentProvider,
		retrier:         retrier,
		breaker:         breaker,
		providerTimeout: config.ProviderTimeout,
		logger:          config.Logger,
		metrics:         config.Metrics,
	}, nil
}

// DefaultRetryPolicy produces four bounded backoff waits: 100, 200, 400, and
// 800 milliseconds. Five total attempts are made because the initial call does
// not follow a backoff wait.
func DefaultRetryPolicy() resilience.RetryPolicy {
	return resilience.RetryPolicy{
		MaxAttempts:    5,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     800 * time.Millisecond,
		Multiplier:     2,
	}
}

func DefaultCircuitBreakerConfig() resilience.CircuitBreakerConfig {
	return resilience.CircuitBreakerConfig{
		FailureThreshold:    3,
		OpenTimeout:         5 * time.Second,
		HalfOpenMaxRequests: 1,
		SuccessThreshold:    1,
	}
}

// Handle adapts Processor to kafkax.Handler.
func (p *Processor) Handle(ctx context.Context, message kafka.Message) error {
	var envelope events.Envelope
	if err := json.Unmarshal(message.Value, &envelope); err != nil {
		return fmt.Errorf("decode payment worker event: %w", err)
	}
	return p.Process(ctx, envelope)
}

func (p *Processor) Process(ctx context.Context, envelope events.Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if envelope.ID == uuid.Nil {
		return fmt.Errorf("payment worker event ID is required")
	}
	if envelope.Type != events.PaymentCreated {
		return fmt.Errorf("payment worker received unexpected event type %q", envelope.Type)
	}
	if requestctx.RequestID(ctx) == "" && envelope.CorrelationID != "" {
		ctx = requestctx.WithRequestID(ctx, envelope.CorrelationID)
	}
	processed, err := p.inbox.Processed(ctx, ConsumerName, envelope.ID)
	if err != nil {
		return fmt.Errorf("check payment worker inbox: %w", err)
	}
	if processed {
		return nil
	}

	var payload events.PaymentPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("decode payment created payload: %w", err)
	}
	if payload.PaymentID == uuid.Nil {
		return fmt.Errorf("payment created payload has an invalid payment_id")
	}

	payment, err := p.repository.Get(ctx, payload.PaymentID)
	if err != nil {
		return fmt.Errorf("load payment %s: %w", payload.PaymentID, err)
	}
	if isTerminal(payment.Status) {
		return p.markProcessed(ctx, envelope, payment)
	}
	if payment.Status == paymentdomain.StatusPending {
		payment, err = p.transition(ctx, payment, paymentdomain.StatusProcessing, "", "", envelope)
		if err != nil {
			return err
		}
		if isTerminal(payment.Status) {
			return p.markProcessed(ctx, envelope, payment)
		}
	}
	if payment.Status != paymentdomain.StatusProcessing {
		return fmt.Errorf("payment %s has unsupported worker status %q", payment.ID, payment.Status)
	}

	assessment, err := p.risk.Assess(ctx, riskdomain.Payment{
		ID:       payment.ID,
		UserID:   payment.UserID,
		Amount:   payment.Amount,
		Currency: payment.Currency,
	})
	if err != nil {
		return fmt.Errorf("assess risk for payment %s: %w", payment.ID, err)
	}
	if assessment.PaymentID != payment.ID {
		return fmt.Errorf("risk assessment payment mismatch: got %s, want %s", assessment.PaymentID, payment.ID)
	}

	switch assessment.Decision {
	case riskdomain.DecisionDeny:
		reason := "risk denied"
		if len(assessment.Reasons) > 0 {
			reason += ": " + strings.Join(assessment.Reasons, ",")
		}
		payment, err = p.transition(ctx, payment, paymentdomain.StatusFailed, "", truncate(reason, 500), envelope)
	case riskdomain.DecisionAllow:
		payment, err = p.processAllowed(ctx, payment, envelope)
	default:
		return fmt.Errorf("risk assessment for payment %s has invalid decision %q", payment.ID, assessment.Decision)
	}
	if err != nil {
		return err
	}
	if !isTerminal(payment.Status) {
		return fmt.Errorf("payment %s remained non-terminal after processing: %s", payment.ID, payment.Status)
	}
	return p.markProcessed(ctx, envelope, payment)
}

func (p *Processor) processAllowed(
	ctx context.Context,
	payment paymentdomain.Payment,
	envelope events.Envelope,
) (paymentdomain.Payment, error) {
	request := provider.Payment{
		ID:       payment.ID.String(),
		Amount:   payment.Amount,
		Currency: payment.Currency,
	}
	var result provider.Result
	err := p.retrier.Do(ctx, func(retryCtx context.Context) error {
		attemptCtx, cancel := context.WithTimeout(retryCtx, p.providerTimeout)
		defer cancel()
		started := time.Now()
		err := p.breaker.Execute(attemptCtx, func(providerCtx context.Context) error {
			providerResult, err := p.provider.Process(providerCtx, request)
			if err != nil {
				return err
			}
			if strings.TrimSpace(providerResult.Reference) == "" {
				return fmt.Errorf("provider returned an empty reference")
			}
			result = providerResult
			return nil
		})
		if p.metrics != nil {
			p.metrics.ProviderDuration.Observe(time.Since(started).Seconds())
		}
		return err
	})
	if err == nil {
		return p.transition(ctx, payment, paymentdomain.StatusCompleted, result.Reference, "", envelope)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return paymentdomain.Payment{}, contextErr
	}
	if !errors.Is(err, resilience.ErrRetriesExhausted) {
		return paymentdomain.Payment{}, fmt.Errorf("process payment %s with provider: %w", payment.ID, err)
	}
	return p.transition(
		ctx,
		payment,
		paymentdomain.StatusFailed,
		"",
		truncate("provider retries exhausted: "+err.Error(), 500),
		envelope,
	)
}

// transition treats a concurrently reached terminal state as successful
// recovery. This is what makes a replay after "state committed, inbox not yet
// marked" safe. A concurrent processing transition is also safe to continue.
func (p *Processor) transition(
	ctx context.Context,
	payment paymentdomain.Payment,
	to paymentdomain.Status,
	providerReference string,
	failureReason string,
	envelope events.Envelope,
) (paymentdomain.Payment, error) {
	updated, err := p.repository.Transition(
		ctx,
		payment.ID,
		payment.Version,
		to,
		providerReference,
		failureReason,
		requestctx.RequestID(ctx),
		envelope.ID.String(),
	)
	if err == nil {
		return updated, nil
	}
	if !errors.Is(err, paymentdomain.ErrConcurrentModification) {
		return paymentdomain.Payment{}, fmt.Errorf("transition payment %s to %s: %w", payment.ID, to, err)
	}

	latest, loadErr := p.repository.Get(ctx, payment.ID)
	if loadErr != nil {
		return paymentdomain.Payment{}, fmt.Errorf("reload concurrently modified payment %s: %w", payment.ID, loadErr)
	}
	if isTerminal(latest.Status) || (to == paymentdomain.StatusProcessing && latest.Status == paymentdomain.StatusProcessing) {
		return latest, nil
	}
	return paymentdomain.Payment{}, fmt.Errorf(
		"transition payment %s to %s after concurrent modification: current status %s: %w",
		payment.ID,
		to,
		latest.Status,
		paymentdomain.ErrConcurrentModification,
	)
}

func (p *Processor) markProcessed(ctx context.Context, envelope events.Envelope, payment paymentdomain.Payment) error {
	if !isTerminal(payment.Status) {
		return fmt.Errorf("refuse to mark non-terminal payment %s (%s) as processed", payment.ID, payment.Status)
	}
	if err := p.inbox.MarkProcessed(ctx, ConsumerName, envelope.ID); err != nil {
		return fmt.Errorf("mark payment worker inbox: %w", err)
	}
	if p.metrics != nil {
		switch payment.Status {
		case paymentdomain.StatusCompleted:
			p.metrics.PaymentsCompleted.Inc()
		case paymentdomain.StatusFailed:
			p.metrics.PaymentsFailed.Inc()
		}
	}
	if p.logger != nil {
		logging.WithContext(ctx, p.logger).Info(
			"payment event processed",
			"payment_id", payment.ID,
			"event_id", envelope.ID,
			"event_type", envelope.Type,
			"status", payment.Status,
		)
	}
	return nil
}

func isTerminal(status paymentdomain.Status) bool {
	switch status {
	case paymentdomain.StatusCompleted, paymentdomain.StatusFailed, paymentdomain.StatusCancelled:
		return true
	default:
		return false
	}
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
