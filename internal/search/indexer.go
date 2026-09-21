package search

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/praedyth/payment-platform/internal/events"
	"github.com/segmentio/kafka-go"
)

type DocumentIndexer interface {
	Index(context.Context, PaymentDocument) error
}

type Indexer struct {
	documents DocumentIndexer
}

func NewIndexer(documents DocumentIndexer) *Indexer {
	return &Indexer{documents: documents}
}

func (i *Indexer) Handle(ctx context.Context, message kafka.Message) error {
	var envelope events.Envelope
	if err := json.Unmarshal(message.Value, &envelope); err != nil {
		return fmt.Errorf("decode payment search envelope: %w", err)
	}
	document, err := DocumentFromEnvelope(envelope)
	if err != nil {
		return err
	}
	if err := i.documents.Index(ctx, document); err != nil {
		return fmt.Errorf("index payment %s at version %d: %w", document.PaymentID, document.AggregateVersion, err)
	}
	return nil
}
