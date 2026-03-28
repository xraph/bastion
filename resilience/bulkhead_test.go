package resilience

import (
	"context"
	"sync"
	"testing"
)

func TestBulkhead_Disabled(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{Enabled: false})

	release, err := b.Acquire(context.Background(), "t1")
	if err != nil {
		t.Fatalf("disabled bulkhead should always allow: %v", err)
	}

	release()
}

func TestBulkhead_AcquireRelease(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{Enabled: true, MaxConcurrent: 2})

	r1, err := b.Acquire(context.Background(), "t1")
	if err != nil {
		t.Fatalf("first acquire should succeed: %v", err)
	}

	r2, err := b.Acquire(context.Background(), "t1")
	if err != nil {
		t.Fatalf("second acquire should succeed: %v", err)
	}

	// Third should fail (capacity = 2)
	_, err = b.Acquire(context.Background(), "t1")
	if err == nil {
		t.Fatal("third acquire should fail when capacity is 2")
	}

	r1()
	// Now one slot is free
	r3, err := b.Acquire(context.Background(), "t1")
	if err != nil {
		t.Fatalf("acquire after release should succeed: %v", err)
	}

	r2()
	r3()
}

func TestBulkhead_IndependentTargets(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{Enabled: true, MaxConcurrent: 1})

	r1, err := b.Acquire(context.Background(), "t1")
	if err != nil {
		t.Fatalf("t1 acquire should succeed: %v", err)
	}

	// Different target should have its own semaphore
	r2, err := b.Acquire(context.Background(), "t2")
	if err != nil {
		t.Fatalf("t2 acquire should succeed independently: %v", err)
	}

	r1()
	r2()
}

func TestBulkhead_ConcurrentAccess(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{Enabled: true, MaxConcurrent: 10})

	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			release, err := b.Acquire(context.Background(), "t1")
			if err != nil {
				return
			}

			defer release()
		}()
	}

	wg.Wait()
}

func TestBulkhead_Remove(t *testing.T) {
	b := NewBulkhead(BulkheadConfig{Enabled: true, MaxConcurrent: 2})

	_, err := b.Acquire(context.Background(), "t1")
	if err != nil {
		t.Fatalf("acquire should succeed: %v", err)
	}

	b.Remove("t1")
	// After remove, a new semaphore is created
	_, err = b.Acquire(context.Background(), "t1")
	if err != nil {
		t.Fatalf("acquire after remove should succeed: %v", err)
	}
}
