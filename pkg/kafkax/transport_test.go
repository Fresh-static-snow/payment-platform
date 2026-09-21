package kafkax

import (
	"crypto/tls"
	"testing"
)

func TestNewDialerEnablesModernTLSOnlyWhenRequested(t *testing.T) {
	if dialer := NewDialer(false); dialer.TLS != nil {
		t.Fatal("plaintext dialer unexpectedly has TLS configuration")
	}
	dialer := NewDialer(true)
	if dialer.TLS == nil || dialer.TLS.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS dialer configuration = %#v", dialer.TLS)
	}
}
