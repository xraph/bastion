package resilience

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRequestDrainer_AcquireRelease(t *testing.T) {
	d := NewRequestDrainer(DrainConfig{Enabled: true, Timeout: 5 * time.Second})

	if !d.Acquire() {
		t.Fatal("acquire should succeed when not draining")
	}

	if d.InFlight() != 1 {
		t.Errorf("expected 1 in-flight, got %d", d.InFlight())
	}

	d.Release()

	if d.InFlight() != 0 {
		t.Errorf("expected 0 in-flight after release, got %d", d.InFlight())
	}
}

func TestRequestDrainer_RejectsWhenDraining(t *testing.T) {
	d := NewRequestDrainer(DrainConfig{Enabled: true, Timeout: 1 * time.Second})

	// Start draining in background
	go func() {
		d.Drain(context.Background()) //nolint:errcheck
	}()

	// Wait a moment for drain to start
	time.Sleep(10 * time.Millisecond)

	if d.Acquire() {
		t.Fatal("acquire should fail when draining")
	}
}

func TestRequestDrainer_WaitsForInFlight(t *testing.T) {
	d := NewRequestDrainer(DrainConfig{Enabled: true, Timeout: 5 * time.Second})

	d.Acquire()

	done := make(chan error, 1)

	go func() {
		done <- d.Drain(context.Background())
	}()

	// Drain should be blocking
	select {
	case <-done:
		t.Fatal("drain should block while requests are in-flight")
	case <-time.After(50 * time.Millisecond):
		// Expected
	}

	d.Release()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("drain should complete without error, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("drain should complete after release")
	}
}

func TestRequestDrainer_Timeout(t *testing.T) {
	d := NewRequestDrainer(DrainConfig{Enabled: true, Timeout: 50 * time.Millisecond})

	d.Acquire()
	// Don't release - let drain timeout

	err := d.Drain(context.Background())
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}

	d.Release()
}

func TestRequestDrainer_Disabled(t *testing.T) {
	d := NewRequestDrainer(DrainConfig{Enabled: false})

	d.Acquire()

	err := d.Drain(context.Background())
	if err != nil {
		t.Errorf("disabled drainer should return nil, got: %v", err)
	}

	d.Release()
}

func TestRequestDrainer_ConcurrentAcquire(t *testing.T) {
	d := NewRequestDrainer(DrainConfig{Enabled: true, Timeout: 5 * time.Second})

	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if d.Acquire() {
				defer d.Release()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()

	if d.InFlight() != 0 {
		t.Errorf("expected 0 in-flight after all released, got %d", d.InFlight())
	}
}
