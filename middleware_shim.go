package bastion

// This file provides backward-compatible type aliases and thin wrappers
// for types that have been migrated to the middleware subpackage.
// The canonical implementations now live in github.com/xraph/bastion/middleware.

import (
	"context"
	"net/http"

	"github.com/xraph/bastion/middleware"
	"github.com/xraph/forge"
)

// --- Compression ---

// CompressionConfig configures response compression.
//
// Canonical definition: middleware.CompressionConfig
type CompressionConfig = middleware.CompressionConfig

// CompressionMiddleware wraps an http.Handler with gzip compression.
//
// Canonical definition: middleware.CompressionMiddleware
type CompressionMiddleware = middleware.CompressionMiddleware

// NewCompressionMiddleware creates a new compression middleware.
func NewCompressionMiddleware(config CompressionConfig) *CompressionMiddleware {
	return middleware.NewCompressionMiddleware(config)
}

// --- Rate Limiting ---

// RateLimiter implements token-bucket rate limiting.
//
// Canonical definition: middleware.RateLimiter
type RateLimiter = middleware.RateLimiter

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(config RateLimitConfig) *RateLimiter {
	return middleware.NewRateLimiter(config)
}

// DistributedRateLimiter extends the basic RateLimiter with an external store
// for multi-instance rate limiting.
//
// Canonical definition: middleware.DistributedRateLimiter
type DistributedRateLimiter = middleware.DistributedRateLimiter

// NewDistributedRateLimiter creates a rate limiter backed by an external store.
func NewDistributedRateLimiter(config RateLimitConfig, store RateLimitStore) *DistributedRateLimiter {
	return middleware.NewDistributedRateLimiter(config, store)
}

// InMemoryRateLimitStore is a local implementation of RateLimitStore.
//
// Canonical definition: middleware.InMemoryRateLimitStore
type InMemoryRateLimitStore = middleware.InMemoryRateLimitStore

// NewInMemoryRateLimitStore creates a new in-memory rate limit store.
func NewInMemoryRateLimitStore() *InMemoryRateLimitStore {
	return middleware.NewInMemoryRateLimitStore()
}

// --- Caching ---

// CacheStats tracks cache hit/miss metrics.
//
// Canonical definition: middleware.CacheStats
type CacheStats = middleware.CacheStats

// InMemoryCacheStore provides a simple in-memory LRU cache for the gateway.
//
// Canonical definition: middleware.InMemoryCacheStore
type InMemoryCacheStore = middleware.InMemoryCacheStore

// NewInMemoryCacheStore creates a new in-memory cache store.
func NewInMemoryCacheStore(maxSize int) *InMemoryCacheStore {
	return middleware.NewInMemoryCacheStore(maxSize)
}

// ResponseCache provides HTTP response caching for the gateway.
// This is a thin wrapper around middleware.ResponseCache that adapts
// the root-level Route type to the middleware-local Route type.
type ResponseCache struct {
	inner *middleware.ResponseCache
}

// NewResponseCache creates a new response cache.
func NewResponseCache(config CachingConfig, logger forge.Logger, store CacheStore) *ResponseCache {
	return &ResponseCache{
		inner: middleware.NewResponseCache(config, logger, store),
	}
}

// Get attempts to retrieve a cached response for the given request.
func (rc *ResponseCache) Get(r *http.Request, route *Route) *CachedResponse {
	return rc.inner.Get(r, toMiddlewareRoute(route))
}

// Set stores a response in the cache.
func (rc *ResponseCache) Set(r *http.Request, route *Route, statusCode int, headers http.Header, body []byte) {
	rc.inner.Set(r, toMiddlewareRoute(route), statusCode, headers, body)
}

// WriteCachedResponse writes a cached response to the client.
func (rc *ResponseCache) WriteCachedResponse(w http.ResponseWriter, cached *CachedResponse) {
	rc.inner.WriteCachedResponse(w, cached)
}

// Invalidate removes a cached entry for the given request.
func (rc *ResponseCache) Invalidate(ctx context.Context, method, path string) error {
	return rc.inner.Invalidate(ctx, method, path)
}

// Stats returns the cache statistics.
func (rc *ResponseCache) Stats() *CacheStats {
	return rc.inner.Stats()
}

// toMiddlewareRoute converts a root-level Route to the middleware-local Route type.
func toMiddlewareRoute(route *Route) *middleware.Route {
	if route == nil {
		return nil
	}

	return &middleware.Route{
		Path:    route.Path,
		Cache:   route.Cache,
		Methods: route.Methods,
	}
}
