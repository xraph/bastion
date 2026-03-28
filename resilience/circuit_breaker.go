package resilience

import (
	"sync"
	"time"

	bastion "github.com/xraph/bastion"
)

// CircuitBreaker implements a per-target circuit breaker with three states.
type CircuitBreaker struct {
	mu sync.RWMutex

	state           bastion.CircuitState
	failureCount    int
	successCount    int
	halfOpenReqs    int
	lastFailure     time.Time
	lastStateChange time.Time

	config bastion.CircuitBreakerConfig

	// Callbacks
	onStateChange func(targetID string, from, to bastion.CircuitState)
	targetID      string
}

// NewCircuitBreaker creates a new circuit breaker for a target.
func NewCircuitBreaker(targetID string, config bastion.CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		state:           bastion.CircuitClosed,
		config:          config,
		targetID:        targetID,
		lastStateChange: time.Now(),
	}
}

// SetOnStateChange sets the state change callback.
func (cb *CircuitBreaker) SetOnStateChange(fn func(targetID string, from, to bastion.CircuitState)) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.onStateChange = fn
}

// Allow checks if a request is allowed through the circuit breaker.
func (cb *CircuitBreaker) Allow() bool {
	if !cb.config.Enabled {
		return true
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case bastion.CircuitClosed:
		return true

	case bastion.CircuitOpen:
		// Check if reset timeout has elapsed
		if time.Since(cb.lastStateChange) > cb.config.ResetTimeout {
			cb.transitionTo(bastion.CircuitHalfOpen)
			cb.halfOpenReqs = 1

			return true
		}

		return false

	case bastion.CircuitHalfOpen:
		if cb.halfOpenReqs < cb.config.HalfOpenMax {
			cb.halfOpenReqs++

			return true
		}

		return false
	}

	return true
}

// RecordSuccess records a successful request.
func (cb *CircuitBreaker) RecordSuccess() {
	if !cb.config.Enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case bastion.CircuitHalfOpen:
		cb.successCount++

		if cb.successCount >= cb.config.HalfOpenMax {
			cb.transitionTo(bastion.CircuitClosed)
			cb.failureCount = 0
			cb.successCount = 0
		}

	case bastion.CircuitClosed:
		// Reset failure count on success
		cb.failureCount = 0
	}
}

// RecordFailure records a failed request.
func (cb *CircuitBreaker) RecordFailure() {
	if !cb.config.Enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.lastFailure = time.Now()

	switch cb.state {
	case bastion.CircuitClosed:
		// Check if failures are within the window
		if cb.config.FailureWindow > 0 && time.Since(cb.lastStateChange) > cb.config.FailureWindow {
			// Reset if outside window
			cb.failureCount = 1
			cb.lastStateChange = time.Now()
		} else {
			cb.failureCount++
		}

		if cb.failureCount >= cb.config.FailureThreshold {
			cb.transitionTo(bastion.CircuitOpen)
		}

	case bastion.CircuitHalfOpen:
		// Any failure in half-open goes back to open
		cb.transitionTo(bastion.CircuitOpen)
		cb.halfOpenReqs = 0
		cb.successCount = 0
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() bastion.CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	// Check if open circuit should transition to half-open
	if cb.state == bastion.CircuitOpen && time.Since(cb.lastStateChange) > cb.config.ResetTimeout {
		return bastion.CircuitHalfOpen
	}

	return cb.state
}

// Reset resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.transitionTo(bastion.CircuitClosed)
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenReqs = 0
}

// transitionTo changes state (must be called with lock held).
func (cb *CircuitBreaker) transitionTo(newState bastion.CircuitState) {
	oldState := cb.state
	if oldState == newState {
		return
	}

	cb.state = newState
	cb.lastStateChange = time.Now()

	if cb.onStateChange != nil {
		go cb.onStateChange(cb.targetID, oldState, newState)
	}
}

// CBManager manages circuit breakers for all targets.
type CBManager struct {
	mu       sync.RWMutex
	breakers map[string]*CircuitBreaker
	config   bastion.CircuitBreakerConfig
	onChange func(targetID string, from, to bastion.CircuitState)
}

// NewCBManager creates a new circuit breaker manager.
func NewCBManager(config bastion.CircuitBreakerConfig) *CBManager {
	return &CBManager{
		breakers: make(map[string]*CircuitBreaker),
		config:   config,
	}
}

// SetOnStateChange sets the global state change callback.
func (m *CBManager) SetOnStateChange(fn func(targetID string, from, to bastion.CircuitState)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.onChange = fn

	for _, cb := range m.breakers {
		cb.SetOnStateChange(fn)
	}
}

// Get returns the circuit breaker for a target, creating one if necessary.
func (m *CBManager) Get(targetID string) bastion.Breaker {
	m.mu.RLock()
	if cb, ok := m.breakers[targetID]; ok {
		m.mu.RUnlock()

		return cb
	}

	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if cb, ok := m.breakers[targetID]; ok {
		return cb
	}

	cb := NewCircuitBreaker(targetID, m.config)
	if m.onChange != nil {
		cb.SetOnStateChange(m.onChange)
	}

	m.breakers[targetID] = cb

	return cb
}

// GetWithConfig returns a circuit breaker with a per-route config override.
func (m *CBManager) GetWithConfig(targetID string, cfg *bastion.CBConfig) bastion.Breaker {
	if cfg == nil {
		return m.Get(targetID)
	}

	// For per-route overrides, use a composite key
	key := targetID + ":" + cbConfigKey(cfg)

	m.mu.RLock()
	if cb, ok := m.breakers[key]; ok {
		m.mu.RUnlock()

		return cb
	}

	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	if cb, ok := m.breakers[key]; ok {
		return cb
	}

	overrideCfg := m.config
	overrideCfg.FailureThreshold = cfg.FailureThreshold
	overrideCfg.ResetTimeout = cfg.ResetTimeout
	overrideCfg.HalfOpenMax = cfg.HalfOpenMax

	cb := NewCircuitBreaker(targetID, overrideCfg)
	if m.onChange != nil {
		cb.SetOnStateChange(m.onChange)
	}

	m.breakers[key] = cb

	return cb
}

// Remove removes a circuit breaker for a target.
func (m *CBManager) Remove(targetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.breakers, targetID)
}

// cbConfigKey generates a composite key for per-route config overrides.
func cbConfigKey(cfg *bastion.CBConfig) string {
	return string(rune(cfg.FailureThreshold)) + string(rune(cfg.HalfOpenMax))
}
