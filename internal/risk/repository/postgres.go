package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/praedyth/payment-platform/internal/risk/domain"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// GetOrCreate relies on UNIQUE(payment_id, rules_version) as the source of
// truth. ON CONFLICT resolves concurrent inserts without a process-local lock.
// The follow-up SELECT is intentionally a separate statement: under PostgreSQL
// READ COMMITTED it receives a fresh snapshot and can see the winning row.
func (r *Postgres) GetOrCreate(ctx context.Context, candidate domain.Assessment) (domain.Assessment, error) {
	if r == nil || r.pool == nil {
		return domain.Assessment{}, errors.New("risk repository has no database pool")
	}
	if err := candidate.Validate(); err != nil {
		return domain.Assessment{}, err
	}
	reasons, err := json.Marshal(candidate.Reasons)
	if err != nil {
		return domain.Assessment{}, fmt.Errorf("marshal risk reasons: %w", err)
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO risk_decisions (
			id, payment_id, rules_version, score, decision, reasons, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (payment_id, rules_version) DO NOTHING
		RETURNING id, payment_id, rules_version, score, decision, reasons, created_at`,
		candidate.ID,
		candidate.PaymentID,
		candidate.RulesVersion,
		candidate.Score,
		candidate.Decision,
		string(reasons),
		candidate.CreatedAt,
	)
	assessment, err := scanAssessment(row)
	if err == nil {
		return assessment, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Assessment{}, fmt.Errorf("insert risk decision: %w", err)
	}

	assessment, err = r.find(ctx, candidate.PaymentID, candidate.RulesVersion)
	if err != nil {
		return domain.Assessment{}, fmt.Errorf("load concurrent risk decision: %w", err)
	}
	return assessment, nil
}

func (r *Postgres) find(ctx context.Context, paymentID uuid.UUID, rulesVersion string) (domain.Assessment, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, payment_id, rules_version, score, decision, reasons, created_at
		FROM risk_decisions
		WHERE payment_id = $1 AND rules_version = $2`, paymentID, rulesVersion)
	assessment, err := scanAssessment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Assessment{}, domain.ErrDecisionNotFound
	}
	if err != nil {
		return domain.Assessment{}, fmt.Errorf("query risk decision: %w", err)
	}
	return assessment, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAssessment(row scanner) (domain.Assessment, error) {
	var assessment domain.Assessment
	var rawReasons []byte
	if err := row.Scan(
		&assessment.ID,
		&assessment.PaymentID,
		&assessment.RulesVersion,
		&assessment.Score,
		&assessment.Decision,
		&rawReasons,
		&assessment.CreatedAt,
	); err != nil {
		return domain.Assessment{}, err
	}
	if err := json.Unmarshal(rawReasons, &assessment.Reasons); err != nil {
		return domain.Assessment{}, fmt.Errorf("decode risk reasons: %w", err)
	}
	if err := assessment.Validate(); err != nil {
		return domain.Assessment{}, fmt.Errorf("validate stored risk decision: %w", err)
	}
	return assessment, nil
}
