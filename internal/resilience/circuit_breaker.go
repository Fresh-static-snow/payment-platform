package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrCircuitOpen             = errors.New("circuit breaker is open")
	ErrHalfOpenCapacityReached = errors.New("circuit breaker half-open capacity reached")
)

type CircuitState string

const (
	StateClosed   CircuitState = "CLOSED"
	StateOpen     CircuitState = "OPEN"
	StateHalfOpen CircuitState = "HALF_OPEN"
)

type FailureClassifier func(error) bool

type CircuitBreakerConfig struct {
	FailureThreshold    int
	OpenTimeout         time.Duration
	HalfOpenMaxRequests int
	SuccessThreshold    int
	IsFailure           FailureClassifier
}

func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		FailureThreshold:    5,
		OpenTimeout:         30 * time.Second,
		HalfOpenMaxRequests: 1,
		SuccessThreshold:    1,
	}
}

type Clock interface {
	Now() time.Time
}

type clockFunc func() time.Time

func (fn clockFunc) Now() time.Time { return fn() }

type CircuitBreakerOption func(*CircuitBreaker) error

func WithCircuitClock(clock Clock) CircuitBreakerOption {
	return func(breaker *CircuitBreaker) error {
		if clock == nil {
			return fmt.Errorf("circuit breaker clock must not be nil")
		}
		breaker.clock = clock
		return nil
	}
}

// CircuitBreaker implements the classic CLOSED/OPEN/HALF_OPEN state machine.
// All state transitions and counters are protected by mu; user operations run
// outside the lock so a slow provider cannot block unrelated callers.
type CircuitBreaker struct {
	config CircuitBreakerConfig
	clock  Clock

	mu                sync.Mutex
	state             CircuitState
	generation        uint64
	consecutiveFails  int
	openedAt          time.Time
	halfOpenInFlight  int
	halfOpenSuccesses int
}

func NewCircuitBreaker(config CircuitBreakerConfig, options ...CircuitBreakerOption) (*CircuitBreaker, error) {
	if config.FailureThreshold < 1 {
		return nil, fmt.Errorf("circuit breaker failure threshold must be at least 1")
	}
	if config.OpenTimeout <= 0 {
		return nil, fmt.Errorf("circuit breaker open timeout must be positive")
	}
	if config.HalfOpenMaxRequests < 1 {
		return nil, fmt.Errorf("circuit breaker half-open max requests must be at least 1")
	}
	if config.SuccessThreshold < 1 {
		return nil, fmt.Errorf("circuit breaker success threshold must be at least 1")
	}
	if config.IsFailure == nil {
		config.IsFailure = func(err error) bool { return err != nil }
	}

	breaker := &CircuitBreaker{
		config: config,
		clock:  clockFunc(time.Now),
		state:  StateClosed,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("circuit breaker option must not be nil")
		}
		if err := option(breaker); err != nil {
			return nil, err
		}
	}
	return breaker, nil
}

func (b *CircuitBreaker) Execute(ctx context.Context, operation func(context.Context) error) (err error) {
	if operation == nil {
		return fmt.Errorf("circuit breaker operation must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	token, err := b.beforeRequest()
	if err != nil {
		return err
	}

	success := false
	defer func() {
		// The defer also releases a HALF_OPEN slot and records a failure if the
		// operation panics. The panic itself intentionally remains visible.
		b.afterRequest(token, success)
	}()
	err = operation(ctx)
	success = err == nil || !b.config.IsFailure(err)
	return err
}

func (b *CircuitBreaker) State() CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

type CircuitSnapshot struct {
	State             CircuitState
	ConsecutiveFails  int
	OpenedAt          time.Time
	HalfOpenInFlight  int
	HalfOpenSuccesses int
}

func (b *CircuitBreaker) Snapshot() CircuitSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return CircuitSnapshot{
		State:             b.state,
		ConsecutiveFails:  b.consecutiveFails,
		OpenedAt:          b.openedAt,
		HalfOpenInFlight:  b.halfOpenInFlight,
		HalfOpenSuccesses: b.halfOpenSuccesses,
	}
}

// Reset closes the breaker and starts a new generation. Outcomes from requests
// that began before Reset are ignored when they eventually return.
func (b *CircuitBreaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeLocked()
}

type requestToken struct {
	generation uint64
	state      CircuitState
}

func (b *CircuitBreaker) beforeRequest() (requestToken, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == StateOpen {
		if b.clock.Now().Sub(b.openedAt) < b.config.OpenTimeout {
			return requestToken{}, ErrCircuitOpen
		}
		b.halfOpenLocked()
	}
	if b.state == StateHalfOpen {
		if b.halfOpenInFlight >= b.config.HalfOpenMaxRequests {
			return requestToken{}, ErrHalfOpenCapacityReached
		}
		b.halfOpenInFlight++
	}
	return requestToken{generation: b.generation, state: b.state}, nil
}

func (b *CircuitBreaker) afterRequest(token requestToken, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if token.generation != b.generation || token.state != b.state {
		return
	}

	switch b.state {
	case StateClosed:
		if success {
			b.consecutiveFails = 0
			return
		}
		b.consecutiveFails++
		if b.consecutiveFails >= b.config.FailureThreshold {
			b.openLocked()
		}
	case StateHalfOpen:
		b.halfOpenInFlight--
		if !success {
			b.openLocked()
			return
		}
		b.halfOpenSuccesses++
		if b.halfOpenSuccesses >= b.config.SuccessThreshold {
			b.closeLocked()
		}
	}
}

func (b *CircuitBreaker) openLocked() {
	b.state = StateOpen
	b.generation++
	b.consecutiveFails = 0
	b.openedAt = b.clock.Now()
	b.halfOpenInFlight = 0
	b.halfOpenSuccesses = 0
}

func (b *CircuitBreaker) halfOpenLocked() {
	b.state = StateHalfOpen
	b.generation++
	b.consecutiveFails = 0
	b.halfOpenInFlight = 0
	b.halfOpenSuccesses = 0
}

func (b *CircuitBreaker) closeLocked() {
	b.state = StateClosed
	b.generation++
	b.consecutiveFails = 0
	b.openedAt = time.Time{}
	b.halfOpenInFlight = 0
	b.halfOpenSuccesses = 0
}
