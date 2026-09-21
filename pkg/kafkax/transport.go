package kafkax

import (
	"crypto/tls"
	"time"

	"github.com/segmentio/kafka-go"
)

// NewDialer centralizes Kafka transport settings so local plaintext Kafka and
// encrypted MSK TLS endpoints use identical consumer semantics.
func NewDialer(tlsEnabled bool) *kafka.Dialer {
	dialer := &kafka.Dialer{Timeout: 10 * time.Second, DualStack: true}
	if tlsEnabled {
		dialer.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return dialer
}

func NewWriter(brokers []string, dialer *kafka.Dialer) *kafka.Writer {
	if dialer == nil {
		dialer = NewDialer(false)
	}
	return kafka.NewWriter(kafka.WriterConfig{
		Brokers: brokers, Balancer: &kafka.Hash{}, RequiredAcks: -1,
		Async: false, Dialer: dialer,
	})
}
