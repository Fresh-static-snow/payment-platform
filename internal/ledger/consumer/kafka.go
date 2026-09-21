package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/segmentio/kafka-go"

	"github.com/praedyth/payment-platform/internal/events"
	"github.com/praedyth/payment-platform/internal/requestctx"
	"github.com/praedyth/payment-platform/pkg/kafkax"
	"go.opentelemetry.io/otel"
)

type Reader interface {
	FetchMessage(context.Context) (kafka.Message, error)
	CommitMessages(context.Context, ...kafka.Message) error
	Close() error
}

type Handler interface {
	Process(context.Context, events.Envelope) error
}

type Consumer struct {
	reader     Reader
	handler    Handler
	logger     *slog.Logger
	workers    int
	queueDepth int
}

func New(reader Reader, handler Handler, logger *slog.Logger, workers, queueDepth int) *Consumer {
	return &Consumer{
		reader:     reader,
		handler:    handler,
		logger:     logger,
		workers:    workers,
		queueDepth: queueDepth,
	}
}

// Run uses a bounded set of partition-affine queues. Messages from the same
// Kafka partition always reach the same worker in fetch order; this prevents a
// later offset from being committed while an earlier offset is still failing.
func (c *Consumer) Run(ctx context.Context) error {
	if c.reader == nil {
		return fmt.Errorf("ledger Kafka reader is required")
	}
	if c.handler == nil {
		return fmt.Errorf("ledger event handler is required")
	}
	if c.workers < 1 {
		return fmt.Errorf("ledger worker count must be positive")
	}
	if c.queueDepth < 0 {
		return fmt.Errorf("ledger worker queue depth cannot be negative")
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make([]chan kafka.Message, c.workers)
	failures := make(chan error, 1)
	var workers sync.WaitGroup

	reportFailure := func(err error) {
		select {
		case failures <- err:
		default:
		}
		cancel()
	}

	for index := range jobs {
		jobs[index] = make(chan kafka.Message, c.queueDepth)
		workers.Add(1)
		go func(workerID int, queue <-chan kafka.Message) {
			defer workers.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case message := <-queue:
					if workerCtx.Err() != nil {
						return
					}
					if err := c.processAndCommit(workerCtx, message); err != nil {
						reportFailure(fmt.Errorf("ledger worker %d: %w", workerID, err))
						return
					}
				}
			}
		}(index, jobs[index])
	}

fetchLoop:
	for {
		message, err := c.reader.FetchMessage(workerCtx)
		if err != nil {
			if workerCtx.Err() == nil {
				reportFailure(fmt.Errorf("fetch ledger Kafka message: %w", err))
			}
			break
		}

		workerID := message.Partition % c.workers
		if workerID < 0 {
			workerID = -workerID
		}
		select {
		case jobs[workerID] <- message:
		case <-workerCtx.Done():
			break fetchLoop
		}
	}

	cancel()
	workers.Wait()
	select {
	case err := <-failures:
		return err
	default:
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return nil
}

func (c *Consumer) processAndCommit(ctx context.Context, message kafka.Message) error {
	ctx = otel.GetTextMapPropagator().Extract(ctx, kafkax.HeaderCarrier(message.Headers))
	var event events.Envelope
	if err := json.Unmarshal(message.Value, &event); err != nil {
		return fmt.Errorf("decode Kafka event at partition=%d offset=%d: %w", message.Partition, message.Offset, err)
	}
	if event.CorrelationID != "" {
		ctx = requestctx.WithRequestID(ctx, event.CorrelationID)
	}
	if err := c.handler.Process(ctx, event); err != nil {
		return fmt.Errorf("process event %s at partition=%d offset=%d: %w", event.ID, message.Partition, message.Offset, err)
	}
	if err := c.reader.CommitMessages(ctx, message); err != nil {
		return fmt.Errorf("commit event %s at partition=%d offset=%d: %w", event.ID, message.Partition, message.Offset, err)
	}
	if c.logger != nil {
		c.logger.DebugContext(
			ctx,
			"ledger Kafka message committed",
			"event_id", event.ID,
			"partition", message.Partition,
			"offset", message.Offset,
		)
	}
	return nil
}

func (c *Consumer) Close() error {
	if c.reader == nil {
		return nil
	}
	return c.reader.Close()
}
