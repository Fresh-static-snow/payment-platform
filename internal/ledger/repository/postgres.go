package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/praedyth/payment-platform/internal/ledger/domain"
)

var ErrJournalConflict = errors.New("ledger journal conflicts with an existing reference journal")

type transactionStarter interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

type Postgres struct {
	db transactionStarter
}

func NewPostgres(db transactionStarter) *Postgres {
	return &Postgres{db: db}
}

// Store atomically claims the event in the consumer inbox and writes the
// journal with all of its entries. A rollback therefore makes the Kafka event
// eligible for a complete retry, never a partially posted journal.
func (r *Postgres) Store(ctx context.Context, consumer string, journal domain.Journal) (bool, error) {
	if strings.TrimSpace(consumer) == "" {
		return false, fmt.Errorf("consumer name is required")
	}
	if err := journal.Validate(); err != nil {
		return false, fmt.Errorf("validate journal: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin ledger transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	claim, err := tx.Exec(ctx, `
		INSERT INTO consumer_inbox (event_id, consumer, processed_at)
		VALUES ($1, $2, now())
		ON CONFLICT (consumer, event_id) DO NOTHING
	`, journal.EventID, consumer)
	if err != nil {
		return false, fmt.Errorf("claim ledger event %s: %w", journal.EventID, err)
	}
	if claim.RowsAffected() == 0 {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit duplicate ledger event %s: %w", journal.EventID, err)
		}
		return false, nil
	}

	var journalID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO ledger_journals (
			event_id,payment_id,reference_type,reference_id,currency,amount,created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (reference_type,reference_id) DO NOTHING
		RETURNING id
	`, journal.EventID, journal.PaymentID, journal.ReferenceType, journal.ReferenceID,
		journal.Currency, journal.Amount, journal.CreatedAt).Scan(&journalID)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := validateExistingJournal(ctx, tx, journal); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit duplicate %s journal %s: %w", journal.ReferenceType, journal.ReferenceID, err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert ledger journal for %s %s: %w", journal.ReferenceType, journal.ReferenceID, err)
	}

	for _, entry := range journal.Entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ledger_entries
				(journal_id, account, direction, amount, currency, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, journalID, entry.Account, string(entry.Direction), entry.Amount, entry.Currency, entry.CreatedAt); err != nil {
			return false, fmt.Errorf("insert ledger entry for journal %s: %w", journalID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit ledger journal %s: %w", journalID, err)
	}
	return true, nil
}

func validateExistingJournal(ctx context.Context, tx pgx.Tx, requested domain.Journal) error {
	var (
		eventID   uuid.UUID
		paymentID uuid.UUID
		currency  string
		amount    int64
	)
	if err := tx.QueryRow(ctx, `
		SELECT event_id, payment_id, currency, amount
		FROM ledger_journals
		WHERE reference_type=$1 AND reference_id=$2
	`, requested.ReferenceType, requested.ReferenceID).Scan(&eventID, &paymentID, &currency, &amount); err != nil {
		return fmt.Errorf("load existing journal for %s %s: %w", requested.ReferenceType, requested.ReferenceID, err)
	}
	if paymentID != requested.PaymentID || currency != requested.Currency || amount != requested.Amount {
		return fmt.Errorf(
			"%w: reference=%s/%s existing_event=%s requested_event=%s",
			ErrJournalConflict,
			requested.ReferenceType,
			requested.ReferenceID,
			eventID,
			requested.EventID,
		)
	}
	return nil
}
