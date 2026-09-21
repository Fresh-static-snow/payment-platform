package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/praedyth/payment-platform/internal/events"
	paymentdomain "github.com/praedyth/payment-platform/internal/payment/domain"
	refunddomain "github.com/praedyth/payment-platform/internal/refund/domain"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const refundColumns = `id,payment_id,user_id,amount,currency,status,provider_reference,
failure_reason,workflow_id,workflow_started_at,created_at,updated_at,version`

func scanRefund(row pgx.Row) (refunddomain.Refund, error) {
	var refund refunddomain.Refund
	err := row.Scan(
		&refund.ID, &refund.PaymentID, &refund.UserID, &refund.Amount, &refund.Currency,
		&refund.Status, &refund.ProviderReference, &refund.FailureReason, &refund.WorkflowID,
		&refund.WorkflowStartedAt, &refund.CreatedAt, &refund.UpdatedAt, &refund.Version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return refunddomain.Refund{}, refunddomain.ErrRefundNotFound
	}
	if err != nil {
		return refunddomain.Refund{}, fmt.Errorf("scan refund: %w", err)
	}
	return refund, nil
}

// Create locks the payment while checking the already reserved refund amount.
// Pending and processing refunds count toward the reservation, preventing two
// concurrent requests from over-refunding the same payment.
func (s *Store) Create(ctx context.Context, params refunddomain.CreateParams) (refunddomain.Refund, bool, error) {
	if err := params.Validate(); err != nil {
		return refunddomain.Refund{}, false, err
	}
	params.IdempotencyKey = strings.TrimSpace(params.IdempotencyKey)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return refunddomain.Refund{}, false, fmt.Errorf("begin refund creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Return a persisted response before re-evaluating mutable business rules.
	// Without this check, a valid retry can be rejected as over-refunded after a
	// later refund consumed the remaining amount.
	if existing, found, err := loadIdempotentRefund(ctx, tx, params); err != nil {
		return refunddomain.Refund{}, false, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return refunddomain.Refund{}, false, fmt.Errorf("commit idempotent refund read: %w", err)
		}
		return existing, false, nil
	}

	var (
		paymentUserID uuid.UUID
		paymentAmount int64
		currency      string
		paymentStatus paymentdomain.Status
	)
	err = tx.QueryRow(ctx, `
		SELECT user_id,amount,currency,status
		FROM payments
		WHERE id=$1
		FOR UPDATE
	`, params.PaymentID).Scan(&paymentUserID, &paymentAmount, &currency, &paymentStatus)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && paymentUserID != params.UserID) {
		return refunddomain.Refund{}, false, refunddomain.ErrRefundNotFound
	}
	if err != nil {
		return refunddomain.Refund{}, false, fmt.Errorf("lock refundable payment: %w", err)
	}
	if paymentStatus != paymentdomain.StatusCompleted {
		return refunddomain.Refund{}, false, fmt.Errorf("%w: payment must be completed", refunddomain.ErrInvalidRefund)
	}

	// A concurrent request for the same payment may have committed while this
	// transaction waited for the payment row lock.
	if existing, found, err := loadIdempotentRefund(ctx, tx, params); err != nil {
		return refunddomain.Refund{}, false, err
	} else if found {
		if err := tx.Commit(ctx); err != nil {
			return refunddomain.Refund{}, false, fmt.Errorf("commit concurrent idempotent refund read: %w", err)
		}
		return existing, false, nil
	}

	var reserved int64
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(amount),0)
		FROM refunds
		WHERE payment_id=$1 AND status IN ('pending','processing','completed')
	`, params.PaymentID).Scan(&reserved); err != nil {
		return refunddomain.Refund{}, false, fmt.Errorf("sum reserved refunds: %w", err)
	}
	if params.Amount > paymentAmount-reserved {
		return refunddomain.Refund{}, false, refunddomain.ErrRefundAmountExceeded
	}

	id := uuid.New()
	workflowID := "refund:" + id.String()
	refund, scanErr := scanRefund(tx.QueryRow(ctx, `
		INSERT INTO refunds(
			id,payment_id,user_id,idempotency_key,request_hash,amount,currency,status,workflow_id
		)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(user_id,idempotency_key) DO NOTHING
		RETURNING `+refundColumns,
		id, params.PaymentID, params.UserID, params.IdempotencyKey, params.RequestHash,
		params.Amount, currency, refunddomain.StatusPending, workflowID,
	))
	created := scanErr == nil
	if errors.Is(scanErr, refunddomain.ErrRefundNotFound) {
		var found bool
		refund, found, err = loadIdempotentRefund(ctx, tx, params)
		if err != nil {
			return refunddomain.Refund{}, false, err
		}
		if !found {
			return refunddomain.Refund{}, false, errors.New("idempotency conflict row disappeared")
		}
	} else if scanErr != nil {
		return refunddomain.Refund{}, false, fmt.Errorf("insert refund: %w", scanErr)
	}

	if created {
		if err := insertRefundOutbox(ctx, tx, refund, events.RefundRequested, params.RequestID, ""); err != nil {
			return refunddomain.Refund{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return refunddomain.Refund{}, false, fmt.Errorf("commit refund creation: %w", err)
	}
	return refund, created, nil
}

func loadIdempotentRefund(
	ctx context.Context,
	tx pgx.Tx,
	params refunddomain.CreateParams,
) (refunddomain.Refund, bool, error) {
	var (
		refund       refunddomain.Refund
		existingHash []byte
	)
	err := tx.QueryRow(ctx, `SELECT `+refundColumns+`,request_hash
		FROM refunds WHERE user_id=$1 AND idempotency_key=$2`,
		params.UserID, params.IdempotencyKey,
	).Scan(
		&refund.ID, &refund.PaymentID, &refund.UserID, &refund.Amount, &refund.Currency,
		&refund.Status, &refund.ProviderReference, &refund.FailureReason, &refund.WorkflowID,
		&refund.WorkflowStartedAt, &refund.CreatedAt, &refund.UpdatedAt, &refund.Version,
		&existingHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return refunddomain.Refund{}, false, nil
	}
	if err != nil {
		return refunddomain.Refund{}, false, fmt.Errorf("load idempotent refund: %w", err)
	}
	if refund.PaymentID != params.PaymentID || refund.Amount != params.Amount || !bytes.Equal(existingHash, params.RequestHash) {
		return refunddomain.Refund{}, false, refunddomain.ErrIdempotencyConflict
	}
	return refund, true, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (refunddomain.Refund, error) {
	return scanRefund(s.pool.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds WHERE id=$1`, id))
}

