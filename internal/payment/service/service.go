package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/praedyth/payment-platform/internal/payment/domain"
	"github.com/praedyth/payment-platform/internal/payment/repository"
	"github.com/redis/go-redis/v9"
)

const paymentCacheTTL = 30 * time.Second

type Service struct {
	store *repository.Store
	cache *redis.Client
}

func New(store *repository.Store, cache *redis.Client) *Service {
	return &Service{store: store, cache: cache}
}

type CreateInput struct {
	IdempotencyKey string    `json:"-"`
	UserID         uuid.UUID `json:"user_id"`
	Amount         int64     `json:"amount"`
	Currency       string    `json:"currency"`
	Description    string    `json:"description"`
	RequestID      string    `json:"-"`
}

func (s *Service) Create(ctx context.Context, input CreateInput) (domain.Payment, bool, error) {
	canonical := struct {
		UserID      uuid.UUID `json:"user_id"`
		Amount      int64     `json:"amount"`
		Currency    string    `json:"currency"`
		Description string    `json:"description"`
	}{input.UserID, input.Amount, strings.ToUpper(input.Currency), input.Description}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("marshal idempotency request: %w", err)
	}
	hash := sha256.Sum256(raw)
	payment, created, err := s.store.Create(ctx, domain.CreateParams{
		IdempotencyKey: input.IdempotencyKey,
		RequestHash:    hash[:],
		UserID:         input.UserID,
		Amount:         input.Amount,
		Currency:       input.Currency,
		Description:    input.Description,
		RequestID:      input.RequestID,
	})
	if err != nil {
		return domain.Payment{}, false, err
	}
	s.putCache(ctx, payment)
	return payment, created, nil
}

func (s *Service) Get(ctx context.Context, id, userID uuid.UUID) (domain.Payment, error) {
	if s.cache != nil {
		var cached domain.Payment
		raw, err := s.cache.Get(ctx, cacheKey(id)).Bytes()
		if err == nil && json.Unmarshal(raw, &cached) == nil {
			if cached.UserID != userID {
				return domain.Payment{}, domain.ErrPaymentNotFound
			}
			return cached, nil
		}
	}
	payment, err := s.store.Get(ctx, id)
	if err != nil {
		return domain.Payment{}, err
	}
	if payment.UserID != userID {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	s.putCache(ctx, payment)
	return payment, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, limit int, cursor string) ([]domain.Payment, string, error) {
	var decoded *repository.Cursor
	if cursor != "" {
		value, err := repository.DecodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		decoded = &value
	}
	items, err := s.store.List(ctx, userID, limit+1, decoded)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		last := items[limit-1]
		next = repository.EncodeCursor(repository.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		items = items[:limit]
	}
	return items, next, nil
}

func (s *Service) Cancel(ctx context.Context, id, userID uuid.UUID, requestID string) (domain.Payment, error) {
	payment, err := s.store.Cancel(ctx, id, userID, requestID)
	if err != nil {
		return domain.Payment{}, err
	}
	s.deleteCache(ctx, id)
	return payment, nil
}

func (s *Service) RequestReceipt(ctx context.Context, id, userID uuid.UUID, requestID string) (bool, error) {
	return s.store.RequestReceipt(ctx, id, userID, requestID)
}

func (s *Service) putCache(ctx context.Context, payment domain.Payment) {
	if s.cache == nil {
		return
	}
	raw, err := json.Marshal(payment)
	if err == nil {
		_ = s.cache.Set(ctx, cacheKey(payment.ID), raw, paymentCacheTTL).Err()
	}
}

func (s *Service) deleteCache(ctx context.Context, id uuid.UUID) {
	if s.cache != nil {
		_ = s.cache.Del(ctx, cacheKey(id)).Err()
	}
}

func cacheKey(id uuid.UUID) string { return "payment:" + id.String() }
