package resilience

import (
	"sync"
	"time"

	bastion "github.com/xraph/bastion"
)

// CircuitBreakerMode defines how failures are measured.
type CircuitBreakerMode string

const (
	// CBModeCount trips the breaker after N consecutive failures (default).
	CBModeCount CircuitBreakerMode = "count"

	// CBModeRate trips the breaker when failure percentage exceeds threshold
	// within a sliding window.
	CBModeRate CircuitBreakerMode = "rate"
)

// EnhancedCBConfig extends CircuitBreakerConfig with advanced options.
type EnhancedCBConfig struct {
	bastion.CircuitBreakerConfig

	// Mode determines how failures are evaluated ("count" or "rate").
	Mode CircuitBreakerMode `json:"mode" yaml:"mode"`

	// FailureRateThreshold is the failure percentage (0-100) that triggers
	// the breaker in "rate" mode.
	FailureRateThreshold float64 `json:"failureRateThreshold" yaml:"failure_rate_threshold"`

	// MinRequestsInWindow is the minimum number of requests needed in the
	// sliding window before the failure rate is evaluated.
	MinRequestsInWindow int `json:"minRequestsInWindow" yaml:"min_requests_in_window"`

	// SlowCallDurationThreshold defines a request as "slow" if it exceeds
	// this duration.
	SlowCallDurationThreshold time.Duration `json:"slowCallDurationThreshold" yaml:"slow_call_duration_threshold"`

	// SlowCallRateThreshold is the percentage (0-100) of slow calls that
	// triggers the breaker.
	SlowCallRateThreshold float64 `json:"slowCallRateThreshold" yaml:"slow_call_rate_threshold"`
}

// SlidingWindowCircuitBreaker extends CircuitBreaker with failure-rate
// and slow-call detection using a sliding window.
type SlidingWindowCircuitBreaker struct {
	*CircuitBreaker

	mu      sync.Mutex
	config  EnhancedCBConfig
	window  *slidingWindow
	slowCfg bool
}

type slidingWindow struct {
	entries    []windowEntry
	maxAge     time.Duration
	totalCount int
	failCount  int
	slowCount  int
}

type windowEntry struct {
	timestamp time.Time
	failed    bool
	slow      bool
}

func newSlidingWindow(maxAge time.Duration) *slidingWindow {
	return &slidingWindow{
		maxAge: maxAge,
	}
}

func (sw *slidingWindow) record(failed, slow bool) {
	sw.evict()

	sw.entries = append(sw.entries, windowEntry{
		timestamp: time.Now(),
		failed:    failed,
		slow:      slow,
	})

	sw.totalCount++

	if failed {
		sw.failCount++
	}

	if slow {
		sw.slowCount++
	}
}

func (sw *slidingWindow) failureRate() float64 {
	sw.evict()

	if sw.totalCount == 0 {
		return 0
	}

	return float64(sw.failCount) / float64(sw.totalCount) * 100
}

func (sw *slidingWindow) slowCallRate() float64 {
	sw.evict()

	if sw.totalCount == 0 {
		return 0
	}

	return float64(sw.slowCount) / float64(sw.totalCount) * 100
}

func (sw *slidingWindow) count() int {
	sw.evict()

	return sw.totalCount
}

func (sw *slidingWindow) evict() {
	cutoff := time.Now().Add(-sw.maxAge)
	i := 0

	for i < len(sw.entries) && sw.entries[i].timestamp.Before(cutoff) {
		if sw.entries[i].failed {
			sw.failCount--
		}

		if sw.entries[i].slow {
			sw.slowCount--
		}

		sw.totalCount--
		i++
	}

	if i > 0 {
		sw.entries = sw.entries[i:]
	}
}

func (sw *slidingWindow) reset() {
	sw.entries = sw.entries[:0]
	sw.totalCount = 0
	sw.failCount = 0
	sw.slowCount = 0
}

// NewSlidingWindowCircuitBreaker creates an enhanced circuit breaker with
// sliding window failure-rate and slow-call detection.
func NewSlidingWindowCircuitBreaker(targetID string, config EnhancedCBConfig) *SlidingWindowCircuitBreaker {
	if config.Mode == "" {
		config.Mode = CBModeCount
	}

	if config.MinRequestsInWindow <= 0 {
		config.MinRequestsInWindow = 10
	}

	windowDuration := config.FailureWindow
	if windowDuration == 0 {
		windowDuration = 60 * time.Second
	}

	return &SlidingWindowCircuitBreaker{
		CircuitBreaker: NewCircuitBreaker(targetID, config.CircuitBreakerConfig),
		config:         config,
		window:         newSlidingWindow(windowDuration),
		slowCfg:        config.SlowCallDurationThreshold > 0 && config.SlowCallRateThreshold > 0,
	}
}

// RecordResult records a request outcome with latency tracking.
func (cb *SlidingWindowCircuitBreaker) RecordResult(failed bool, latency time.Duration) {
	if !cb.config.Enabled {
		return
	}

	if cb.config.Mode == CBModeCount {
		// Delegate to base circuit breaker for count mode
		if failed {
			cb.CircuitBreaker.RecordFailure()
		} else {
			cb.CircuitBreaker.RecordSuccess()
		}

		return
	}

	// Rate mode: use sliding window
	cb.mu.Lock()
	defer cb.mu.Unlock()

	slow := cb.slowCfg && latency > cb.config.SlowCallDurationThreshold
	cb.window.record(failed, slow)

	state := cb.CircuitBreaker.State()

	switch state {
	case bastion.CircuitClosed:
		if cb.window.count() >= cb.config.MinRequestsInWindow {
			shouldTrip := false

			// Check failure rate
			if cb.config.FailureRateThreshold > 0 && cb.window.failureRate() >= cb.config.FailureRateThreshold {
				shouldTrip = true
			}

			// Check slow call rate
			if cb.slowCfg && cb.window.slowCallRate() >= cb.config.SlowCallRateThreshold {
				shouldTrip = true
			}

			if shouldTrip {
				cb.CircuitBreaker.mu.Lock()
				cb.CircuitBreaker.transitionTo(bastion.CircuitOpen)
				cb.CircuitBreaker.mu.Unlock()
			}
		}

	case bastion.CircuitHalfOpen:
		if failed {
			cb.CircuitBreaker.RecordFailure()
		} else {
			cb.CircuitBreaker.RecordSuccess()
		}

		// Reset window on state change
		if cb.CircuitBreaker.State() != bastion.CircuitHalfOpen {
			cb.window.reset()
		}
	}
}

// Metrics returns the current circuit breaker metrics.
func (cb *SlidingWindowCircuitBreaker) Metrics() CircuitBreakerMetrics {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	return CircuitBreakerMetrics{
		State:       cb.CircuitBreaker.State(),
		FailureRate: cb.window.failureRate(),
		SlowRate:    cb.window.slowCallRate(),
		TotalCount:  cb.window.count(),
	}
}

// CircuitBreakerMetrics holds observable metrics for a circuit breaker.
type CircuitBreakerMetrics struct {
	State       bastion.CircuitState `json:"state"`
	FailureRate float64              `json:"failureRate"`
	SlowRate    float64              `json:"slowRate"`
	TotalCount  int                  `json:"totalCount"`
}
