package resilience

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
}

func (clock *manualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *manualClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

func breakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold:    2,
		OpenTimeout:         10 * time.Second,
		HalfOpenMaxRequests: 1,
		SuccessThreshold:    1,
	}
}

func TestCircuitBreakerOpensAtFailureThreshold(t *testing.T) {
	clock := newManualClock()
	breaker, err := NewCircuitBreaker(breakerConfig(), WithCircuitClock(clock))
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	providerCalls := 0
	fail := func(context.Context) error {
		providerCalls++
		return errTransient
	}
	if err := breaker.Execute(context.Background(), fail); !errors.Is(err, errTransient) {
		t.Fatalf("first failure = %v", err)
	}
	if breaker.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", breaker.State())
	}
	if err := breaker.Execute(context.Background(), fail); !errors.Is(err, errTransient) {
		t.Fatalf("second failure = %v", err)
	}
	if breaker.State() != StateOpen {
		t.Fatalf("state = %s, want OPEN", breaker.State())
	}
	if err := breaker.Execute(context.Background(), fail); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("open call = %v, want ErrCircuitOpen", err)
	}
	if providerCalls != 2 {
		t.Fatalf("provider calls = %d, want 2", providerCalls)
	}
}

func TestCircuitBreakerHalfOpenSuccessCloses(t *testing.T) {
	clock := newManualClock()
	config := breakerConfig()
	config.FailureThreshold = 1
	breaker, err := NewCircuitBreaker(config, WithCircuitClock(clock))
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	_ = breaker.Execute(context.Background(), func(context.Context) error { return errTransient })
	clock.Advance(config.OpenTimeout)

	if err := breaker.Execute(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("half-open probe: %v", err)
	}
	if breaker.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", breaker.State())
	}
}

func TestCircuitBreakerHalfOpenFailureRestartsOpenTimeout(t *testing.T) {
	clock := newManualClock()
	config := breakerConfig()
	config.FailureThreshold = 1
	breaker, err := NewCircuitBreaker(config, WithCircuitClock(clock))
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	_ = breaker.Execute(context.Background(), func(context.Context) error { return errTransient })
	firstOpenedAt := breaker.Snapshot().OpenedAt
	clock.Advance(config.OpenTimeout)
	_ = breaker.Execute(context.Background(), func(context.Context) error { return errTransient })
	secondOpenedAt := breaker.Snapshot().OpenedAt
	if !secondOpenedAt.After(firstOpenedAt) {
		t.Fatalf("open timeout was not restarted: first=%s second=%s", firstOpenedAt, secondOpenedAt)
	}
	if err := breaker.Execute(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("immediate retry = %v, want ErrCircuitOpen", err)
	}
}

func TestCircuitBreakerLimitsConcurrentHalfOpenProbes(t *testing.T) {
	clock := newManualClock()
	config := breakerConfig()
	config.FailureThreshold = 1
	breaker, err := NewCircuitBreaker(config, WithCircuitClock(clock))
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	_ = breaker.Execute(context.Background(), func(context.Context) error { return errTransient })
	clock.Advance(config.OpenTimeout)

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- breaker.Execute(context.Background(), func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	if err := breaker.Execute(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrHalfOpenCapacityReached) {
		t.Fatalf("second half-open call = %v, want capacity error", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("first half-open probe: %v", err)
	}
	if breaker.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", breaker.State())
	}
}

func TestCircuitBreakerRequiresConfiguredRecoverySuccesses(t *testing.T) {
	clock := newManualClock()
	config := breakerConfig()
	config.FailureThreshold = 1
	config.SuccessThreshold = 2
	breaker, err := NewCircuitBreaker(config, WithCircuitClock(clock))
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	_ = breaker.Execute(context.Background(), func(context.Context) error { return errTransient })
	clock.Advance(config.OpenTimeout)
	if err := breaker.Execute(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("first probe: %v", err)
	}
	if breaker.State() != StateHalfOpen {
		t.Fatalf("state = %s, want HALF_OPEN", breaker.State())
	}
	if err := breaker.Execute(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if breaker.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", breaker.State())
	}
}

func TestCircuitBreakerSuccessResetsConsecutiveFailures(t *testing.T) {
	breaker, err := NewCircuitBreaker(breakerConfig())
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	_ = breaker.Execute(context.Background(), func(context.Context) error { return errTransient })
	_ = breaker.Execute(context.Background(), func(context.Context) error { return nil })
	_ = breaker.Execute(context.Background(), func(context.Context) error { return errTransient })
	if breaker.State() != StateClosed || breaker.Snapshot().ConsecutiveFails != 1 {
		t.Fatalf("snapshot = %+v, want one failure in CLOSED", breaker.Snapshot())
	}
}

func TestCircuitBreakerFailureClassifier(t *testing.T) {
	ignored := errors.New("ignored")
	config := breakerConfig()
	config.FailureThreshold = 1
	config.IsFailure = func(err error) bool { return !errors.Is(err, ignored) }
	breaker, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	if err := breaker.Execute(context.Background(), func(context.Context) error { return ignored }); !errors.Is(err, ignored) {
		t.Fatalf("operation error = %v, want ignored error", err)
	}
	if breaker.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", breaker.State())
	}
}

