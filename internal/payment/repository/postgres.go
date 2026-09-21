package repository

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/internal/payment/domain"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const paymentColumns = `id, idempotency_key, user_id, amount, currency, description,
status, provider_reference, failure_reason, created_at, updated_at, version`

func scanPayment(row pgx.Row) (domain.Payment, error) {
	var payment domain.Payment
	err := row.Scan(
		&payment.ID, &payment.IdempotencyKey, &payment.UserID, &payment.Amount,
		&payment.Currency, &payment.Description, &payment.Status,
		&payment.ProviderReference, &payment.FailureReason, &payment.CreatedAt,
		&payment.UpdatedAt, &payment.Version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	if err != nil {
		return domain.Payment{}, fmt.Errorf("scan payment: %w", err)
	}
	return payment, nil
}

// Create inserts the aggregate and its first event in one database transaction.
// The user-scoped unique idempotency key is the cross-instance arbiter; Redis
// is never used for correctness here.
func (s *Store) Create(ctx context.Context, params domain.CreateParams) (domain.Payment, bool, error) {
	if err := params.Validate(); err != nil {
		return domain.Payment{}, false, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("begin create payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := uuid.New()
	row := tx.QueryRow(ctx, `
		INSERT INTO payments (id, idempotency_key, request_hash, user_id, amount, currency, description, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING `+paymentColumns,
		id, params.IdempotencyKey, params.RequestHash, params.UserID, params.Amount,
		strings.ToUpper(params.Currency), params.Description, domain.StatusPending,
	)
	payment, scanErr := scanPayment(row)
	created := scanErr == nil
	if errors.Is(scanErr, domain.ErrPaymentNotFound) {
		var existingHash []byte
		row = tx.QueryRow(ctx, `SELECT `+paymentColumns+`, request_hash FROM payments WHERE user_id=$1 AND idempotency_key=$2`, params.UserID, params.IdempotencyKey)
		var existing domain.Payment
		err = row.Scan(
			&existing.ID, &existing.IdempotencyKey, &existing.UserID, &existing.Amount,
			&existing.Currency, &existing.Description, &existing.Status,
			&existing.ProviderReference, &existing.FailureReason, &existing.CreatedAt,
			&existing.UpdatedAt, &existing.Version, &existingHash,
		)
		if err != nil {
			return domain.Payment{}, false, fmt.Errorf("load idempotent payment: %w", err)
		}
		if !bytes.Equal(existingHash, params.RequestHash) {
			return domain.Payment{}, false, domain.ErrIdempotencyConflict
		}
		payment = existing
	} else if scanErr != nil {
		return domain.Payment{}, false, fmt.Errorf("insert payment: %w", scanErr)
	}

	if created {
		payload := events.PaymentPayload{
			PaymentID: payment.ID, UserID: payment.UserID, Amount: payment.Amount,
			Currency: payment.Currency, Description: payment.Description, Status: string(payment.Status),
			AggregateVersion: payment.Version, CreatedAt: payment.CreatedAt, UpdatedAt: payment.UpdatedAt,
		}
		envelope, err := events.NewContext(ctx, events.PaymentCreated, params.RequestID, "", payload)
		if err != nil {
			return domain.Payment{}, false, err
		}
		if err := insertOutbox(ctx, tx, payment.ID, envelope); err != nil {
			return domain.Payment{}, false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Payment{}, false, fmt.Errorf("commit create payment: %w", err)
	}
	return payment, created, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (domain.Payment, error) {
	payment, err := scanPayment(s.pool.QueryRow(ctx, `SELECT `+paymentColumns+` FROM payments WHERE id=$1`, id))
	if err != nil {
		return domain.Payment{}, err
	}
	return payment, nil
}

type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

func EncodeCursor(cursor Cursor) string {
	value := cursor.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + cursor.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func DecodeCursor(value string) (Cursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return Cursor{}, fmt.Errorf("decode cursor: invalid format")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return Cursor{}, fmt.Errorf("decode cursor time: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return Cursor{}, fmt.Errorf("decode cursor id: %w", err)
	}
	return Cursor{CreatedAt: createdAt, ID: id}, nil
}

func (s *Store) List(ctx context.Context, userID uuid.UUID, limit int, cursor *Cursor) ([]domain.Payment, error) {
	if limit < 1 || limit > 101 {
		return nil, fmt.Errorf("limit must be between 1 and 101")
	}
	query := `SELECT ` + paymentColumns + ` FROM payments WHERE user_id=$1`
	args := []any{userID}
	if cursor != nil {
		query += ` AND (created_at, id) < ($2, $3)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list payments: %w", err)
	}
	defer rows.Close()
	result := make([]domain.Payment, 0, limit)
	for rows.Next() {
		payment, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, payment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate payments: %w", err)
	}
	return result, nil
}

