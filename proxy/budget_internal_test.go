package proxy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Concurrent requests and retries stay within the budget. Run with -race.
func TestRetryBudget_ConcurrentUse(t *testing.T) {
	b := &retryBudget{}
	now := time.Unix(1_000, 0)

	var wg sync.WaitGroup
	for range 1000 {
		wg.Go(func() { b.recordRequest(now) })
	}
	wg.Wait()

	var allowed atomic.Int64
	for range 1000 {
		wg.Go(func() {
			if b.tryRetry(now, 20) {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()

	if got := allowed.Load(); got != 200 {
		t.Errorf("allowed %d retries, want 200 (20%% of 1000)", got)
	}
}

func TestRetryBudget_WindowSlides(t *testing.T) {
	b := &retryBudget{}
	start := time.Unix(1_000, 0)

	for range 100 {
		b.recordRequest(start)
	}

	for i := range 10 {
		if !b.tryRetry(start, 10) {
			t.Fatalf("retry %d refused, want 10 of 100 allowed", i+1)
		}
	}
	if b.tryRetry(start, 10) {
		t.Fatal("11th retry allowed, want the budget spent")
	}

	// Nine seconds on, the burst is still inside the window.
	if b.tryRetry(start.Add(9*time.Second), 10) {
		t.Error("retry allowed 9s later, want the burst still counted")
	}

	// Ten seconds on, it has left the window, and the floor applies.
	later := start.Add(10 * time.Second)
	for i := range minRetriesPerWindow {
		if !b.tryRetry(later, 10) {
			t.Fatalf("retry %d refused after the window rolled, want the floor of %d", i+1, minRetriesPerWindow)
		}
	}
	if b.tryRetry(later, 10) {
		t.Error("retry past the floor allowed with no requests in the window")
	}
}
