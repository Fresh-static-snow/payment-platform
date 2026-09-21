// Package provider contains the deliberately small payment-provider boundary
// used by the payment worker. The fake implementation is deterministic so it
// is useful both in the local stack and in tests.
package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	EnvMode         = "FAKE_PROVIDER_MODE"
	EnvPaymentModes = "FAKE_PROVIDER_PAYMENT_MODES"
	EnvSlowDelay    = "FAKE_PROVIDER_SLOW_DELAY"
)

var (
	ErrInvalidRequest  = errors.New("invalid provider request")
	ErrProviderFailure = errors.New("fake provider failure")
)

// Mode controls the fake provider's behaviour. ModeByPaymentID maps a stable
// hash of the payment ID to success (70%), failure (20%), or slow success
// (10%). Unlike randomness, the same payment always receives the same mode.
type Mode string

const (
	ModeSuccess     Mode = "success"
	ModeFail        Mode = "fail"
	ModeSlow        Mode = "slow"
	ModeByPaymentID Mode = "by_payment_id"
)

type Payment struct {
	ID       string
	Amount   int64
	Currency string
}

type Result struct {
	Reference string
}

// Provider is the worker-facing provider contract.
type Provider interface {
	Process(context.Context, Payment) (Result, error)
}

// WaitFunc makes slow-provider behaviour cancellable and lets tests advance
// without wall-clock sleeps.
type WaitFunc func(context.Context, time.Duration) error

type Option func(*settings) error

type settings struct {
	defaultMode Mode
	modes       map[string]Mode
	slowDelay   time.Duration
	wait        WaitFunc
}

// WithMode overrides the environment-provided default mode.
func WithMode(mode Mode) Option {
	return func(cfg *settings) error {
		cfg.defaultMode = mode
		return nil
	}
}

// WithPaymentMode selects an exact mode for one payment. It takes precedence
// over the default mode and the hash-based selector.
func WithPaymentMode(paymentID string, mode Mode) Option {
	return func(cfg *settings) error {
		paymentID = strings.TrimSpace(paymentID)
		if paymentID == "" {
			return fmt.Errorf("payment mode: payment ID must not be empty")
		}
		cfg.modes[paymentID] = mode
		return nil
	}
}

func WithSlowDelay(delay time.Duration) Option {
	return func(cfg *settings) error {
		cfg.slowDelay = delay
		return nil
	}
}

func WithWaitFunc(wait WaitFunc) Option {
	return func(cfg *settings) error {
		if wait == nil {
			return fmt.Errorf("provider wait function must not be nil")
		}
		cfg.wait = wait
		return nil
	}
}

// FakeProvider is safe for concurrent use. Its mode configuration is immutable
// after construction; only call counters are protected by the mutex.
type FakeProvider struct {
	defaultMode Mode
	modes       map[string]Mode
	slowDelay   time.Duration
	wait        WaitFunc

	mu         sync.Mutex
	calls      map[string]int
	totalCalls int
}

var _ Provider = (*FakeProvider)(nil)

// NewFakeProvider reads these optional variables before applying options:
//
//   - FAKE_PROVIDER_MODE=success|fail|slow|by_payment_id
//   - FAKE_PROVIDER_PAYMENT_MODES=id-a=fail,id-b=slow
//   - FAKE_PROVIDER_SLOW_DELAY=750ms
//
// With* options override values read from the environment.
func NewFakeProvider(options ...Option) (*FakeProvider, error) {
	cfg, err := settingsFromEnvironment(os.Getenv)
	if err != nil {
		return nil, err
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("provider option must not be nil")
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}
	if !cfg.defaultMode.valid(true) {
		return nil, fmt.Errorf("invalid fake provider mode %q", cfg.defaultMode)
	}
	for paymentID, mode := range cfg.modes {
		if !mode.valid(false) {
			return nil, fmt.Errorf("invalid fake provider mode %q for payment %q", mode, paymentID)
		}
	}
	if cfg.slowDelay < 0 {
		return nil, fmt.Errorf("fake provider slow delay must not be negative")
	}

	modes := make(map[string]Mode, len(cfg.modes))
	for paymentID, mode := range cfg.modes {
		modes[paymentID] = mode
	}
	return &FakeProvider{
		defaultMode: cfg.defaultMode,
		modes:       modes,
		slowDelay:   cfg.slowDelay,
		wait:        cfg.wait,
		calls:       make(map[string]int),
	}, nil
}

