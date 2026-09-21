package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/pkg/logging"
	"github.com/segmentio/kafka-go"
)

const consumerName = "notification-service-v1"

type Service struct {
	pool     *pgxpool.Pool
	sns      *sns.Client
	sqs      *sqs.Client
	topicARN string
	queueURL string
	logger   *slog.Logger
}

func NewService(pool *pgxpool.Pool, snsClient *sns.Client, sqsClient *sqs.Client, topicARN, queueURL string, logger *slog.Logger) *Service {
	return &Service{pool: pool, sns: snsClient, sqs: sqsClient, topicARN: topicARN, queueURL: queueURL, logger: logger}
}

type deliveryPayload struct {
	DeliveryID uuid.UUID `json:"delivery_id"`
	PaymentID  uuid.UUID `json:"payment_id"`
	UserID     uuid.UUID `json:"user_id"`
	Kind       string    `json:"kind"`
	RequestID  string    `json:"request_id,omitempty"`
}

func (s *Service) HandlePaymentCompleted(ctx context.Context, message kafka.Message) error {
	var envelope events.Envelope
	if err := json.Unmarshal(message.Value, &envelope); err != nil {
		return fmt.Errorf("decode notification event: %w", err)
	}
	if envelope.Type != events.PaymentCompleted {
		return fmt.Errorf("unexpected event type %q", envelope.Type)
	}
	var payment events.PaymentPayload
	if err := json.Unmarshal(envelope.Payload, &payment); err != nil {
		return fmt.Errorf("decode payment payload: %w", err)
	}
	delivery := deliveryPayload{
		DeliveryID: uuid.New(), PaymentID: payment.PaymentID, UserID: payment.UserID,
		Kind: "payment_completed", RequestID: envelope.CorrelationID,
	}
	payload, err := json.Marshal(delivery)
	if err != nil {
		return fmt.Errorf("encode notification delivery: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin notification transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `INSERT INTO consumer_inbox(consumer,event_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, consumerName, envelope.ID)
	if err != nil {
		return fmt.Errorf("insert notification inbox: %w", err)
	}
	if command.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO notification_deliveries(id,event_id,payment_id,user_id,payload,status)
		VALUES($1,$2,$3,$4,$5,'pending')`,
		delivery.DeliveryID, envelope.ID, payment.PaymentID, payment.UserID, string(payload),
	)
	if err != nil {
		return fmt.Errorf("insert notification delivery: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Service) RunPublisher(ctx context.Context) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := s.publishBatch(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("notification publish batch failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) publishBatch(ctx context.Context) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delivery batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT id,payload FROM notification_deliveries
		WHERE status='pending' AND attempts < 10
		ORDER BY created_at LIMIT 20 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return fmt.Errorf("select notification deliveries: %w", err)
	}
	type delivery struct {
		id      uuid.UUID
		payload []byte
	}
	items := make([]delivery, 0, 20)
	for rows.Next() {
		var item delivery
		if err := rows.Scan(&item.id, &item.payload); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		publishCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, publishErr := s.sns.Publish(publishCtx, &sns.PublishInput{
			TopicArn: aws.String(s.topicARN), Message: aws.String(string(item.payload)),
		})
		cancel()
		if publishErr != nil {
			_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET attempts=attempts+1,last_error=$2 WHERE id=$1`, item.id, publishErr.Error())
			if err != nil {
				return err
			}
			continue
		}
		_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='published',published_at=now(),attempts=attempts+1,last_error='' WHERE id=$1`, item.id)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) RunSQSConsumer(ctx context.Context) error {
	for {
		output, err := s.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(s.queueURL), MaxNumberOfMessages: 10,
			WaitTimeSeconds: 10, VisibilityTimeout: 30,
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive notifications: %w", err)
		}
		for _, message := range output.Messages {
			if err := s.consumeSQSMessage(ctx, aws.ToString(message.Body)); err != nil {
				s.logger.Error("notification SQS message failed", "message_id", aws.ToString(message.MessageId), "error", err)
				continue
			}
			if _, err := s.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(s.queueURL), ReceiptHandle: message.ReceiptHandle}); err != nil {
				return fmt.Errorf("delete notification message: %w", err)
			}
		}
	}
}

func (s *Service) consumeSQSMessage(ctx context.Context, body string) error {
	var payload deliveryPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		// SNS can wrap messages unless RawMessageDelivery is enabled.
		var wrapper struct {
			Message string `json:"Message"`
		}
		if wrapperErr := json.Unmarshal([]byte(body), &wrapper); wrapperErr != nil || wrapper.Message == "" {
			return fmt.Errorf("decode SQS notification: %w", err)
		}
		if err := json.Unmarshal([]byte(wrapper.Message), &payload); err != nil {
			return fmt.Errorf("decode wrapped SNS notification: %w", err)
		}
	}
	if payload.DeliveryID == uuid.Nil || payload.PaymentID == uuid.Nil {
		return fmt.Errorf("notification payload has empty identifiers")
	}
	command, err := s.pool.Exec(ctx, `INSERT INTO notification_sends(delivery_id,payment_id,user_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, payload.DeliveryID, payload.PaymentID, payload.UserID)
	if err != nil {
		return fmt.Errorf("record notification send: %w", err)
	}
	if command.RowsAffected() == 1 {
		logging.WithContext(ctx, s.logger).Info("notification sent", "payment_id", payload.PaymentID, "user_id", payload.UserID, "delivery_id", payload.DeliveryID, "kind", payload.Kind)
	}
	return nil
}