// Transition applies optimistic locking and appends the resulting domain event
// atomically. A caller holding a stale version receives ErrConcurrentModification.
func (s *Store) Transition(
	ctx context.Context,
	id uuid.UUID,
	expectedVersion int64,
	to domain.Status,
	providerReference string,
	failureReason string,
	requestID string,
	causationID string,
) (domain.Payment, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Payment{}, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanPayment(tx.QueryRow(ctx, `SELECT `+paymentColumns+` FROM payments WHERE id=$1`, id))
	if err != nil {
		return domain.Payment{}, err
	}
	if current.Version != expectedVersion {
		return domain.Payment{}, domain.ErrConcurrentModification
	}
	if err := current.Transition(to); err != nil {
		return domain.Payment{}, err
	}
	current.ProviderReference = providerReference
	current.FailureReason = failureReason
	updated, err := scanPayment(tx.QueryRow(ctx, `
		UPDATE payments SET status=$1, provider_reference=$2, failure_reason=$3,
			updated_at=now(), version=version+1
		WHERE id=$4 AND version=$5
		RETURNING `+paymentColumns,
		to, providerReference, failureReason, id, expectedVersion,
	))
	if errors.Is(err, domain.ErrPaymentNotFound) {
		return domain.Payment{}, domain.ErrConcurrentModification
	}
	if err != nil {
		return domain.Payment{}, fmt.Errorf("update payment: %w", err)
	}

	eventType, ok := eventForStatus(to)
	if !ok {
		return domain.Payment{}, fmt.Errorf("no event for status %s", to)
	}
	payload := events.PaymentPayload{
		PaymentID: updated.ID, UserID: updated.UserID, Amount: updated.Amount,
		Currency: updated.Currency, Description: updated.Description, Status: string(updated.Status),
		ProviderReference: updated.ProviderReference, FailureReason: updated.FailureReason,
		AggregateVersion: updated.Version, CreatedAt: updated.CreatedAt, UpdatedAt: updated.UpdatedAt,
	}
	envelope, err := events.NewContext(ctx, eventType, requestID, causationID, payload)
	if err != nil {
		return domain.Payment{}, err
	}
	if err := insertOutbox(ctx, tx, updated.ID, envelope); err != nil {
		return domain.Payment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Payment{}, fmt.Errorf("commit transition: %w", err)
	}
	return updated, nil
}

func (s *Store) Cancel(ctx context.Context, id, userID uuid.UUID, requestID string) (domain.Payment, error) {
	payment, err := s.Get(ctx, id)
	if err != nil {
		return domain.Payment{}, err
	}
	if payment.UserID != userID {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return s.Transition(ctx, id, payment.Version, domain.StatusCancelled, "", "", requestID, "")
}

func (s *Store) RequestReceipt(ctx context.Context, id, userID uuid.UUID, requestID string) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin receipt request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	payment, err := scanPayment(tx.QueryRow(ctx, `SELECT `+paymentColumns+` FROM payments WHERE id=$1 AND user_id=$2`, id, userID))
	if err != nil {
		return false, err
	}
	if payment.Status != domain.StatusCompleted {
		return false, fmt.Errorf("%w: receipt requires completed payment", domain.ErrInvalidStatusTransition)
	}
	eventID := uuid.New()
	command, err := tx.Exec(ctx, `INSERT INTO receipt_requests(payment_id,event_id) VALUES($1,$2) ON CONFLICT(payment_id) DO NOTHING`, id, eventID)
	if err != nil {
		return false, fmt.Errorf("insert receipt request: %w", err)
	}
	created := command.RowsAffected() == 1
	if created {
		payload := events.ReceiptPayload{PaymentID: id, UserID: payment.UserID, Amount: payment.Amount, Currency: payment.Currency, Status: string(payment.Status)}
		envelope, err := events.NewContext(ctx, events.ReceiptRequested, requestID, "", payload)
		if err != nil {
			return false, err
		}
		envelope.ID = eventID
		if err := insertOutbox(ctx, tx, id, envelope); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit receipt request: %w", err)
	}
	return created, nil
}

func insertOutbox(ctx context.Context, tx pgx.Tx, aggregateID uuid.UUID, envelope events.Envelope) error {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal outbox envelope: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events(id, aggregate_id, event_type, payload, request_id)
		VALUES($1,$2,$3,$4,$5)`,
		envelope.ID, aggregateID, envelope.Type, string(payload), envelope.CorrelationID,
	)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

func eventForStatus(status domain.Status) (string, bool) {
	switch status {
	case domain.StatusProcessing:
		return events.PaymentProcessing, true
	case domain.StatusCompleted:
		return events.PaymentCompleted, true
	case domain.StatusFailed:
		return events.PaymentFailed, true
	case domain.StatusCancelled:
		return events.PaymentCancelled, true
	default:
		return "", false
	}
}
