package resilience

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DrainConfig configures graceful request draining on shutdown.
type DrainConfig struct {
	// Enabled enables graceful draining.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Timeout is the maximum time to wait for in-flight requests to complete.
	Timeout time.Duration `json:"timeout" yaml:"timeout"`
}

// RequestDrainer tracks in-flight requests and supports graceful draining
// on shutdown. When draining begins, new requests are rejected with 503
// while existing requests are allowed to complete up to the drain timeout.
type RequestDrainer struct {
	config   DrainConfig
	draining atomic.Bool
	wg       sync.WaitGroup
	inflight atomic.Int64
}

// NewRequestDrainer creates a new request drainer.
func NewRequestDrainer(config DrainConfig) *RequestDrainer {
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}

	return &RequestDrainer{config: config}
}

// Acquire registers an in-flight request. Returns false if the gateway
// is draining and the request should be rejected.
func (d *RequestDrainer) Acquire() bool {
	if d.draining.Load() {
		return false
	}

	d.wg.Add(1)
	d.inflight.Add(1)

	// Double-check after acquiring
	if d.draining.Load() {
		d.wg.Done()
		d.inflight.Add(-1)

		return false
	}

	return true
}

// Release marks an in-flight request as completed.
func (d *RequestDrainer) Release() {
	d.inflight.Add(-1)
	d.wg.Done()
}

// IsDraining returns true if the gateway is in drain mode.
func (d *RequestDrainer) IsDraining() bool {
	return d.draining.Load()
}

// InFlight returns the number of in-flight requests.
func (d *RequestDrainer) InFlight() int64 {
	return d.inflight.Load()
}

// Drain initiates graceful draining. It blocks until all in-flight requests
// complete or the drain timeout is reached. Returns a context error if the
// timeout was exceeded.
func (d *RequestDrainer) Drain(ctx context.Context) error {
	if !d.config.Enabled {
		return nil
	}

	d.draining.Store(true)

	done := make(chan struct{})

	go func() {
		d.wg.Wait()
		close(done)
	}()

	timeout := time.After(d.config.Timeout)

	select {
	case <-done:
		return nil
	case <-timeout:
		return context.DeadlineExceeded
	case <-ctx.Done():
		return ctx.Err()
	}
}