func (p *FakeProvider) Process(ctx context.Context, payment Payment) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(payment.ID) == "" {
		return Result{}, fmt.Errorf("%w: payment ID is required", ErrInvalidRequest)
	}
	if payment.Amount <= 0 {
		return Result{}, fmt.Errorf("%w: amount must be greater than zero", ErrInvalidRequest)
	}
	if strings.TrimSpace(payment.Currency) == "" {
		return Result{}, fmt.Errorf("%w: currency is required", ErrInvalidRequest)
	}

	p.recordCall(payment.ID)
	mode := p.modeFor(payment.ID)
	switch mode {
	case ModeSuccess:
		return successfulResult(payment.ID), nil
	case ModeFail:
		return Result{}, fmt.Errorf("%w: payment %s", ErrProviderFailure, payment.ID)
	case ModeSlow:
		if err := p.wait(ctx, p.slowDelay); err != nil {
			return Result{}, fmt.Errorf("wait for fake provider: %w", err)
		}
		return successfulResult(payment.ID), nil
	default:
		// Construction validates every configured mode; this protects callers if
		// the implementation changes without maintaining that invariant.
		return Result{}, fmt.Errorf("unsupported fake provider mode %q", mode)
	}
}

func (p *FakeProvider) ModeFor(paymentID string) Mode {
	return p.modeFor(paymentID)
}

func (p *FakeProvider) Calls(paymentID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[paymentID]
}

func (p *FakeProvider) TotalCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.totalCalls
}

func (p *FakeProvider) recordCall(paymentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[paymentID]++
	p.totalCalls++
}

func (p *FakeProvider) modeFor(paymentID string) Mode {
	if mode, ok := p.modes[paymentID]; ok {
		return mode
	}
	if p.defaultMode == ModeByPaymentID {
		return deterministicMode(paymentID)
	}
	return p.defaultMode
}

func deterministicMode(paymentID string) Mode {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(paymentID))
	switch hash.Sum32() % 10 {
	case 7, 8:
		return ModeFail
	case 9:
		return ModeSlow
	default:
		return ModeSuccess
	}
}

func successfulResult(paymentID string) Result {
	digest := sha256.Sum256([]byte(paymentID))
	return Result{Reference: "fake_" + hex.EncodeToString(digest[:8])}
}

func settingsFromEnvironment(getenv func(string) string) (settings, error) {
	cfg := settings{
		defaultMode: ModeSuccess,
		modes:       make(map[string]Mode),
		slowDelay:   750 * time.Millisecond,
		wait:        waitContext,
	}
	if value := strings.TrimSpace(getenv(EnvMode)); value != "" {
		cfg.defaultMode = parseMode(value)
	}
	if value := strings.TrimSpace(getenv(EnvSlowDelay)); value != "" {
		delay, err := time.ParseDuration(value)
		if err != nil {
			return settings{}, fmt.Errorf("parse %s: %w", EnvSlowDelay, err)
		}
		cfg.slowDelay = delay
	}
	if value := strings.TrimSpace(getenv(EnvPaymentModes)); value != "" {
		for _, item := range strings.Split(value, ",") {
			paymentID, rawMode, ok := strings.Cut(item, "=")
			paymentID = strings.TrimSpace(paymentID)
			if !ok || paymentID == "" || strings.TrimSpace(rawMode) == "" {
				return settings{}, fmt.Errorf("parse %s item %q: expected payment_id=mode", EnvPaymentModes, item)
			}
			cfg.modes[paymentID] = parseMode(rawMode)
		}
	}
	return cfg, nil
}

func parseMode(value string) Mode {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "by-id" || normalized == "by_id" || normalized == "deterministic" {
		return ModeByPaymentID
	}
	return Mode(normalized)
}

func (m Mode) valid(allowByPaymentID bool) bool {
	switch m {
	case ModeSuccess, ModeFail, ModeSlow:
		return true
	case ModeByPaymentID:
		return allowByPaymentID
	default:
		return false
	}
}

func waitContext(ctx context.Context, delay time.Duration) error {
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
