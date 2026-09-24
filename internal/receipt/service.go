package receipt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/internal/outbox"
	"github.com/praedyth/payment-platform/pkg/logging"
	"github.com/segmentio/kafka-go"
)

const consumerName = "receipt-service-v1"

type Service struct {
	pool   *pgxpool.Pool
	s3     *s3.Client
	bucket string
	logger *slog.Logger
}

func NewService(pool *pgxpool.Pool, client *s3.Client, bucket string, logger *slog.Logger) *Service {
	return &Service{pool: pool, s3: client, bucket: bucket, logger: logger}
}

type document struct {
	PaymentID uuid.UUID `json:"payment_id"`
	UserID    uuid.UUID `json:"user_id"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) Handle(ctx context.Context, message kafka.Message) error {
	var envelope events.Envelope
	if err := json.Unmarshal(message.Value, &envelope); err != nil {
		return fmt.Errorf("decode event envelope: %w", err)
	}
	if envelope.Type != events.ReceiptRequested {
		return fmt.Errorf("unexpected event type %q", envelope.Type)
	}
	processed, err := s.processed(ctx, envelope.ID)
	if err != nil {
		return err
	}
	if processed {
		return nil
	}
	var payload events.ReceiptPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return fmt.Errorf("decode receipt payload: %w", err)
	}
	if payload.PaymentID == uuid.Nil || payload.Status != "completed" {
		return errors.New("receipt payload must reference a completed payment")
	}
	key := "receipts/" + payload.PaymentID.String() + ".json"
	body, err := json.Marshal(document{
		PaymentID: payload.PaymentID, UserID: payload.UserID, Amount: payload.Amount,
		Currency: payload.Currency, Status: payload.Status, CreatedAt: envelope.OccurredAt,
	})
	if err != nil {
		return fmt.Errorf("encode receipt: %w", err)
	}
	if _, err := s.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	}); err != nil {
		return fmt.Errorf("put receipt in S3: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin receipt transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `INSERT INTO consumer_inbox(consumer,event_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, consumerName, envelope.ID)
	if err != nil {
		return fmt.Errorf("insert receipt inbox: %w", err)
	}
	if command.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO receipts(payment_id,event_id,object_key,content,created_at)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(payment_id) DO UPDATE SET object_key=excluded.object_key, content=excluded.content`,
		payload.PaymentID, envelope.ID, key, string(body), envelope.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("save receipt metadata: %w", err)
	}
	createdPayload := payload
	createdPayload.ObjectKey = key
	createdEvent, err := events.NewContext(ctx, events.ReceiptCreated, envelope.CorrelationID, envelope.ID.String(), createdPayload)
	if err != nil {
		return err
	}
	if err := outbox.Insert(ctx, tx, payload.PaymentID, createdEvent); err != nil {
		return fmt.Errorf("insert receipt outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit receipt transaction: %w", err)
	}
	logging.WithContext(ctx, s.logger).Info("receipt created", "payment_id", payload.PaymentID, "object_key", key, "event_type", createdEvent.Type)
	return nil
}

func (s *Service) processed(ctx context.Context, eventID uuid.UUID) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM consumer_inbox WHERE consumer=$1 AND event_id=$2)`, consumerName, eventID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check receipt inbox: %w", err)
	}
	return exists, nil
}
