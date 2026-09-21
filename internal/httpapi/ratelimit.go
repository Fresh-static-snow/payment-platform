package httpapi

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

var slidingWindow = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local member = ARGV[4]
redis.call('ZREMRANGEBYSCORE', key, 0, now-window)
local count = redis.call('ZCARD', key)
if count >= limit then return 0 end
redis.call('ZADD', key, now, member)
redis.call('PEXPIRE', key, window)
return 1
`)

type RateLimiter struct {
	client *redis.Client
	limit  int
	window time.Duration
}

func NewRateLimiter(client *redis.Client, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{client: client, limit: limit, window: window}
}

// Allow fails open when Redis is unavailable. PostgreSQL-backed idempotency and
// payment correctness remain intact; only the protective optimization degrades.
func (r *RateLimiter) Allow(ctx context.Context, subject, requestID string) bool {
	if r == nil || r.client == nil {
		return true
	}
	now := time.Now().UnixMilli()
	result, err := slidingWindow.Run(ctx, r.client, []string{"rate:" + subject}, now, r.window.Milliseconds(), r.limit, requestID).Int()
	return err != nil || result == 1
}
