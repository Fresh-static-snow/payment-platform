package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestFakeProviderSuccessIsStable(t *testing.T) {
	clearProviderEnvironment(t)
	provider, err := NewFakeProvider(WithMode(ModeSuccess))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	payment := Payment{ID: "payment-1", Amount: 1000, Currency: "USD"}

	first, err := provider.Process(context.Background(), payment)
	if err != nil {
		t.Fatalf("first process: %v", err)
	}
	second, err := provider.Process(context.Background(), payment)
	if err != nil {
		t.Fatalf("second process: %v", err)
	}
	if first.Reference == "" || first.Reference != second.Reference {
		t.Fatalf("expected a stable non-empty reference, got %q and %q", first.Reference, second.Reference)
	}
	if got := provider.Calls(payment.ID); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
}

func TestFakeProviderFailure(t *testing.T) {
	clearProviderEnvironment(t)
	provider, err := NewFakeProvider(WithMode(ModeFail))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	_, err = provider.Process(context.Background(), Payment{ID: "failed", Amount: 10, Currency: "EUR"})
	if !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("error = %v, want ErrProviderFailure", err)
	}
}

func TestFakeProviderSlowModeUsesCancellableWait(t *testing.T) {
	clearProviderEnvironment(t)
	var gotDelay time.Duration
	ctx, cancel := context.WithCancel(context.Background())
	provider, err := NewFakeProvider(
		WithMode(ModeSlow),
		WithSlowDelay(3*time.Second),
		WithWaitFunc(func(waitCtx context.Context, delay time.Duration) error {
			gotDelay = delay
			cancel()
			<-waitCtx.Done()
			return waitCtx.Err()
		}),
	)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	_, err = provider.Process(ctx, Payment{ID: "slow", Amount: 10, Currency: "USD"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if gotDelay != 3*time.Second {
		t.Fatalf("delay = %s, want 3s", gotDelay)
	}
}

func TestFakeProviderSlowModeCompletes(t *testing.T) {
	clearProviderEnvironment(t)
	waited := false
	provider, err := NewFakeProvider(
		WithMode(ModeSlow),
		WithSlowDelay(time.Second),
		WithWaitFunc(func(context.Context, time.Duration) error {
			waited = true
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	result, err := provider.Process(context.Background(), Payment{ID: "slow-success", Amount: 10, Currency: "USD"})
	if err != nil || result.Reference == "" || !waited {
		t.Fatalf("result = %+v, error = %v, waited = %v", result, err, waited)
	}
}

func TestFakeProviderPaymentModeOverridesDefault(t *testing.T) {
	clearProviderEnvironment(t)
	provider, err := NewFakeProvider(
		WithMode(ModeSuccess),
		WithPaymentMode("declined-payment", ModeFail),
	)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	if _, err := provider.Process(context.Background(), Payment{ID: "ordinary-payment", Amount: 1, Currency: "USD"}); err != nil {
		t.Fatalf("default payment: %v", err)
	}
	_, err = provider.Process(context.Background(), Payment{ID: "declined-payment", Amount: 1, Currency: "USD"})
	if !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("override error = %v, want ErrProviderFailure", err)
	}
}

func TestFakeProviderReadsEnvironment(t *testing.T) {
	t.Setenv(EnvMode, "success")
	t.Setenv(EnvPaymentModes, "payment-a=fail, payment-b=slow")
	t.Setenv(EnvSlowDelay, "1250ms")
	var gotDelay time.Duration
	provider, err := NewFakeProvider(WithWaitFunc(func(_ context.Context, delay time.Duration) error {
		gotDelay = delay
		return nil
	}))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	_, err = provider.Process(context.Background(), Payment{ID: "payment-a", Amount: 1, Currency: "USD"})
	if !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("payment-a error = %v, want ErrProviderFailure", err)
	}
	if _, err := provider.Process(context.Background(), Payment{ID: "payment-b", Amount: 1, Currency: "USD"}); err != nil {
		t.Fatalf("payment-b: %v", err)
	}
	if gotDelay != 1250*time.Millisecond {
		t.Fatalf("slow delay = %s, want 1.25s", gotDelay)
	}
}

func TestFakeProviderByPaymentIDIsDeterministic(t *testing.T) {
	clearProviderEnvironment(t)
	provider, err := NewFakeProvider(WithMode(ModeByPaymentID))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	seen := map[Mode]bool{}
	for i := 0; i < 1000 && len(seen) < 3; i++ {
		id := "payment-" + time.Unix(int64(i), 0).UTC().Format("150405")
		first := provider.ModeFor(id)
		second := provider.ModeFor(id)
		if first != second {
			t.Fatalf("mode for %q changed from %q to %q", id, first, second)
		}
		seen[first] = true
	}
	for _, mode := range []Mode{ModeSuccess, ModeFail, ModeSlow} {
		if !seen[mode] {
			t.Fatalf("deterministic selector never produced %q", mode)
		}
	}
}

func TestFakeProviderRejectsInvalidRequestsWithoutCallingProvider(t *testing.T) {
	clearProviderEnvironment(t)
	provider, err := NewFakeProvider(WithMode(ModeSuccess))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	_, err = provider.Process(context.Background(), Payment{ID: "", Amount: 100, Currency: "USD"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if provider.TotalCalls() != 0 {
		t.Fatalf("invalid request must not reach provider, calls = %d", provider.TotalCalls())
	}

	_, err = provider.Process(context.Background(), Payment{ID: "id", Amount: 0, Currency: "USD"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("amount error = %v, want ErrInvalidRequest", err)
	}
	_, err = provider.Process(context.Background(), Payment{ID: "id", Amount: 1})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("currency error = %v, want ErrInvalidRequest", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = provider.Process(ctx, Payment{ID: "id", Amount: 1, Currency: "USD"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v, want context.Canceled", err)
	}
	if provider.TotalCalls() != 0 {
		t.Fatalf("rejected requests must not reach provider, calls = %d", provider.TotalCalls())
	}
}

func TestFakeProviderCountersAreConcurrentSafe(t *testing.T) {
	clearProviderEnvironment(t)
	provider, err := NewFakeProvider(WithMode(ModeSuccess))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	const calls = 64
	var wg sync.WaitGroup
	wg.Add(calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()
			_, _ = provider.Process(context.Background(), Payment{ID: "shared", Amount: 1, Currency: "USD"})
		}()
	}
	wg.Wait()
	if got := provider.Calls("shared"); got != calls {
		t.Fatalf("calls = %d, want %d", got, calls)
	}
	if got := provider.TotalCalls(); got != calls {
		t.Fatalf("total calls = %d, want %d", got, calls)
	}
}

func TestFakeProviderConfigurationValidation(t *testing.T) {
	clearProviderEnvironment(t)
	tests := []struct {
		name   string
		option Option
	}{
		{name: "invalid mode", option: WithMode(Mode("surprise"))},
		{name: "negative delay", option: WithSlowDelay(-time.Second)},
		{name: "empty payment id", option: WithPaymentMode("", ModeFail)},
		{name: "by-id override", option: WithPaymentMode("id", ModeByPaymentID)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewFakeProvider(test.option); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestFakeProviderEnvironmentValidation(t *testing.T) {
	t.Run("duration", func(t *testing.T) {
		t.Setenv(EnvMode, "success")
		t.Setenv(EnvPaymentModes, "")
		t.Setenv(EnvSlowDelay, "not-a-duration")
		if _, err := NewFakeProvider(); err == nil {
			t.Fatal("expected duration parse error")
		}
	})
	t.Run("payment mapping", func(t *testing.T) {
		t.Setenv(EnvMode, "success")
		t.Setenv(EnvPaymentModes, "missing-separator")
		t.Setenv(EnvSlowDelay, "")
		if _, err := NewFakeProvider(); err == nil {
			t.Fatal("expected payment mapping parse error")
		}
	})
	t.Run("mode", func(t *testing.T) {
		t.Setenv(EnvMode, "unknown")
		t.Setenv(EnvPaymentModes, "")
		t.Setenv(EnvSlowDelay, "")
		if _, err := NewFakeProvider(); err == nil {
			t.Fatal("expected mode validation error")
		}
	})
}

func TestProviderWaitContextWithoutWallClockDelay(t *testing.T) {
	if err := waitContext(context.Background(), 0); err != nil {
		t.Fatalf("zero delay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v, want context.Canceled", err)
	}
}

func clearProviderEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv(EnvMode, "")
	t.Setenv(EnvPaymentModes, "")
	t.Setenv(EnvSlowDelay, "")
}
