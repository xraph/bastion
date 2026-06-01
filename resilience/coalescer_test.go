package resilience

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestCoalescer_Disabled(t *testing.T) {
	rc := NewRequestCoalescer(CoalescerConfig{Enabled: false})

	var calls atomic.Int32

	resp, err := rc.Do("key", func() (*http.Response, error) {
		calls.Add(1)

		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusOK)
		rec.Write([]byte("ok")) //nolint:errcheck

		return rec.Result(), nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	if calls.Load() != 1 {
		t.Errorf("expected 1 call, got %d", calls.Load())
	}
}

func TestRequestCoalescer_CoalescesRequests(t *testing.T) {
	rc := NewRequestCoalescer(CoalescerConfig{Enabled: true})

	var calls atomic.Int32
	started := make(chan struct{})
	barrier := make(chan struct{})

	doFn := func() (*http.Response, error) {
		calls.Add(1)
		close(started) // Signal that the first call has started
		<-barrier      // Wait until released

		rec := httptest.NewRecorder()
		rec.WriteHeader(http.StatusOK)
		rec.Write([]byte("response")) //nolint:errcheck

		return rec.Result(), nil
	}

	// Start the first call - it will block on the barrier
	var wg sync.WaitGroup
	results := make([]*CoalescedResponse, 5)
	errors := make([]error, 5)

	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], errors[0] = rc.Do("same-key", doFn)
	}()

	// Wait for the first call to actually start executing
	<-started

	// Now launch 4 more concurrent calls - they should coalesce
	for i := 1; i < 5; i++ {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()
			results[idx], errors[idx] = rc.Do("same-key", doFn)
		}(i)
	}

	// Wait until all 4 followers have registered as coalesced waiters on the
	// in-flight call. This guarantees they observed the first call before the
	// barrier is released. Otherwise releasing the barrier could let the first
	// call complete and leave the flight map, causing a late follower to start
	// a second (non-coalesced) call and invoke doFn again — closing the already
	// closed started channel.
	for waiterCount(rc, "same-key") < 4 {
		time.Sleep(time.Millisecond)
	}

	// Release the barrier so the single call completes
	close(barrier)
	wg.Wait()

	if calls.Load() != 1 {
		t.Errorf("expected 1 call (coalesced), got %d", calls.Load())
	}

	for i, err := range errors {
		if err != nil {
			t.Errorf("goroutine %d got error: %v", i, err)
		}
	}

	for i, resp := range results {
		if resp == nil {
			t.Errorf("goroutine %d got nil response", i)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			t.Errorf("goroutine %d got status %d", i, resp.StatusCode)
		}
	}
}

// waiterCount returns the number of coalesced followers currently waiting on
// the in-flight call for key. It lets the test deterministically coordinate
// with the coalescer's internal state instead of relying on goroutine timing.
func waiterCount(rc *RequestCoalescer, key string) int {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if c, ok := rc.flight[key]; ok {
		return c.waiters
	}

	return 0
}

func TestCoalesceKey_GETOnly(t *testing.T) {
	tests := []struct {
		method  string
		wantKey bool
	}{
		{"GET", true},
		{"HEAD", true},
		{"POST", false},
		{"PUT", false},
		{"DELETE", false},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, "/api/users", nil)
		_, ok := CoalesceKey(req, "http://backend:8080")

		if ok != tt.wantKey {
			t.Errorf("%s: CoalesceKey ok=%v, want %v", tt.method, ok, tt.wantKey)
		}
	}
}
