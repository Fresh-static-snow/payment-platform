package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/praedyth/payment-platform/internal/risk/domain"
)

var ErrInvalidConfiguration = errors.New("invalid risk service configuration")

// Repository owns the atomic idempotency boundary. Implementations must return
// the already persisted row when another caller wins a concurrent insert for
// the same (payment_id, rules_version) pair.
type Repository interface {
	GetOrCreate(ctx context.Context, assessment domain.Assessment) (domain.Assessment, error)
}

type Evaluator interface {
	RulesVersion() string
	Evaluate(payment domain.Payment) (domain.Evaluation, error)
}

type Service struct {
	repository Repository
	evaluator  Evaluator
	now        func() time.Time
}

func New(repository Repository, evaluator Evaluator) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: repository is required", ErrInvalidConfiguration)
	}
	if evaluator == nil {
		return nil, fmt.Errorf("%w: evaluator is required", ErrInvalidConfiguration)
	}
	if evaluator.RulesVersion() == "" {
		return nil, fmt.Errorf("%w: evaluator rules version is required", ErrInvalidConfiguration)
	}
	return &Service{
		repository: repository,
		evaluator:  evaluator,
		now:        func() time.Time { return time.Now().UTC() },
	}, nil
}

func (s *Service) Assess(ctx context.Context, payment domain.Payment) (domain.Assessment, error) {
	if err := ctx.Err(); err != nil {
		return domain.Assessment{}, fmt.Errorf("assess payment risk: %w", err)
	}
	evaluation, err := s.evaluator.Evaluate(payment)
	if err != nil {
		return domain.Assessment{}, fmt.Errorf("evaluate payment risk: %w", err)
	}

	candidate := domain.Assessment{
		PaymentID:    payment.ID,
		RulesVersion: s.evaluator.RulesVersion(),
		Score:        evaluation.Score,
		Decision:     evaluation.Decision,
		Reasons:      append([]string(nil), evaluation.Reasons...),
		CreatedAt:    s.now(),
	}
	if err := candidate.ValidateForCreate(); err != nil {
		return domain.Assessment{}, fmt.Errorf("build risk decision: %w", err)
	}

	assessment, err := s.repository.GetOrCreate(ctx, candidate)
	if err != nil {
		return domain.Assessment{}, fmt.Errorf("persist risk decision: %w", err)
	}
	return assessment, nil
}
