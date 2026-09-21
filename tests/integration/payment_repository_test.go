//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/payment/domain"
	"github.com/praedyth/payment-platform/internal/payment/repository"
	"github.com/praedyth/payment-platform/pkg/database"
)

func TestConcurrentIdempotencyCreatesOnePaymentAndEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := repository.New(pool)
	key := "integration-" + uuid.NewString()
	userID := uuid.New()
	hash := sha256.Sum256([]byte("same-request"))
	params := domain.CreateParams{IdempotencyKey: key, RequestHash: hash[:], UserID: userID, Amount: 1000, Currency: "USD", Description: "race", RequestID: uuid.NewString()}

	const clients = 32
	start := make(chan struct{})
	ids := make(chan uuid.UUID, clients)
	errorsCh := make(chan error, clients)
	var created atomic.Int32
	var wg sync.WaitGroup
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			payment, wasCreated, err := store.Create(ctx, params)
			if err != nil {
				errorsCh <- err
				return
			}
			if wasCreated {
				created.Add(1)
			}
			ids <- payment.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent create: %v", err)
	}
	if created.Load() != 1 {
		t.Fatalf("created count=%d, want 1", created.Load())
	}
	var first uuid.UUID
	for id := range ids {
		if first == uuid.Nil {
			first = id
		}
		if id != first {
			t.Fatalf("got multiple payment ids: %s and %s", first, id)
		}
	}
	var payments, events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE idempotency_key=$1`, key).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type='payment.created.v1'`, first).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if payments != 1 || events != 1 {
		t.Fatalf("payments=%d events=%d, want 1/1", payments, events)
	}
}

func TestIdempotencyBodyConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := repository.New(pool)
	key := "integration-conflict-" + uuid.NewString()
	firstHash := sha256.Sum256([]byte("first"))
	secondHash := sha256.Sum256([]byte("second"))
	base := domain.CreateParams{IdempotencyKey: key, RequestHash: firstHash[:], UserID: uuid.New(), Amount: 500, Currency: "EUR", RequestID: uuid.NewString()}
	if _, _, err := store.Create(ctx, base); err != nil {
		t.Fatal(err)
	}
	base.RequestHash = secondHash[:]
	if _, _, err := store.Create(ctx, base); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("got %v, want ErrIdempotencyConflict", err)
	}
}

func TestIdempotencyKeyIsScopedToUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := repository.New(pool)

	key := "integration-tenant-" + uuid.NewString()
	firstUserID := uuid.New()
	secondUserID := uuid.New()
	requestHash := sha256.Sum256([]byte("same-request-body"))

	first, firstCreated, err := store.Create(ctx, domain.CreateParams{
		IdempotencyKey: key,
		RequestHash:    requestHash[:],
		UserID:         firstUserID,
		Amount:         500,
		Currency:       "USD",
		Description:    "shared body",
		RequestID:      uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !firstCreated {
		t.Fatal("first tenant payment was not created")
	}

	second, secondCreated, err := store.Create(ctx, domain.CreateParams{
		IdempotencyKey: key,
		RequestHash:    requestHash[:],
		UserID:         secondUserID,
		Amount:         500,
		Currency:       "USD",
		Description:    "shared body",
		RequestID:      uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !secondCreated {
		t.Fatal("second tenant payment was not created")
	}
	if first.ID == second.ID {
		t.Fatalf("different tenants received the same payment %s", first.ID)
	}
	if first.UserID != firstUserID || second.UserID != secondUserID {
		t.Fatalf("payment crossed tenant boundary: first user=%s second user=%s", first.UserID, second.UserID)
	}

	secondRetry, retryCreated, err := store.Create(ctx, domain.CreateParams{
		IdempotencyKey: key,
		RequestHash:    requestHash[:],
		UserID:         secondUserID,
		Amount:         500,
		Currency:       "USD",
		Description:    "shared body",
		RequestID:      uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if retryCreated {
		t.Fatal("second tenant retry unexpectedly created another payment")
	}
	if secondRetry.ID != second.ID || secondRetry.UserID != secondUserID {
		t.Fatalf("retry returned foreign payment: got id=%s user=%s, want id=%s user=%s", secondRetry.ID, secondRetry.UserID, second.ID, secondUserID)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE idempotency_key=$1`, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("payments with shared key=%d, want 2", count)
	}
}

func TestOptimisticLockRejectsStaleVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, databaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := repository.New(pool)
	hash := sha256.Sum256([]byte(uuid.NewString()))
	payment, _, err := store.Create(ctx, domain.CreateParams{IdempotencyKey: uuid.NewString(), RequestHash: hash[:], UserID: uuid.New(), Amount: 700, Currency: "USD", RequestID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, payment.ID, payment.Version, domain.StatusProcessing, "", "", uuid.NewString(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, payment.ID, payment.Version, domain.StatusCancelled, "", "", uuid.NewString(), ""); !errors.Is(err, domain.ErrConcurrentModification) {
		t.Fatalf("got %v, want ErrConcurrentModification", err)
	}
}

func databaseURL() string {
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value
	}
	return "postgres://payment:payment@postgres:5432/payments?sslmode=disable"
}
