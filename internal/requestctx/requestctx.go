package requestctx

import "context"

type key struct{}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, key{}, requestID)
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(key{}).(string)
	return value
}
