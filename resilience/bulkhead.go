package resilience

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

// BulkheadConfig configures per-upstream concurrency limiting.
type BulkheadConfig struct {
	// Enabled enables bulkhead concurrency limiting.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// MaxConcurrent is the maximum concurrent requests per upstream target.
	MaxConcurrent int `json:"maxConcurrent" yaml:"max_concurrent"`

	// MaxWaitQueue is the maximum number of requests waiting for a slot.
	// When exceeded, requests are rejected immediately with 503.
	MaxWaitQueue int `json:"maxWaitQueue" yaml:"max_wait_queue"`
}

// Bulkhead limits concurrency to individual upstream targets,
// preventing one misbehaving upstream from consuming all goroutines.
type Bulkhead struct {
	mu   sync.RWMutex
	sems map[string]chan struct{}

	config BulkheadConfig
}

// NewBulkhead creates a new bulkhead limiter.
func NewBulkhead(config BulkheadConfig) *Bulkhead {
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 64
	}

	return &Bulkhead{
		sems:   make(map[string]chan struct{}),
		config: config,
	}
}

// Acquire attempts to acquire a concurrency slot for the given target.
// It returns a release function and nil error on success. If the bulkhead
// is full and the wait queue is also full, it returns a non-nil error.
func (b *Bulkhead) Acquire(ctx context.Context, targetID string) (release func(), err error) {
	if !b.config.Enabled {
		return func() {}, nil
	}

	sem := b.getSem(targetID)

	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, fmt.Errorf("bulkhead full for target %s", targetID)
	}
}

// AcquireHTTP is a convenience wrapper that writes a 503 on failure.
func (b *Bulkhead) AcquireHTTP(w http.ResponseWriter, r *http.Request, targetID string) (release func(), ok bool) {
	release, err := b.Acquire(r.Context(), targetID)
	if err != nil {
		http.Error(w, `{"error":"service overloaded"}`, http.StatusServiceUnavailable)
		return nil, false
	}

	return release, true
}

func (b *Bulkhead) getSem(targetID string) chan struct{} {
	b.mu.RLock()
	sem, ok := b.sems[targetID]
	b.mu.RUnlock()

	if ok {
		return sem
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if sem, ok = b.sems[targetID]; ok {
		return sem
	}

	capacity := b.config.MaxConcurrent
	if b.config.MaxWaitQueue > 0 {
		capacity += b.config.MaxWaitQueue
	}

	sem = make(chan struct{}, capacity)
	b.sems[targetID] = sem

	return sem
}

// Remove cleans up the semaphore for a target that's been deregistered.
func (b *Bulkhead) Remove(targetID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.sems, targetID)
}
