package resilience

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

var errTransient = errors.New("transient failure")

func TestRetrierSucceedsAfterExponentialBackoff(t *testing.T) {
	var waits []time.Duration
	var hookAttempts []int
	policy := RetryPolicy{
		MaxAttempts:    4,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     time.Second,
		Multiplier:     2,
		OnRetry: func(attempt int, _ error, _ time.Duration) {
			hookAttempts = append(hookAttempts, attempt)
		},
	}
	retrier, err := NewRetrier(policy, WithRetryWaitFunc(func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}))
	if err != nil {
		t.Fatalf("new retrier: %v", err)
	}

	attempts := 0
	err = retrier.Do(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errTransient
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}; !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	if want := []int{1, 2}; !reflect.DeepEqual(hookAttempts, want) {
		t.Fatalf("hook attempts = %v, want %v", hookAttempts, want)
	}
}

func TestRetrierCapsBackoff(t *testing.T) {
	var waits []time.Duration
	retrier, err := NewRetrier(RetryPolicy{
		MaxAttempts:    5,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     250 * time.Millisecond,
		Multiplier:     2,
	}, WithRetryWaitFunc(func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}))
	if err != nil {
		t.Fatalf("new retrier: %v", err)
	}
	_ = retrier.Do(context.Background(), func(context.Context) error { return errTransient })

	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
}

func TestRetrierStopsOnNonRetryableError(t *testing.T) {
	permanent := errors.New("permanent")
	retrier, err := NewRetrier(RetryPolicy{
		MaxAttempts: 3,
		Retryable:   func(err error) bool { return !errors.Is(err, permanent) },
	}, WithRetryWaitFunc(func(context.Context, time.Duration) error {
		t.Fatal("wait must not be called")
		return nil
	}))
	if err != nil {
		t.Fatalf("new retrier: %v", err)
	}
	attempts := 0
	err = retrier.Do(context.Background(), func(context.Context) error {
		attempts++
		return permanent
	})
	if !errors.Is(err, permanent) || attempts != 1 {
		t.Fatalf("error = %v, attempts = %d; want permanent after one attempt", err, attempts)
	}
}

func TestRetrierReportsExhaustionAndPreservesCause(t *testing.T) {
	retrier, err := NewRetrier(RetryPolicy{MaxAttempts: 3}, WithRetryWaitFunc(func(context.Context, time.Duration) error { return nil }))
	if err != nil {
		t.Fatalf("new retrier: %v", err)
	}
	err = retrier.Do(context.Background(), func(context.Context) error { return errTransient })
	if !errors.Is(err, ErrRetriesExhausted) {
		t.Fatalf("error = %v, want ErrRetriesExhausted", err)
	}
	if !errors.Is(err, errTransient) {
		t.Fatalf("error = %v, want original cause", err)
	}
	var retryError *RetryError
	if !errors.As(err, &retryError) || retryError.Attempts != 3 {
		t.Fatalf("RetryError = %#v, want 3 attempts", retryError)
	}
	if retryError.Error() == "" {
		t.Fatal("retry error message must not be empty")
	}
}

func TestRetrierHonoursContextBeforeFirstAttempt(t *testing.T) {
	retrier, err := NewRetrier(RetryPolicy{MaxAttempts: 2})
	if err != nil {
		t.Fatalf("new retrier: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err = retrier.Do(ctx, func(context.Context) error {
		attempts++
		return nil
	})
	if !errors.Is(err, context.Canceled) || attempts != 0 {
		t.Fatalf("error = %v, attempts = %d; want cancellation before operation", err, attempts)
	}
}

func TestRetrierHonoursCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	retrier, err := NewRetrier(RetryPolicy{MaxAttempts: 3}, WithRetryWaitFunc(func(waitCtx context.Context, _ time.Duration) error {
		cancel()
		<-waitCtx.Done()
		return waitCtx.Err()
	}))
	if err != nil {
		t.Fatalf("new retrier: %v", err)
	}
	attempts := 0
	err = retrier.Do(ctx, func(context.Context) error {
		attempts++
		return errTransient
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("error = %v, attempts = %d; want cancellation after first attempt", err, attempts)
	}
}

func TestRetrierDoesNotRetryOperationCancellation(t *testing.T) {
	retrier, err := NewRetrier(RetryPolicy{MaxAttempts: 3}, WithRetryWaitFunc(func(context.Context, time.Duration) error {
		t.Fatal("wait must not be called")
		return nil
	}))
	if err != nil {
		t.Fatalf("new retrier: %v", err)
	}
	attempts := 0
	err = retrier.Do(context.Background(), func(context.Context) error {
		attempts++
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("error = %v, attempts = %d; want one attempt", err, attempts)
	}
}

func TestRetryPolicyValidation(t *testing.T) {
	tests := []RetryPolicy{
		{MaxAttempts: 0},
		{MaxAttempts: 1, InitialBackoff: -1},
		{MaxAttempts: 1, MaxBackoff: -1},
		{MaxAttempts: 1, InitialBackoff: time.Second, MaxBackoff: time.Millisecond},
		{MaxAttempts: 1, Multiplier: .5},
	}
	for _, policy := range tests {
		if _, err := NewRetrier(policy); err == nil {
			t.Fatalf("expected invalid policy error for %+v", policy)
		}
	}
}

func TestRetrierRejectsNilOperation(t *testing.T) {
	retrier, err := NewRetrier(RetryPolicy{MaxAttempts: 1})
	if err != nil {
		t.Fatalf("new retrier: %v", err)
	}
	if err := retrier.Do(context.Background(), nil); err == nil {
		t.Fatal("expected nil operation error")
	}
}

func TestRetryConvenienceFunctionAndDefaults(t *testing.T) {
	policy := DefaultRetryPolicy()
	if policy.MaxAttempts != 4 || policy.Multiplier != 2 {
		t.Fatalf("unexpected default policy: %+v", policy)
	}
	policy.MaxAttempts = 1
	if err := Retry(context.Background(), policy, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Retry: %v", err)
	}
}

func TestRetryOptionsValidation(t *testing.T) {
	if _, err := NewRetrier(RetryPolicy{MaxAttempts: 1}, nil); err == nil {
		t.Fatal("expected nil retry option error")
	}
	if _, err := NewRetrier(RetryPolicy{MaxAttempts: 1}, WithRetryWaitFunc(nil)); err == nil {
		t.Fatal("expected nil wait function error")
	}
}

func TestRetryWaitForContextWithoutWallClockDelay(t *testing.T) {
	if err := waitForContext(context.Background(), 0); err != nil {
		t.Fatalf("zero delay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v, want context.Canceled", err)
	}
}
