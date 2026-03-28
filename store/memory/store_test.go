package memory

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/store"
)

func TestStoreImplementsInterface(t *testing.T) {
	var _ store.Store = (*Store)(nil)
}

func TestLifecycle(t *testing.T) {
	s := New()
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRouteStore(t *testing.T) {
	s := New()
	ctx := context.Background()

	// Empty list
	routes, err := s.ListRoutes(ctx)
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if len(routes) != 0 {
		t.Fatalf("expected 0 routes, got %d", len(routes))
	}

	// Save
	r := &bastion.Route{
		ID:       "r1",
		Path:     "/api/v1",
		Methods:  []string{"GET"},
		Priority: 10,
		Enabled:  true,
	}
	if err := s.SaveRoute(ctx, r); err != nil {
		t.Fatalf("SaveRoute: %v", err)
	}

	// Get
	got, err := s.GetRoute(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoute: %v", err)
	}
	if got.Path != "/api/v1" {
		t.Errorf("expected path /api/v1, got %s", got.Path)
	}

	// Get nonexistent
	_, err = s.GetRoute(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent route")
	}

	// Save another with lower priority
	r2 := &bastion.Route{
		ID:       "r2",
		Path:     "/api/v2",
		Priority: 5,
		Enabled:  true,
	}
	if err := s.SaveRoute(ctx, r2); err != nil {
		t.Fatalf("SaveRoute r2: %v", err)
	}

	// List should be sorted by priority DESC
	routes, _ = s.ListRoutes(ctx)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].ID != "r1" {
		t.Errorf("expected r1 first (higher priority), got %s", routes[0].ID)
	}

	// Delete
	if err := s.DeleteRoute(ctx, "r1"); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	routes, _ = s.ListRoutes(ctx)
	if len(routes) != 1 {
		t.Fatalf("expected 1 route after delete, got %d", len(routes))
	}
}

func TestCircuitBreakerStore(t *testing.T) {
	s := New()
	ctx := context.Background()

	// Get nonexistent returns nil
	snap, err := s.GetState(ctx, "target-1")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if snap != nil {
		t.Fatal("expected nil for nonexistent target")
	}

	// Save
	now := time.Now()
	cb := &bastion.CircuitBreakerSnapshot{
		TargetID:        "target-1",
		State:           bastion.CircuitClosed,
		FailureCount:    3,
		SuccessCount:    10,
		LastFailure:     now,
		LastStateChange: now,
		UpdatedAt:       now,
	}
	if err := s.SaveState(ctx, cb); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Get
	snap, err = s.GetState(ctx, "target-1")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if snap.FailureCount != 3 {
		t.Errorf("expected 3 failures, got %d", snap.FailureCount)
	}

	// List
	states, err := s.ListStates(ctx)
	if err != nil {
		t.Fatalf("ListStates: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("expected 1 state, got %d", len(states))
	}

	// Delete
	if err := s.DeleteState(ctx, "target-1"); err != nil {
		t.Fatalf("DeleteState: %v", err)
	}
	snap, _ = s.GetState(ctx, "target-1")
	if snap != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestHealthStore(t *testing.T) {
	s := New()
	ctx := context.Background()

	// Record checks
	for i := 0; i < 5; i++ {
		err := s.RecordCheck(ctx, &bastion.HealthCheckResult{
			TargetID:  "t1",
			TargetURL: "http://localhost:8080",
			Healthy:   i%2 == 0,
			Latency:   time.Duration(i) * time.Millisecond,
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("RecordCheck: %v", err)
		}
	}

	// GetHistory
	history, err := s.GetHistory(ctx, "t1", 3)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 results with limit, got %d", len(history))
	}

	// GetHistory unlimited
	history, _ = s.GetHistory(ctx, "t1", 0)
	if len(history) != 5 {
		t.Fatalf("expected 5 results, got %d", len(history))
	}

	// GetLatestState
	latest, err := s.GetLatestState(ctx, "t1")
	if err != nil {
		t.Fatalf("GetLatestState: %v", err)
	}
	if latest == nil {
		t.Fatal("expected non-nil latest")
	}

	// Nonexistent target
	latest, _ = s.GetLatestState(ctx, "nonexistent")
	if latest != nil {
		t.Fatal("expected nil for nonexistent target")
	}
}

func TestCacheStore(t *testing.T) {
	s := New()
	ctx := context.Background()

	// Miss
	_, err := s.Get(ctx, "key1")
	if err == nil {
		t.Fatal("expected cache miss error")
	}

	// Set and Get
	if err := s.Set(ctx, "key1", []byte("value1"), 5*time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	val, err := s.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "value1" {
		t.Errorf("expected 'value1', got '%s'", string(val))
	}

	// Overwrite
	if err := s.Set(ctx, "key1", []byte("updated"), 5*time.Minute); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	val, _ = s.Get(ctx, "key1")
	if string(val) != "updated" {
		t.Errorf("expected 'updated', got '%s'", string(val))
	}

	// Delete
	if err := s.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = s.Get(ctx, "key1")
	if err == nil {
		t.Fatal("expected cache miss after delete")
	}
}

func TestRateLimitStore(t *testing.T) {
	s := New()
	ctx := context.Background()

	// First call should be allowed (burst = 2)
	allowed, err := s.Allow(ctx, "client-1", 1.0, 2, time.Second)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !allowed {
		t.Error("first request should be allowed")
	}

	// Second call should be allowed
	allowed, _ = s.Allow(ctx, "client-1", 1.0, 2, time.Second)
	if !allowed {
		t.Error("second request should be allowed")
	}

	// Third call should be rate-limited (burst exhausted)
	allowed, _ = s.Allow(ctx, "client-1", 1.0, 2, time.Second)
	if allowed {
		t.Error("third request should be rate-limited")
	}
}

func TestAuditSink(t *testing.T) {
	s := New()
	ctx := context.Background()

	event := &bastion.AuditEvent{
		Action:    bastion.AuditRouteCreated,
		Actor:     "admin",
		Resource:  "route/r1",
		Result:    "success",
		Timestamp: time.Now(),
	}

	if err := s.Write(ctx, event); err != nil {
		t.Fatalf("Write: %v", err)
	}
}