func TestCircuitBreakerCancelledContextDoesNotInvokeOperation(t *testing.T) {
	config := breakerConfig()
	config.FailureThreshold = 1
	breaker, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err = breaker.Execute(ctx, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("error = %v, called = %v; want cancellation without operation", err, called)
	}
	if breaker.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", breaker.State())
	}
}

func TestCircuitBreakerPanicCountsAsFailure(t *testing.T) {
	config := breakerConfig()
	config.FailureThreshold = 1
	breaker, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic")
			}
		}()
		_ = breaker.Execute(context.Background(), func(context.Context) error {
			panic("provider panic")
		})
	}()
	if breaker.State() != StateOpen {
		t.Fatalf("state = %s, want OPEN", breaker.State())
	}
}

func TestCircuitBreakerConcurrentSuccesses(t *testing.T) {
	breaker, err := NewCircuitBreaker(breakerConfig())
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	const calls = 128
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			if err := breaker.Execute(context.Background(), func(context.Context) error { return nil }); err != nil {
				t.Errorf("execute: %v", err)
			}
		}()
	}
	wg.Wait()
	if breaker.State() != StateClosed {
		t.Fatalf("state = %s, want CLOSED", breaker.State())
	}
}

func TestCircuitBreakerValidation(t *testing.T) {
	valid := breakerConfig()
	tests := []CircuitBreakerConfig{
		{FailureThreshold: 0, OpenTimeout: time.Second, HalfOpenMaxRequests: 1, SuccessThreshold: 1},
		{FailureThreshold: 1, OpenTimeout: 0, HalfOpenMaxRequests: 1, SuccessThreshold: 1},
		{FailureThreshold: 1, OpenTimeout: time.Second, HalfOpenMaxRequests: 0, SuccessThreshold: 1},
		{FailureThreshold: 1, OpenTimeout: time.Second, HalfOpenMaxRequests: 1, SuccessThreshold: 0},
	}
	for _, config := range tests {
		if _, err := NewCircuitBreaker(config); err == nil {
			t.Fatalf("expected validation error for %+v", config)
		}
	}
	breaker, err := NewCircuitBreaker(valid)
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if err := breaker.Execute(context.Background(), nil); err == nil {
		t.Fatal("expected nil operation error")
	}
	if _, err := NewCircuitBreaker(valid, nil); err == nil {
		t.Fatal("expected nil option error")
	}
	if _, err := NewCircuitBreaker(valid, WithCircuitClock(nil)); err == nil {
		t.Fatal("expected nil clock error")
	}
}

func TestCircuitBreakerDefaultsAndReset(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	config.FailureThreshold = 1
	breaker, err := NewCircuitBreaker(config)
	if err != nil {
		t.Fatalf("new breaker: %v", err)
	}
	_ = breaker.Execute(context.Background(), func(context.Context) error { return errTransient })
	if breaker.State() != StateOpen {
		t.Fatalf("state = %s, want OPEN", breaker.State())
	}
	breaker.Reset()
	snapshot := breaker.Snapshot()
	if snapshot.State != StateClosed || snapshot.ConsecutiveFails != 0 || !snapshot.OpenedAt.IsZero() {
		t.Fatalf("snapshot after reset = %+v", snapshot)
	}
}
