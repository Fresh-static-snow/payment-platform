package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/praedyth/payment-platform/internal/events"
	"github.com/segmentio/kafka-go"
)

type EventWriter interface {
	Insert(context.Context, PaymentEvent) error
}

type Ingestor struct {
	writer EventWriter
	now    func() time.Time
}

func NewIngestor(writer EventWriter) *Ingestor {
	return &Ingestor{writer: writer, now: time.Now}
}

func (i *Ingestor) Handle(ctx context.Context, message kafka.Message) error {
	var envelope events.Envelope
	if err := json.Unmarshal(message.Value, &envelope); err != nil {
		return fmt.Errorf("decode analytics envelope: %w", err)
	}
	event, err := PaymentEventFromEnvelope(envelope, message.Topic, message.Partition, message.Offset, i.now())
	if err != nil {
		return err
	}
	if err := i.writer.Insert(ctx, event); err != nil {
		return fmt.Errorf("insert analytics event %s: %w", event.EventID, err)
	}
	return nil
}
