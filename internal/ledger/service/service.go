package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/internal/ledger/domain"
)

const ConsumerName = "ledger-service"

type JournalStore interface {
	Store(ctx context.Context, consumer string, journal domain.Journal) (created bool, err error)
}

type Processor struct {
	store  JournalStore
	logger *slog.Logger
}

func NewProcessor(store JournalStore, logger *slog.Logger) *Processor {
	return &Processor{store: store, logger: logger}
}

func (p *Processor) Process(ctx context.Context, event events.Envelope) error {
	if event.ID == uuid.Nil {
		return fmt.Errorf("ledger event id is required")
	}
	if event.Version != 1 {
		return fmt.Errorf("unsupported %s event version %d", event.Type, event.Version)
	}
	if event.OccurredAt.IsZero() {
		return fmt.Errorf("ledger event occurred_at is required")
	}
	switch event.Type {
	case events.PaymentCompleted:
		return p.processPayment(ctx, event)
	case events.RefundCompleted:
		return p.processRefund(ctx, event)
	default:
		return fmt.Errorf("unsupported ledger event type %q", event.Type)
	}
}

func (p *Processor) processPayment(ctx context.Context, event events.Envelope) error {
	var payload events.PaymentPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode payment completion payload: %w", err)
	}
	if payload.Status != "completed" {
		return fmt.Errorf("payment completion status must be completed, got %q", payload.Status)
	}

	journal, err := domain.NewPaymentJournal(
		event.ID,
		payload.PaymentID,
		payload.Currency,
		payload.Amount,
		event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("build payment ledger journal: %w", err)
	}

	created, err := p.store.Store(ctx, ConsumerName, journal)
	if err != nil {
		return fmt.Errorf("store payment ledger journal: %w", err)
	}
	if p.logger != nil {
		p.logger.InfoContext(
			ctx,
			"payment ledger event processed",
			"event_id", event.ID,
			"payment_id", payload.PaymentID,
			"created", created,
		)
	}
	return nil
}

func (p *Processor) processRefund(ctx context.Context, event events.Envelope) error {
	var payload events.RefundPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode refund completion payload: %w", err)
	}
	if payload.Status != "completed" {
		return fmt.Errorf("refund completion status must be completed, got %q", payload.Status)
	}
	journal, err := domain.NewRefundJournal(
		event.ID, payload.RefundID, payload.PaymentID, payload.Currency, payload.Amount, event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("build refund ledger journal: %w", err)
	}
	created, err := p.store.Store(ctx, ConsumerName, journal)
	if err != nil {
		return fmt.Errorf("store refund ledger journal: %w", err)
	}
	if p.logger != nil {
		p.logger.InfoContext(ctx, "refund ledger event processed",
			"event_id", event.ID, "refund_id", payload.RefundID,
			"payment_id", payload.PaymentID, "created", created,
		)
	}
	return nil
}
