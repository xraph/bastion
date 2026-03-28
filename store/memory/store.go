// Package memory provides an in-memory implementation of the Bastion
// composite store. This is the default fallback when no persistent
// backend is configured.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/store"
)

// Compile-time interface check.
var _ store.Store = (*Store)(nil)

// Store is an in-memory implementation of the composite Bastion store.
type Store struct {
	mu sync.RWMutex

	// Routes
	routes map[string]*bastion.Route

	// Circuit breaker state
	cbStates map[string]*bastion.CircuitBreakerSnapshot

	// Health check history (per-target, newest first)
	healthChecks map[string][]bastion.HealthCheckResult
	healthCap    int

	// Cache
	cache    map[string]*cacheEntry
	cacheCap int

	// Rate limit buckets
	rateLimits map[string]*rateLimitEntry

	// Audit events
	auditEvents []bastion.AuditEvent
	auditCap    int
}

type cacheEntry struct {
	value     []byte
	expiresAt time.Time
}

type rateLimitEntry struct {
	tokens   float64
	lastTime time.Time
}

// New creates a new in-memory store.
func New() *Store {
	return &Store{
		routes:       make(map[string]*bastion.Route),
		cbStates:     make(map[string]*bastion.CircuitBreakerSnapshot),
		healthChecks: make(map[string][]bastion.HealthCheckResult),
		healthCap:    1000,
		cache:        make(map[string]*cacheEntry),
		cacheCap:     10000,
		rateLimits:   make(map[string]*rateLimitEntry),
		auditEvents:  make([]bastion.AuditEvent, 0),
		auditCap:     10000,
	}
}

// --- Lifecycle ---

func (s *Store) Migrate(_ context.Context) error { return nil }
func (s *Store) Ping(_ context.Context) error    { return nil }
func (s *Store) Close() error                    { return nil }

// --- RouteStore ---

func (s *Store) ListRoutes(_ context.Context) ([]*bastion.Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	routes := make([]*bastion.Route, 0, len(s.routes))
	for _, r := range s.routes {
		routes = append(routes, r)
	}

	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Priority > routes[j].Priority
	})

	return routes, nil
}

func (s *Store) GetRoute(_ context.Context, routeID string) (*bastion.Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.routes[routeID]
	if !ok {
		return nil, fmt.Errorf("route %q not found", routeID)
	}

	return r, nil
}

func (s *Store) SaveRoute(_ context.Context, route *bastion.Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.routes[route.ID] = route

	return nil
}

func (s *Store) DeleteRoute(_ context.Context, routeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.routes, routeID)

	return nil
}

// --- CircuitBreakerStore ---

func (s *Store) GetState(_ context.Context, targetID string) (*bastion.CircuitBreakerSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap, ok := s.cbStates[targetID]
	if !ok {
		return nil, nil
	}

	return snap, nil
}

func (s *Store) SaveState(_ context.Context, snap *bastion.CircuitBreakerSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cbStates[snap.TargetID] = snap

	return nil
}

func (s *Store) ListStates(_ context.Context) ([]*bastion.CircuitBreakerSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	states := make([]*bastion.CircuitBreakerSnapshot, 0, len(s.cbStates))
	for _, snap := range s.cbStates {
		states = append(states, snap)
	}

	return states, nil
}

func (s *Store) DeleteState(_ context.Context, targetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.cbStates, targetID)

	return nil
}

// --- HealthStore ---

func (s *Store) RecordCheck(_ context.Context, result *bastion.HealthCheckResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	history := s.healthChecks[result.TargetID]

	// Prepend (newest first)
	history = append([]bastion.HealthCheckResult{*result}, history...)

	// Trim to capacity
	if len(history) > s.healthCap {
		history = history[:s.healthCap]
	}

	s.healthChecks[result.TargetID] = history

	return nil
}

func (s *Store) GetHistory(_ context.Context, targetID string, limit int) ([]bastion.HealthCheckResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.healthChecks[targetID]
	if limit > 0 && limit < len(history) {
		history = history[:limit]
	}

	out := make([]bastion.HealthCheckResult, len(history))
	copy(out, history)

	return out, nil
}

func (s *Store) GetLatestState(_ context.Context, targetID string) (*bastion.HealthCheckResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.healthChecks[targetID]
	if len(history) == 0 {
		return nil, nil
	}

	result := history[0]

	return &result, nil
}

// --- CacheStore ---

func (s *Store) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.cache[key]
	if !ok {
		return nil, fmt.Errorf("cache miss: %s", key)
	}

	if time.Now().After(entry.expiresAt) {
		return nil, fmt.Errorf("cache expired: %s", key)
	}

	out := make([]byte, len(entry.value))
	copy(out, entry.value)

	return out, nil
}

func (s *Store) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Evict if at capacity
	if len(s.cache) >= s.cacheCap {
		// Remove oldest expired entry, or first entry
		var oldest string
		var oldestTime time.Time
		for k, v := range s.cache {
			if time.Now().After(v.expiresAt) {
				delete(s.cache, k)

				break
			}
			if oldest == "" || v.expiresAt.Before(oldestTime) {
				oldest = k
				oldestTime = v.expiresAt
			}
		}

		if len(s.cache) >= s.cacheCap && oldest != "" {
			delete(s.cache, oldest)
		}
	}

	data := make([]byte, len(value))
	copy(data, value)

	s.cache[key] = &cacheEntry{
		value:     data,
		expiresAt: time.Now().Add(ttl),
	}

	return nil
}

func (s *Store) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.cache, key)

	return nil
}

// --- RateLimitStore ---

func (s *Store) Allow(_ context.Context, key string, limit float64, burst int, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	entry, ok := s.rateLimits[key]

	if !ok {
		entry = &rateLimitEntry{
			tokens:   float64(burst),
			lastTime: now,
		}
		s.rateLimits[key] = entry
	}

	// Refill tokens
	elapsed := now.Sub(entry.lastTime).Seconds()
	entry.tokens += elapsed * limit
	if entry.tokens > float64(burst) {
		entry.tokens = float64(burst)
	}
	entry.lastTime = now

	if entry.tokens >= 1.0 {
		entry.tokens--
		return true, nil
	}

	return false, nil
}

// --- AuditSink ---

func (s *Store) Write(_ context.Context, event *bastion.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.auditEvents = append(s.auditEvents, *event)

	// Trim to capacity
	if len(s.auditEvents) > s.auditCap {
		s.auditEvents = s.auditEvents[len(s.auditEvents)-s.auditCap:]
	}

	return nil
}
