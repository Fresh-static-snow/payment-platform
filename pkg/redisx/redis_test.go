package redisx

import (
	"context"
	"testing"
)

func TestOpenWithConfigRejectsTLSAddressWithoutPort(t *testing.T) {
	_, err := OpenWithConfig(context.Background(), Config{Address: "redis.internal", TLSEnabled: true})
	if err == nil {
		t.Fatal("expected invalid TLS Redis address to fail before dialing")
	}
}