func (s *Store) GetForUser(ctx context.Context, id, userID uuid.UUID) (refunddomain.Refund, error) {
	return scanRefund(s.pool.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds WHERE id=$1 AND user_id=$2`, id, userID))
}

func (s *Store) MarkWorkflowStarted(ctx context.Context, id uuid.UUID) error {
	command, err := s.pool.Exec(ctx, `
		UPDATE refunds SET workflow_started_at=coalesce(workflow_started_at,now()) WHERE id=$1
	`, id)
	if err != nil {
		return fmt.Errorf("mark refund workflow started: %w", err)
	}
	if command.RowsAffected() == 0 {
		return refunddomain.ErrRefundNotFound
	}
	return nil
}

func (s *Store) ListUnstarted(ctx context.Context, limit int) ([]refunddomain.Refund, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+refundColumns+`
		FROM refunds
		WHERE workflow_started_at IS NULL AND status='pending'
		ORDER BY created_at,id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unstarted refund workflows: %w", err)
	}
	defer rows.Close()
	refunds := make([]refunddomain.Refund, 0, limit)
	for rows.Next() {
		refund, err := scanRefund(rows)
		if err != nil {
			return nil, err
		}
		refunds = append(refunds, refund)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unstarted refunds: %w", err)
	}
	return refunds, nil
}

