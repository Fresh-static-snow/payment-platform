package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/praedyth/payment-platform/internal/events"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/segmentio/kafka-go"
)

type Relay struct {
	pool     *pgxpool.Pool
	writer   *kafka.Writer
	logger   *slog.Logger
	metrics  *platformmetrics.Metrics
	interval time.Duration
	batch    int
}

type event struct {
	ID          uuid.UUID
	AggregateID uuid.UUID
	Type        string
	Payload     []byte
	RequestID   string
}

// Insert persists an event envelope and lets PostgreSQL assign its UUIDv7.
// For envelopes without an ID, the same database-generated value is written
// into both outbox_events.id and payload.event_id in one statement.
func Insert(ctx context.Context, tx pgx.Tx, aggregateID uuid.UUID, envelope events.Envelope) error {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal outbox envelope: %w", err)
	}
	if envelope.ID != uuid.Nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO outbox_events(id, aggregate_id, event_type, payload, request_id)
			VALUES($1,$2,$3,$4,$5)`,
			envelope.ID, aggregateID, envelope.Type, string(payload), envelope.CorrelationID,
		)
	} else {
		_, err = tx.Exec(ctx, `
			WITH new_uuid AS MATERIALIZED (SELECT uuidv7() AS id)
			INSERT INTO outbox_events(id, aggregate_id, event_type, payload, request_id)
			SELECT id, $1, $2,
				jsonb_set($3::jsonb, '{event_id}', to_jsonb(id::text), true), $4
			FROM new_uuid`,
			aggregateID, envelope.Type, string(payload), envelope.CorrelationID,
		)
	}
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

func NewRelay(pool *pgxpool.Pool, writer *kafka.Writer, logger *slog.Logger, metrics *platformmetrics.Metrics) *Relay {
	return &Relay{pool: pool, writer: writer, logger: logger, metrics: metrics, interval: 250 * time.Millisecond, batch: 50}
}

func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.publishBatch(ctx); err != nil && ctx.Err() == nil {
			r.logger.Error("outbox batch failed", "error", err)
		}
		if err := r.updatePendingMetric(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("outbox metric query failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Relay) publishBatch(ctx context.Context) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin outbox batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT id, aggregate_id, event_type, payload, request_id
		FROM outbox_events
		WHERE published_at IS NULL AND attempts < 20
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, r.batch)
	if err != nil {
		return fmt.Errorf("select outbox batch: %w", err)
	}
	batch := make([]event, 0, r.batch)
	for rows.Next() {
		var item event
		if err := rows.Scan(&item.ID, &item.AggregateID, &item.Type, &item.Payload, &item.RequestID); err != nil {
			rows.Close()
			return fmt.Errorf("scan outbox event: %w", err)
		}
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate outbox events: %w", err)
	}
	rows.Close()

	for _, item := range batch {
		headers := []kafka.Header{
			{Key: "event_id", Value: []byte(item.ID.String())},
			{Key: "request_id", Value: []byte(item.RequestID)},
		}
		var envelope events.Envelope
		if err := json.Unmarshal(item.Payload, &envelope); err == nil {
			for key, value := range envelope.TraceContext {
				headers = append(headers, kafka.Header{Key: key, Value: []byte(value)})
			}
		}
		message := kafka.Message{
			Topic:   item.Type,
			Key:     []byte(item.AggregateID.String()),
			Value:   item.Payload,
			Headers: headers,
			Time:    time.Now().UTC(),
		}
		publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := r.writer.WriteMessages(publishCtx, message)
		cancel()
		if err != nil {
			_, updateErr := tx.Exec(ctx, `UPDATE outbox_events SET attempts=attempts+1, last_error=$2 WHERE id=$1`, item.ID, err.Error())
			if updateErr != nil {
				return fmt.Errorf("record outbox failure: %w", updateErr)
			}
			r.logger.Warn("outbox publish failed", "event_id", item.ID, "event_type", item.Type, "error", err)
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox_events SET published_at=now(), attempts=attempts+1, last_error='' WHERE id=$1`, item.ID); err != nil {
			return fmt.Errorf("mark outbox published: %w", err)
		}
		r.logger.Info("outbox event published", "event_id", item.ID, "event_type", item.Type, "request_id", item.RequestID)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit outbox batch: %w", err)
	}
	return nil
}

func (r *Relay) updatePendingMetric(ctx context.Context) error {
	var count float64
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&count); err != nil {
		return err
	}
	r.metrics.OutboxPending.Set(count)
	return nil
}
