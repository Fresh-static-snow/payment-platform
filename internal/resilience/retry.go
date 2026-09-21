package resilience

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

var ErrRetriesExhausted = errors.New("retry attempts exhausted")

type RetryableFunc func(error) bool
type RetryWaitFunc func(context.Context, time.Duration) error
type RetryHook func(failedAttempt int, err error, nextDelay time.Duration)

// RetryPolicy counts the initial call as one attempt. InitialBackoff is applied
// after the first failed attempt, then multiplied until MaxBackoff is reached.
type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64
	Retryable      RetryableFunc
	OnRetry        RetryHook
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:    4,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     800 * time.Millisecond,
		Multiplier:     2,
	}
}

type RetryOption func(*Retrier) error

// WithRetryWaitFunc is primarily a test seam, but it also permits integration
// with a scheduler without changing retry semantics.
func WithRetryWaitFunc(wait RetryWaitFunc) RetryOption {
	return func(retrier *Retrier) error {
		if wait == nil {
			return fmt.Errorf("retry wait function must not be nil")
		}
		retrier.wait = wait
		return nil
	}
}

type Retrier struct {
	policy RetryPolicy
	wait   RetryWaitFunc
}

func NewRetrier(policy RetryPolicy, options ...RetryOption) (*Retrier, error) {
	normalized, err := normalizeRetryPolicy(policy)
	if err != nil {
		return nil, err
	}
	retrier := &Retrier{policy: normalized, wait: waitForContext}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("retry option must not be nil")
		}
		if err := option(retrier); err != nil {
			return nil, err
		}
	}
	return retrier, nil
}

// Retry is the convenient stateless entry point. Construct a Retrier directly
// when a custom waiter is needed for deterministic testing.
func Retry(ctx context.Context, policy RetryPolicy, operation func(context.Context) error) error {
	retrier, err := NewRetrier(policy)
	if err != nil {
		return err
	}
	return retrier.Do(ctx, operation)
}

func (r *Retrier) Do(ctx context.Context, operation func(context.Context) error) error {
	if operation == nil {
		return fmt.Errorf("retry operation must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	delay := r.policy.InitialBackoff
	var lastErr error
	for attempt := 1; attempt <= r.policy.MaxAttempts; attempt++ {
		err := operation(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if !r.policy.Retryable(err) {
			return err
		}
		if attempt == r.policy.MaxAttempts {
			return &RetryError{Attempts: attempt, Err: err}
		}

		if r.policy.OnRetry != nil {
			r.policy.OnRetry(attempt, err, delay)
		}
		if err := r.wait(ctx, delay); err != nil {
			return fmt.Errorf("retry backoff after attempt %d: %w", attempt, err)
		}
		delay = nextBackoff(delay, r.policy.Multiplier, r.policy.MaxBackoff)
	}
	// Policy validation makes this path unreachable, but returning a wrapped
	// error keeps an invariant bug from turning into a process-wide panic.
	return &RetryError{Attempts: r.policy.MaxAttempts, Err: lastErr}
}

type RetryError struct {
	Attempts int
	Err      error
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("%v after %d attempts: %v", ErrRetriesExhausted, e.Attempts, e.Err)
}

func (e *RetryError) Unwrap() error { return e.Err }

func (e *RetryError) Is(target error) bool { return target == ErrRetriesExhausted }

func normalizeRetryPolicy(policy RetryPolicy) (RetryPolicy, error) {
	if policy.MaxAttempts < 1 {
		return RetryPolicy{}, fmt.Errorf("retry max attempts must be at least 1")
	}
	if policy.InitialBackoff < 0 {
		return RetryPolicy{}, fmt.Errorf("retry initial backoff must not be negative")
	}
	if policy.MaxBackoff < 0 {
		return RetryPolicy{}, fmt.Errorf("retry max backoff must not be negative")
	}
	if policy.MaxBackoff > 0 && policy.InitialBackoff > policy.MaxBackoff {
		return RetryPolicy{}, fmt.Errorf("retry initial backoff must not exceed max backoff")
	}
	if policy.Multiplier == 0 {
		policy.Multiplier = 2
	}
	if math.IsNaN(policy.Multiplier) || math.IsInf(policy.Multiplier, 0) || policy.Multiplier < 1 {
		return RetryPolicy{}, fmt.Errorf("retry multiplier must be finite and at least 1")
	}
	if policy.Retryable == nil {
		policy.Retryable = func(err error) bool {
			return !errors.Is(err, context.Canceled)
		}
	}
	return policy, nil
}

func nextBackoff(current time.Duration, multiplier float64, maximum time.Duration) time.Duration {
	if current == 0 {
		return 0
	}
	nextFloat := float64(current) * multiplier
	var next time.Duration
	if nextFloat >= float64(math.MaxInt64) {
		next = time.Duration(math.MaxInt64)
	} else {
		next = time.Duration(nextFloat)
	}
	if maximum > 0 && next > maximum {
		return maximum
	}
	return next
}

func waitForContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
