package riskgrpc

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/requestctx"
	"google.golang.org/grpc/metadata"
)

func TestRequestIDInterceptorPropagatesMetadata(t *testing.T) {
	t.Parallel()
	const want = "request-123"
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(RequestIDMetadataKey, want))
	var got string

	_, err := RequestIDUnaryServerInterceptor()(ctx, nil, nil, func(handlerCtx context.Context, _ any) (any, error) {
		got = requestctx.RequestID(handlerCtx)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("interceptor error = %v", err)
	}
	if got != want {
		t.Fatalf("request ID = %q, want %q", got, want)
	}
}

func TestRequestIDInterceptorGeneratesMissingID(t *testing.T) {
	t.Parallel()
	var got string
	_, err := RequestIDUnaryServerInterceptor()(context.Background(), nil, nil, func(handlerCtx context.Context, _ any) (any, error) {
		got = requestctx.RequestID(handlerCtx)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("interceptor error = %v", err)
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("generated request ID %q is not a UUID: %v", got, err)
	}
}

func TestOutgoingRequestIDPreservesExistingMetadata(t *testing.T) {
	t.Parallel()
	const want = "request-from-metadata"
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(RequestIDMetadataKey, want))
	ctx = withOutgoingRequestID(ctx)
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("outgoing metadata is missing")
	}
	values := md.Get(RequestIDMetadataKey)
	if len(values) != 1 || values[0] != want {
		t.Fatalf("request ID metadata = %v, want preserved value", values)
	}
}
