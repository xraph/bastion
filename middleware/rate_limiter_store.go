package middleware

import (
	"context"
	"time"
)

// RateLimitStore is the interface for distributed rate limiting backends.
// Implementations must be safe for concurrent use.
type RateLimitStore interface {
	// Allow checks if a request identified by key is allowed under the given
	// rate limit. Returns true if allowed, false if rate-limited.
	// The implementation should atomically increment the counter.
	Allow(ctx context.Context, key string, limit float64, burst int, window time.Duration) (bool, error)
}

// DistributedRateLimiter extends the basic RateLimiter with an external store
// for multi-instance rate limiting.
type DistributedRateLimiter struct {
	*RateLimiter
	store  RateLimitStore
	window time.Duration
}

// NewDistributedRateLimiter creates a rate limiter backed by an external store.
// Falls back to local token bucket if the store is unavailable.
func NewDistributedRateLimiter(config RateLimitConfig, store RateLimitStore) *DistributedRateLimiter {
	window := time.Second
	if config.RequestsPerSec < 1 {
		window = time.Duration(float64(time.Second) / config.RequestsPerSec)
	}

	return &DistributedRateLimiter{
		RateLimiter: NewRateLimiter(config),
		store:       store,
		window:      window,
	}
}

// AllowDistributed checks the rate limit using the distributed store.
// Falls back to local limiter on store error.
func (drl *DistributedRateLimiter) AllowDistributed(ctx context.Context, key string) bool {
	if drl.store == nil {
		return drl.AllowForKey(key)
	}

	allowed, err := drl.store.Allow(ctx, key, drl.config.RequestsPerSec, drl.config.Burst, drl.window)
	if err != nil {
		// Fall back to local limiter on store error
		return drl.AllowForKey(key)
	}

	return allowed
}

// InMemoryRateLimitStore is a local implementation of RateLimitStore
// using sliding window counters. Useful for testing or single-instance deployments.
type InMemoryRateLimitStore struct {
	limiter *RateLimiter
}

// NewInMemoryRateLimitStore creates a new in-memory rate limit store.
func NewInMemoryRateLimitStore() *InMemoryRateLimitStore {
	return &InMemoryRateLimitStore{
		limiter: NewRateLimiter(RateLimitConfig{
			Enabled:        true,
			RequestsPerSec: 1000,
			Burst:          100,
			PerClient:      true,
		}),
	}
}

// Allow implements RateLimitStore using local token buckets.
func (s *InMemoryRateLimitStore) Allow(_ context.Context, key string, limit float64, burst int, _ time.Duration) (bool, error) {
	_ = limit
	_ = burst
	return s.limiter.AllowForKey(key), nil
}
