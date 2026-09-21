package riskgrpc

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/requestctx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const RequestIDMetadataKey = "x-request-id"

// RequestIDUnaryServerInterceptor transfers the correlation ID from gRPC
// metadata into context.Context. Missing IDs are generated at the boundary and
// returned as response metadata, so downstream logs still remain traceable.
func RequestIDUnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		requestID := incomingRequestID(ctx)
		if requestID == "" {
			requestID = uuid.NewString()
		}
		ctx = requestctx.WithRequestID(ctx, requestID)
		_ = grpc.SetHeader(ctx, metadata.Pairs(RequestIDMetadataKey, requestID))
		return handler(ctx, req)
	}
}

func withOutgoingRequestID(ctx context.Context) context.Context {
	requestID := strings.TrimSpace(requestctx.RequestID(ctx))
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	if requestID == "" {
		for _, value := range md.Get(RequestIDMetadataKey) {
			if value = strings.TrimSpace(value); value != "" {
				requestID = value
				break
			}
		}
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}
	md.Set(RequestIDMetadataKey, requestID)
	return metadata.NewOutgoingContext(ctx, md)
}

func incomingRequestID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, value := range md.Get(RequestIDMetadataKey) {
		if value = strings.TrimSpace(value); value != "" {
			if len(value) > 128 {
				return value[:128]
			}
			return value
		}
	}
	return ""
}
