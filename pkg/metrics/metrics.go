package metrics

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Registry            *prometheus.Registry
	HTTPRequests        *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec
	PaymentsCreated     prometheus.Counter
	PaymentsCompleted   prometheus.Counter
	PaymentsFailed      prometheus.Counter
	KafkaProcessed      *prometheus.CounterVec
	KafkaErrors         *prometheus.CounterVec
	ProviderDuration    prometheus.Histogram
	OutboxPending       prometheus.Gauge
}

func New() *Metrics {
	registry := prometheus.NewRegistry()
	metrics := &Metrics{
		Registry: registry,
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "HTTP requests by method, route, and status.",
		}, []string{"method", "route", "status"}),
		HTTPRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency.", Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		PaymentsCreated:   prometheus.NewCounter(prometheus.CounterOpts{Name: "payments_created_total", Help: "Created payments."}),
		PaymentsCompleted: prometheus.NewCounter(prometheus.CounterOpts{Name: "payments_completed_total", Help: "Completed payments."}),
		PaymentsFailed:    prometheus.NewCounter(prometheus.CounterOpts{Name: "payments_failed_total", Help: "Failed payments."}),
		KafkaProcessed:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kafka_messages_processed_total", Help: "Processed Kafka records."}, []string{"topic", "consumer"}),
		KafkaErrors:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kafka_processing_errors_total", Help: "Kafka processing failures."}, []string{"topic", "consumer"}),
		ProviderDuration:  prometheus.NewHistogram(prometheus.HistogramOpts{Name: "provider_request_duration_seconds", Help: "Fake provider request latency."}),
		OutboxPending:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "outbox_pending_events", Help: "Unpublished outbox rows."}),
	}
	registry.MustRegister(
		metrics.HTTPRequests, metrics.HTTPRequestDuration, metrics.PaymentsCreated,
		metrics.PaymentsCompleted, metrics.PaymentsFailed, metrics.KafkaProcessed,
		metrics.KafkaErrors, metrics.ProviderDuration, metrics.OutboxPending,
	)
	return metrics
}
