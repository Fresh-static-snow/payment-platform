//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	paymentdomain "github.com/praedyth/payment-platform/internal/payment/domain"
	paymentrepository "github.com/praedyth/payment-platform/internal/payment/repository"
	refunddomain "github.com/praedyth/payment-platform/internal/refund/domain"
	refundrepository "github.com/praedyth/payment-platform/internal/refund/repository"
	"github.com/praedyth/payment-platform/pkg/database"
)

func TestRefundIdempotentRetryIgnoresLaterReservations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	userID := uuid.New()
	payment := createCompletedPayment(t, ctx, paymentrepository.New(pool), userID, 100)
	store := refundrepository.New(pool)
	firstParams := refundParams(payment.ID, userID, 60, "retry-after-reservation-"+uuid.NewString())
	first, created, err := store.Create(ctx, firstParams)
	if err != nil || !created {
		t.Fatalf("create first refund: created=%t err=%v", created, err)
	}
	if _, created, err := store.Create(ctx, refundParams(payment.ID, userID, 40, "consume-remainder-"+uuid.NewString())); err != nil || !created {
		t.Fatalf("create second refund: created=%t err=%v", created, err)
	}

	replayed, created, err := store.Create(ctx, firstParams)
	if err != nil {
		t.Fatalf("replay first refund: %v", err)
	}
	if created || replayed.ID != first.ID {
		t.Fatalf("replay = %s created=%t, want %s/false", replayed.ID, created, first.ID)
	}
}

func TestConcurrentRefundReservationsCannotExceedPayment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	userID := uuid.New()
	payment := createCompletedPayment(t, ctx, paymentrepository.New(pool), userID, 100)
	store := refundrepository.New(pool)
	const clients = 20
	start := make(chan struct{})
	errorsCh := make(chan error, clients)
	var created atomic.Int32
	var rejected atomic.Int32
	var group sync.WaitGroup
	for index := 0; index < clients; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, wasCreated, err := store.Create(ctx, refundParams(payment.ID, userID, 10, "concurrent-refund-"+uuid.NewString()))
			switch {
			case err == nil && wasCreated:
				created.Add(1)
			case errors.Is(err, refunddomain.ErrRefundAmountExceeded):
				rejected.Add(1)
			default:
				errorsCh <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("unexpected concurrent refund result: %v", err)
	}
	if created.Load() != 10 || rejected.Load() != 10 {
		t.Fatalf("created=%d rejected=%d, want 10/10", created.Load(), rejected.Load())
	}
}

func TestRefundIdempotencyKeyIsScopedToUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	payments := paymentrepository.New(pool)
	store := refundrepository.New(pool)
	sharedKey := "shared-across-users-" + uuid.NewString()
	for range 2 {
		userID := uuid.New()
		payment := createCompletedPayment(t, ctx, payments, userID, 100)
		if _, created, err := store.Create(ctx, refundParams(payment.ID, userID, 10, sharedKey)); err != nil || !created {
			t.Fatalf("create user-scoped refund: created=%t err=%v", created, err)
		}
	}
}

func TestRefundTerminalTransitionCannotBeReinterpreted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	userID := uuid.New()
	payment := createCompletedPayment(t, ctx, paymentrepository.New(pool), userID, 100)
	store := refundrepository.New(pool)
	refund, _, err := store.Create(ctx, refundParams(payment.ID, userID, 50, "terminal-refund-"+uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, refund.ID, refunddomain.StatusProcessing, "", "", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, refund.ID, refunddomain.StatusCompleted, "provider-reference", "", uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, refund.ID, refunddomain.StatusCompleted, "another-reference", "", uuid.NewString()); !errors.Is(err, refunddomain.ErrTransitionConflict) {
		t.Fatalf("conflicting completion error = %v, want ErrTransitionConflict", err)
	}
	if _, err := store.Transition(ctx, refund.ID, refunddomain.StatusFailed, "", "late failure", uuid.NewString()); !errors.Is(err, refunddomain.ErrInvalidStatusTransition) {
		t.Fatalf("terminal regression error = %v, want ErrInvalidStatusTransition", err)
	}
}

func createCompletedPayment(
	t *testing.T,
	ctx context.Context,
	store *paymentrepository.Store,
	userID uuid.UUID,
	amount int64,
) paymentdomain.Payment {
	t.Helper()
	hash := sha256.Sum256([]byte(uuid.NewString()))
	payment, _, err := store.Create(ctx, paymentdomain.CreateParams{
		IdempotencyKey: "refund-fixture-" + uuid.NewString(),
		RequestHash:    hash[:],
		UserID:         userID,
		Amount:         amount,
		Currency:       "USD",
		RequestID:      uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create payment fixture: %v", err)
	}
	payment, err = store.Transition(ctx, payment.ID, payment.Version, paymentdomain.StatusProcessing, "", "", uuid.NewString(), "")
	if err != nil {
		t.Fatalf("mark payment processing: %v", err)
	}
	payment, err = store.Transition(ctx, payment.ID, payment.Version, paymentdomain.StatusCompleted, "payment-provider-reference", "", uuid.NewString(), "")
	if err != nil {
		t.Fatalf("complete payment fixture: %v", err)
	}
	return payment
}

func refundParams(paymentID, userID uuid.UUID, amount int64, key string) refunddomain.CreateParams {
	hash := sha256.Sum256([]byte(userID.String() + "|" + paymentID.String() + "|" + key))
	return refunddomain.CreateParams{
		PaymentID: paymentID, UserID: userID, Amount: amount,
		IdempotencyKey: key, RequestHash: hash[:], RequestID: uuid.NewString(),
	}
}
