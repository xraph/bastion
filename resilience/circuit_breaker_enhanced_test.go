package resilience

import (
	"testing"
	"time"

	bastion "github.com/xraph/bastion"
)

func TestSlidingWindowCB_CountMode(t *testing.T) {
	cb := NewSlidingWindowCircuitBreaker("t1", EnhancedCBConfig{
		CircuitBreakerConfig: bastion.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 3,
			FailureWindow:    10 * time.Second,
			ResetTimeout:     100 * time.Millisecond,
			HalfOpenMax:      1,
		},
		Mode: CBModeCount,
	})

	// Should start closed
	if cb.State() != bastion.CircuitClosed {
		t.Errorf("expected closed, got %s", cb.State())
	}

	// Record failures
	cb.RecordResult(true, time.Millisecond)
	cb.RecordResult(true, time.Millisecond)
	cb.RecordResult(true, time.Millisecond)

	if cb.State() != bastion.CircuitOpen {
		t.Errorf("expected open after 3 failures, got %s", cb.State())
	}
}

func TestSlidingWindowCB_RateMode(t *testing.T) {
	cb := NewSlidingWindowCircuitBreaker("t1", EnhancedCBConfig{
		CircuitBreakerConfig: bastion.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 100, // High threshold so count mode won't trip
			FailureWindow:    10 * time.Second,
			ResetTimeout:     100 * time.Millisecond,
			HalfOpenMax:      1,
		},
		Mode:                 CBModeRate,
		FailureRateThreshold: 50, // 50% failure rate
		MinRequestsInWindow:  5,
	})

	// Record mix of successes and failures
	cb.RecordResult(false, time.Millisecond)
	cb.RecordResult(false, time.Millisecond)
	cb.RecordResult(true, time.Millisecond)
	cb.RecordResult(true, time.Millisecond)

	// Not enough requests yet (min 5)
	if cb.State() != bastion.CircuitClosed {
		t.Errorf("expected closed (below min requests), got %s", cb.State())
	}

	cb.RecordResult(true, time.Millisecond) // 3/5 = 60% failure rate

	if cb.State() != bastion.CircuitOpen {
		t.Errorf("expected open at 60%% failure rate (threshold 50%%), got %s", cb.State())
	}
}

func TestSlidingWindowCB_SlowCallDetection(t *testing.T) {
	cb := NewSlidingWindowCircuitBreaker("t1", EnhancedCBConfig{
		CircuitBreakerConfig: bastion.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 100,
			FailureWindow:    10 * time.Second,
			ResetTimeout:     100 * time.Millisecond,
			HalfOpenMax:      1,
		},
		Mode:                      CBModeRate,
		FailureRateThreshold:      100, // Disable failure rate
		MinRequestsInWindow:       5,
		SlowCallDurationThreshold: 100 * time.Millisecond,
		SlowCallRateThreshold:     50,
	})

	// All successful but slow
	cb.RecordResult(false, 200*time.Millisecond) // slow
	cb.RecordResult(false, 200*time.Millisecond) // slow
	cb.RecordResult(false, 200*time.Millisecond) // slow
	cb.RecordResult(false, 10*time.Millisecond)  // fast
	cb.RecordResult(false, 10*time.Millisecond)  // fast

	// 3/5 = 60% slow calls (threshold 50%)
	if cb.State() != bastion.CircuitOpen {
		t.Errorf("expected open due to slow calls, got %s", cb.State())
	}
}

func TestSlidingWindowCB_Metrics(t *testing.T) {
	cb := NewSlidingWindowCircuitBreaker("t1", EnhancedCBConfig{
		CircuitBreakerConfig: bastion.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 100,
			FailureWindow:    10 * time.Second,
			ResetTimeout:     time.Second,
			HalfOpenMax:      1,
		},
		Mode:                CBModeRate,
		MinRequestsInWindow: 100,
	})

	cb.RecordResult(false, time.Millisecond)
	cb.RecordResult(true, time.Millisecond)
	cb.RecordResult(false, time.Millisecond)

	m := cb.Metrics()
	if m.TotalCount != 3 {
		t.Errorf("expected 3 total, got %d", m.TotalCount)
	}

	expectedRate := 1.0 / 3.0 * 100
	if m.FailureRate < expectedRate-1 || m.FailureRate > expectedRate+1 {
		t.Errorf("expected ~%.1f%% failure rate, got %.1f%%", expectedRate, m.FailureRate)
	}
}

func TestSlidingWindowCB_WindowEviction(t *testing.T) {
	cb := NewSlidingWindowCircuitBreaker("t1", EnhancedCBConfig{
		CircuitBreakerConfig: bastion.CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 100,
			FailureWindow:    50 * time.Millisecond, // Very short window
			ResetTimeout:     time.Second,
			HalfOpenMax:      1,
		},
		Mode:                 CBModeRate,
		MinRequestsInWindow:  2,
		FailureRateThreshold: 50,
	})

	// Record failures
	cb.RecordResult(true, time.Millisecond)
	cb.RecordResult(true, time.Millisecond)

	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)

	// New requests after window expiry
	cb.RecordResult(false, time.Millisecond)
	cb.RecordResult(false, time.Millisecond)

	m := cb.Metrics()
	if m.FailureRate > 0 {
		t.Errorf("expected 0%% failure rate after window expiry, got %.1f%%", m.FailureRate)
	}
}