// Transition is idempotent for retries from Temporal activities. A terminal
// state is never regressed when an earlier activity is replayed.
func (s *Store) Transition(
	ctx context.Context,
	id uuid.UUID,
	to refunddomain.Status,
	providerReference string,
	failureReason string,
	requestID string,
) (refunddomain.Refund, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return refunddomain.Refund{}, fmt.Errorf("begin refund transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanRefund(tx.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return refunddomain.Refund{}, err
	}
	if err := validateTransitionDetails(to, providerReference, failureReason); err != nil {
		return refunddomain.Refund{}, err
	}
	if current.Status == to {
		if to == refunddomain.StatusCompleted && current.ProviderReference != providerReference {
			return refunddomain.Refund{}, refunddomain.ErrTransitionConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return refunddomain.Refund{}, fmt.Errorf("commit idempotent refund transition: %w", err)
		}
		return current, nil
	}
	if current.Status == refunddomain.StatusCompleted || current.Status == refunddomain.StatusFailed {
		return refunddomain.Refund{}, fmt.Errorf(
			"%w: terminal state %s cannot transition to %s",
			refunddomain.ErrInvalidStatusTransition,
			current.Status,
			to,
		)
	}
	if err := current.ValidateTransition(to); err != nil {
		return refunddomain.Refund{}, err
	}
	updated, err := scanRefund(tx.QueryRow(ctx, `
		UPDATE refunds
		SET status=$2,provider_reference=$3,failure_reason=$4,updated_at=now(),version=version+1
		WHERE id=$1
		RETURNING `+refundColumns,
		id, to, providerReference, failureReason,
	))
	if err != nil {
		return refunddomain.Refund{}, fmt.Errorf("update refund transition: %w", err)
	}
	eventType := map[refunddomain.Status]string{
		refunddomain.StatusProcessing: events.RefundProcessing,
		refunddomain.StatusCompleted:  events.RefundCompleted,
		refunddomain.StatusFailed:     events.RefundFailed,
	}[to]
	if eventType == "" {
		return refunddomain.Refund{}, fmt.Errorf("no event type for refund status %q", to)
	}
	if err := insertRefundOutbox(ctx, tx, updated, eventType, requestID, ""); err != nil {
		return refunddomain.Refund{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return refunddomain.Refund{}, fmt.Errorf("commit refund transition: %w", err)
	}
	return updated, nil
}

func validateTransitionDetails(to refunddomain.Status, providerReference, failureReason string) error {
	switch to {
	case refunddomain.StatusProcessing:
		if providerReference != "" || failureReason != "" {
			return fmt.Errorf("%w: processing transition cannot contain terminal details", refunddomain.ErrInvalidRefund)
		}
	case refunddomain.StatusCompleted:
		if strings.TrimSpace(providerReference) == "" || failureReason != "" {
			return fmt.Errorf("%w: completed transition requires only a provider reference", refunddomain.ErrInvalidRefund)
		}
	case refunddomain.StatusFailed:
		if providerReference != "" || strings.TrimSpace(failureReason) == "" {
			return fmt.Errorf("%w: failed transition requires only a failure reason", refunddomain.ErrInvalidRefund)
		}
	default:
		return fmt.Errorf("%w: unsupported target status %s", refunddomain.ErrInvalidStatusTransition, to)
	}
	return nil
}

func insertRefundOutbox(ctx context.Context, tx pgx.Tx, refund refunddomain.Refund, eventType, requestID, causationID string) error {
	payload := events.RefundPayload{
		RefundID: refund.ID, PaymentID: refund.PaymentID, UserID: refund.UserID,
		Amount: refund.Amount, Currency: refund.Currency, Status: string(refund.Status),
		AggregateVersion: refund.Version, ProviderReference: refund.ProviderReference,
		FailureReason: refund.FailureReason, CreatedAt: refund.CreatedAt, UpdatedAt: refund.UpdatedAt,
	}
	envelope, err := events.NewContext(ctx, eventType, requestID, causationID, payload)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal refund event: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events(id,aggregate_id,event_type,payload,request_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6)
	`, envelope.ID, refund.ID, envelope.Type, string(raw), requestID, envelope.OccurredAt)
	if err != nil {
		return fmt.Errorf("insert refund outbox event: %w", err)
	}
	return nil
}
