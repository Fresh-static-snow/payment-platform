package worker

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresInbox struct {
	pool *pgxpool.Pool
}

func NewPostgresInbox(pool *pgxpool.Pool) *PostgresInbox {
	return &PostgresInbox{pool: pool}
}

func (inbox *PostgresInbox) Processed(ctx context.Context, consumer string, eventID uuid.UUID) (bool, error) {
	var processed bool
	if err := inbox.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM consumer_inbox WHERE consumer=$1 AND event_id=$2
		)
	`, consumer, eventID).Scan(&processed); err != nil {
		return false, fmt.Errorf("query consumer inbox: %w", err)
	}
	return processed, nil
}

func (inbox *PostgresInbox) MarkProcessed(ctx context.Context, consumer string, eventID uuid.UUID) error {
	if _, err := inbox.pool.Exec(ctx, `
		INSERT INTO consumer_inbox(consumer,event_id,processed_at)
		VALUES($1,$2,now())
		ON CONFLICT(consumer,event_id) DO NOTHING
	`, consumer, eventID); err != nil {
		return fmt.Errorf("insert consumer inbox: %w", err)
	}
	return nil
}
