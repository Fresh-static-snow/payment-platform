package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/risk/domain"
)

func TestAssessIsIdempotent(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	service, err := New(repository, domain.DefaultPolicy())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	payment := domain.Payment{ID: uuid.New(), UserID: uuid.New(), Amount: 1_000, Currency: "USD"}

	first, err := service.Assess(context.Background(), payment)
	if err != nil {
		t.Fatalf("first Assess() error = %v", err)
	}
	changed := payment
	changed.Amount = 2_000_000
	second, err := service.Assess(context.Background(), changed)
	if err != nil {
		t.Fatalf("second Assess() error = %v", err)
	}

	if first.ID != second.ID || first.Score != second.Score || first.Decision != second.Decision {
		t.Fatalf("idempotent result changed: first=%#v second=%#v", first, second)
	}
	if repository.len() != 1 {
		t.Fatalf("repository contains %d decisions, want 1", repository.len())
	}
}

func TestAssessConcurrentRequestsCreateOneDecision(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	service, err := New(repository, domain.DefaultPolicy())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	payment := domain.Payment{ID: uuid.New(), UserID: uuid.New(), Amount: 100_000, Currency: "JPY"}

	const callers = 32
	results := make(chan domain.Assessment, callers)
	errorsChannel := make(chan error, callers)
	var waitGroup sync.WaitGroup
	for i := 0; i < callers; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			assessment, assessErr := service.Assess(context.Background(), payment)
			if assessErr != nil {
				errorsChannel <- assessErr
				return
			}
			results <- assessment
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsChannel)

	for assessErr := range errorsChannel {
		t.Errorf("Assess() error = %v", assessErr)
	}
	var decisionID uuid.UUID
	for result := range results {
		if decisionID == uuid.Nil {
			decisionID = result.ID
		}
		if result.ID != decisionID {
			t.Errorf("got decision ID %s, want %s", result.ID, decisionID)
		}
	}
	if repository.len() != 1 {
		t.Fatalf("repository contains %d decisions, want 1", repository.len())
	}
}

func TestAssessPropagatesCancellation(t *testing.T) {
	t.Parallel()
	repository := newMemoryRepository()
	service, err := New(repository, domain.DefaultPolicy())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = service.Assess(ctx, domain.Payment{ID: uuid.New(), UserID: uuid.New(), Amount: 1_000, Currency: "USD"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if repository.len() != 0 {
		t.Fatal("cancelled request reached repository")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	t.Parallel()
	if _, err := New(nil, domain.DefaultPolicy()); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("expected ErrInvalidConfiguration, got %v", err)
	}
}

type memoryRepository struct {
	mu          sync.Mutex
	assessments map[string]domain.Assessment
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{assessments: make(map[string]domain.Assessment)}
}

func (r *memoryRepository) GetOrCreate(_ context.Context, candidate domain.Assessment) (domain.Assessment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := candidate.PaymentID.String() + ":" + candidate.RulesVersion
	if existing, ok := r.assessments[key]; ok {
		return cloneAssessment(existing), nil
	}
	r.assessments[key] = cloneAssessment(candidate)
	return cloneAssessment(candidate), nil
}

func (r *memoryRepository) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.assessments)
}

func cloneAssessment(value domain.Assessment) domain.Assessment {
	value.Reasons = append([]string(nil), value.Reasons...)
	return value
}
