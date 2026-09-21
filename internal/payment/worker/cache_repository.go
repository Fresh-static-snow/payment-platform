package worker

import (
	"context"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/payment/domain"
	"github.com/redis/go-redis/v9"
)

type CacheInvalidatingRepository struct {
	base  PaymentRepository
	cache *redis.Client
}

func NewCacheInvalidatingRepository(base PaymentRepository, cache *redis.Client) *CacheInvalidatingRepository {
	return &CacheInvalidatingRepository{base: base, cache: cache}
}

func (r *CacheInvalidatingRepository) Get(ctx context.Context, id uuid.UUID) (domain.Payment, error) {
	return r.base.Get(ctx, id)
}

func (r *CacheInvalidatingRepository) Transition(
	ctx context.Context,
	id uuid.UUID,
	expectedVersion int64,
	to domain.Status,
	providerReference string,
	failureReason string,
	requestID string,
	causationID string,
) (domain.Payment, error) {
	payment, err := r.base.Transition(ctx, id, expectedVersion, to, providerReference, failureReason, requestID, causationID)
	if err != nil {
		return domain.Payment{}, err
	}
	if r.cache != nil {
		// PostgreSQL remains authoritative; TTL bounds staleness if Redis is
		// temporarily unavailable while this best-effort invalidation runs.
		_ = r.cache.Del(ctx, "payment:"+id.String()).Err()
	}
	return payment, nil
}
