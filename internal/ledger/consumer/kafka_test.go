package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"

	"github.com/praedyth/payment-platform/internal/events"
)

type fakeReader struct {
	messages chan kafka.Message
	commits  chan kafka.Message
	closed   atomic.Bool
}

func (reader *fakeReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	select {
	case message := <-reader.messages:
		return message, nil
	case <-ctx.Done():
		return kafka.Message{}, ctx.Err()
	}
}

func (reader *fakeReader) CommitMessages(ctx context.Context, messages ...kafka.Message) error {
	for _, message := range messages {
		select {
		case reader.commits <- message:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (reader *fakeReader) Close() error {
	reader.closed.Store(true)
	return nil
}

type handlerFunc func(context.Context, events.Envelope) error

func (function handlerFunc) Process(ctx context.Context, event events.Envelope) error {
	return function(ctx, event)
}

func TestConsumerCommitsOnlyAfterSuccessfulProcessing(t *testing.T) {
	message := kafkaMessage(t, 2, 41)
	reader := &fakeReader{
		messages: make(chan kafka.Message, 1),
		commits:  make(chan kafka.Message, 1),
	}
	reader.messages <- message
	var processed atomic.Bool
	handler := handlerFunc(func(_ context.Context, event events.Envelope) error {
		if event.CorrelationID != "request-123" {
			t.Fatalf("correlation id = %q, want request-123", event.CorrelationID)
		}
		processed.Store(true)
		return nil
	})
	consumer := New(reader, handler, nil, 3, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()

	select {
	case committed := <-reader.commits:
		if !processed.Load() {
			t.Fatal("message committed before handler completed")
		}
		if committed.Partition != message.Partition || committed.Offset != message.Offset {
			t.Fatalf("committed message = partition %d offset %d, want partition %d offset %d", committed.Partition, committed.Offset, message.Partition, message.Offset)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Kafka commit")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error after cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("consumer did not stop after cancellation")
	}
}

func TestConsumerDoesNotCommitFailedMessage(t *testing.T) {
	reader := &fakeReader{
		messages: make(chan kafka.Message, 1),
		commits:  make(chan kafka.Message, 1),
	}
	reader.messages <- kafkaMessage(t, 0, 7)
	wantErr := errors.New("posting failed")
	consumer := New(reader, handlerFunc(func(context.Context, events.Envelope) error {
		return wantErr
	}), nil, 2, 1)

	err := consumer.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want wrapped handler error", err)
	}
	select {
	case committed := <-reader.commits:
		t.Fatalf("failed message was committed: partition %d offset %d", committed.Partition, committed.Offset)
	default:
	}
}

func TestConsumerRejectsInvalidConfiguration(t *testing.T) {
	reader := &fakeReader{messages: make(chan kafka.Message), commits: make(chan kafka.Message)}
	handler := handlerFunc(func(context.Context, events.Envelope) error { return nil })
	tests := []struct {
		name     string
		consumer *Consumer
	}{
		{name: "missing reader", consumer: New(nil, handler, nil, 1, 0)},
		{name: "missing handler", consumer: New(reader, nil, nil, 1, 0)},
		{name: "zero workers", consumer: New(reader, handler, nil, 0, 0)},
		{name: "negative queue", consumer: New(reader, handler, nil, 1, -1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.consumer.Run(context.Background()); err == nil {
				t.Fatal("Run() error = nil, want configuration error")
			}
		})
	}
}

func kafkaMessage(t *testing.T, partition int, offset int64) kafka.Message {
	t.Helper()
	payload, err := json.Marshal(events.PaymentPayload{
		PaymentID: uuid.New(),
		UserID:    uuid.New(),
		Amount:    1000,
		Currency:  "USD",
		Status:    "completed",
	})
	if err != nil {
		t.Fatalf("marshal payment payload: %v", err)
	}
	event := events.Envelope{
		ID:            uuid.New(),
		Type:          events.PaymentCompleted,
		Version:       1,
		OccurredAt:    time.Now().UTC(),
		CorrelationID: "request-123",
		Payload:       payload,
	}
	value, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return kafka.Message{Topic: events.PaymentCompleted, Partition: partition, Offset: offset, Value: value}
}
