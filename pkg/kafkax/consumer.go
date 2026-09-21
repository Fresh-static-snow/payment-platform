package kafkax

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/praedyth/payment-platform/internal/requestctx"
	platformmetrics "github.com/praedyth/payment-platform/pkg/metrics"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
)

type Handler func(context.Context, kafka.Message) error

type ConsumerConfig struct {
	Brokers     []string
	Topic       string
	GroupID     string
	Workers     int
	MaxAttempts int
	Logger      *slog.Logger
	Handler     Handler
	Metrics     *platformmetrics.Metrics
	Dialer      *kafka.Dialer
}

// RunConsumerGroup starts a bounded set of independent group members. Each
// reader handles one record at a time, which preserves partition ordering and
// makes a slow handler apply backpressure instead of spawning unbounded work.
func RunConsumerGroup(ctx context.Context, cfg ConsumerConfig) error {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 4
	}
	if cfg.Dialer == nil {
		cfg.Dialer = NewDialer(false)
	}
	dlqWriter := NewWriter(cfg.Brokers, cfg.Dialer)
	defer func() { _ = dlqWriter.Close() }()
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errorsCh := make(chan error, cfg.Workers)
	for worker := 0; worker < cfg.Workers; worker++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			reader := kafka.NewReader(kafka.ReaderConfig{
				Brokers: cfg.Brokers, Topic: cfg.Topic, GroupID: cfg.GroupID,
				MinBytes: 1, MaxBytes: 10e6, MaxWait: time.Second,
				CommitInterval: 0, Dialer: cfg.Dialer,
			})
			defer func() { _ = reader.Close() }()
			if err := consume(workerCtx, reader, dlqWriter, cfg, workerID); err != nil && !errors.Is(err, context.Canceled) {
				errorsCh <- err
			}
		}(worker)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return nil
	case err := <-errorsCh:
		cancel()
		<-done
		return err
	case <-done:
		return nil
	}
}

func consume(ctx context.Context, reader *kafka.Reader, dlq *kafka.Writer, cfg ConsumerConfig, workerID int) error {
	for {
		message, err := reader.FetchMessage(ctx)
		if err != nil {
			return err
		}
		messageCtx := otel.GetTextMapPropagator().Extract(ctx, HeaderCarrier(message.Headers))
		messageCtx = requestctx.WithRequestID(messageCtx, header(message.Headers, "request_id"))
		var handlerErr error
		for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
			handlerErr = cfg.Handler(messageCtx, message)
			if handlerErr == nil {
				break
			}
			if cfg.Metrics != nil {
				cfg.Metrics.KafkaErrors.WithLabelValues(cfg.Topic, cfg.GroupID).Inc()
			}
			cfg.Logger.Warn("kafka handler attempt failed", "topic", cfg.Topic, "partition", message.Partition, "offset", message.Offset, "worker", workerID, "attempt", attempt, "error", handlerErr)
			if attempt < cfg.MaxAttempts {
				delay := time.Duration(1<<(attempt-1)) * 100 * time.Millisecond
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		if handlerErr != nil {
			dlqMessage := kafka.Message{
				Topic: cfg.Topic + ".dlq", Key: message.Key, Value: message.Value,
				Headers: append(message.Headers,
					kafka.Header{Key: "original_topic", Value: []byte(cfg.Topic)},
					kafka.Header{Key: "attempts", Value: []byte(strconv.Itoa(cfg.MaxAttempts))},
					kafka.Header{Key: "error", Value: []byte(handlerErr.Error())},
				),
			}
			if err := dlq.WriteMessages(ctx, dlqMessage); err != nil {
				return err
			}
			cfg.Logger.Error("kafka message moved to DLQ", "topic", cfg.Topic, "partition", message.Partition, "offset", message.Offset, "error", handlerErr)
		}
		if handlerErr == nil && cfg.Metrics != nil {
			cfg.Metrics.KafkaProcessed.WithLabelValues(cfg.Topic, cfg.GroupID).Inc()
		}
		if err := reader.CommitMessages(ctx, message); err != nil {
			return err
		}
	}
}

func header(headers []kafka.Header, name string) string {
	for _, item := range headers {
		if item.Key == name {
			return string(item.Value)
		}
	}
	return ""
}
