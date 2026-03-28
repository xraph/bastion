package routing

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	bastion "github.com/xraph/bastion"
)

// TestRemoveByServiceName_PreservesPrioritySortOrder verifies that after
// removing routes for one service, the remaining routes are still sorted
// by priority descending — the invariant MatchRoute depends on.
func TestRemoveByServiceName_PreservesPrioritySortOrder(t *testing.T) {
	rm := NewManager()

	// Interleave two services with different priorities.
	routes := []*bastion.Route{
		{ID: "a-60", Path: "/a/high", ServiceName: "service-a", Priority: 60, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t1", URL: "http://a:1"}}},
		{ID: "b-50", Path: "/b/high", ServiceName: "service-b", Priority: 50, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t2", URL: "http://b:1"}}},
		{ID: "b-40", Path: "/b/med", ServiceName: "service-b", Priority: 40, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t3", URL: "http://b:2"}}},
		{ID: "a-30", Path: "/a/med", ServiceName: "service-a", Priority: 30, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t4", URL: "http://a:2"}}},
		{ID: "b-20", Path: "/b/low", ServiceName: "service-b", Priority: 20, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t5", URL: "http://b:3"}}},
		{ID: "a-10", Path: "/a/low", ServiceName: "service-a", Priority: 10, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t6", URL: "http://a:3"}}},
	}

	for _, r := range routes {
		if err := rm.AddRoute(r); err != nil {
			t.Fatalf("AddRoute(%s): %v", r.ID, err)
		}
	}

	// Remove service-a.
	rm.RemoveByServiceName("service-a")

	remaining := rm.ListRoutes()
	if len(remaining) != 3 {
		t.Fatalf("expected 3 remaining routes, got %d", len(remaining))
	}

	// Verify descending priority order.
	for i := 1; i < len(remaining); i++ {
		if remaining[i-1].Priority < remaining[i].Priority {
			t.Errorf("routes not sorted by priority: [%d].Priority=%d < [%d].Priority=%d",
				i-1, remaining[i-1].Priority, i, remaining[i].Priority)
		}
	}

	// Verify expected priorities.
	expectedPriorities := []int{50, 40, 20}
	for i, want := range expectedPriorities {
		if remaining[i].Priority != want {
			t.Errorf("remaining[%d].Priority = %d, want %d", i, remaining[i].Priority, want)
		}
	}

	// Verify service-a routes are gone.
	for _, id := range []string{"a-60", "a-30", "a-10"} {
		if _, ok := rm.GetRoute(id); ok {
			t.Errorf("route %s should have been removed", id)
		}
	}

	// Verify MatchRoute still works for service-b paths.
	if r := rm.MatchRoute("/b/high", "GET"); r == nil || r.ID != "b-50" {
		t.Error("MatchRoute(/b/high) failed after RemoveByServiceName")
	}
}

// TestRemoveBySource_PreservesPrioritySortOrder verifies sort order is
// preserved after removing routes by source.
func TestRemoveBySource_PreservesPrioritySortOrder(t *testing.T) {
	rm := NewManager()

	routes := []*bastion.Route{
		{ID: "farp-50", Path: "/farp/high", Priority: 50, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t1", URL: "http://f:1"}}},
		{ID: "disc-40", Path: "/disc/high", Priority: 40, Enabled: true, Source: bastion.SourceDiscovery, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t2", URL: "http://d:1"}}},
		{ID: "farp-30", Path: "/farp/med", Priority: 30, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t3", URL: "http://f:2"}}},
		{ID: "disc-20", Path: "/disc/med", Priority: 20, Enabled: true, Source: bastion.SourceDiscovery, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t4", URL: "http://d:2"}}},
		{ID: "farp-10", Path: "/farp/low", Priority: 10, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t5", URL: "http://f:3"}}},
		{ID: "disc-15", Path: "/disc/low", Priority: 15, Enabled: true, Source: bastion.SourceDiscovery, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t6", URL: "http://d:3"}}},
	}

	for _, r := range routes {
		if err := rm.AddRoute(r); err != nil {
			t.Fatalf("AddRoute(%s): %v", r.ID, err)
		}
	}

	rm.RemoveBySource(bastion.SourceFARP)

	remaining := rm.ListRoutes()
	if len(remaining) != 3 {
		t.Fatalf("expected 3 remaining routes, got %d", len(remaining))
	}

	expectedPriorities := []int{40, 20, 15}
	for i, want := range expectedPriorities {
		if remaining[i].Priority != want {
			t.Errorf("remaining[%d].Priority = %d, want %d", i, remaining[i].Priority, want)
		}
	}

	// All remaining should be SourceDiscovery.
	for _, r := range remaining {
		if r.Source != bastion.SourceDiscovery {
			t.Errorf("route %s source = %s, want %s", r.ID, r.Source, bastion.SourceDiscovery)
		}
	}
}

// TestConcurrentMatchDuringRemoveByServiceName verifies no panics or data
// races when MatchRoute runs concurrently with RemoveByServiceName+AddRoute.
func TestConcurrentMatchDuringRemoveByServiceName(t *testing.T) {
	rm := NewManager()

	services := []string{"svc-a", "svc-b", "svc-c", "svc-d"}
	routesPerService := 5

	addServiceRoutes := func(svc string) {
		for j := 0; j < routesPerService; j++ {
			r := &bastion.Route{
				ID:          fmt.Sprintf("%s-%d", svc, j),
				Path:        fmt.Sprintf("/%s/path%d", svc, j),
				ServiceName: svc,
				Priority:    (j + 1) * 10,
				Enabled:     true,
				Source:      bastion.SourceFARP,
				Protocol:    bastion.ProtocolHTTP,
				Targets:     []*bastion.Target{{ID: fmt.Sprintf("t-%s-%d", svc, j), URL: fmt.Sprintf("http://%s:%d", svc, 8080+j)}},
			}
			_ = rm.AddRoute(r)
		}
	}

	// Seed initial routes.
	for _, svc := range services {
		addServiceRoutes(svc)
	}

	var wg sync.WaitGroup

	// Concurrent readers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				for _, svc := range services {
					route := rm.MatchRoute(fmt.Sprintf("/%s/path0", svc), "GET")
					if route != nil && route.ID == "" {
						t.Error("MatchRoute returned route with empty ID")
					}
				}
			}
		}()
	}

	// Concurrent remove+re-add cycles.
	for _, svc := range services {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				rm.RemoveByServiceName(s)
				addServiceRoutes(s)
			}
		}(svc)
	}

	wg.Wait()

	// If we got here without panics, the test passes.
	// Final state should have routes for all services.
	if rm.RouteCount() == 0 {
		t.Error("expected routes to exist after concurrent operations")
	}
}

// TestRouteTableConsistency_AddUpdateRemoveCycle simulates the exact FARP
// discovery lifecycle and verifies the route table is consistent at each step.
func TestRouteTableConsistency_AddUpdateRemoveCycle(t *testing.T) {
	rm := NewManager()
	svc := "svc-alpha"

	routes := []*bastion.Route{
		{ID: "farp-svc-alpha-http", Path: "/svc-alpha/*", ServiceName: svc, Priority: 20, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t1", URL: "http://10.0.0.1:8080", Healthy: true}}},
		{ID: "farp-svc-alpha-ws", Path: "/svc-alpha/ws/*", ServiceName: svc, Priority: 20, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolWebSocket, Targets: []*bastion.Target{{ID: "t1", URL: "http://10.0.0.1:8080", Healthy: true}}},
		{ID: "farp-svc-alpha-graphql", Path: "/svc-alpha/graphql", Methods: []string{"GET", "POST"}, ServiceName: svc, Priority: 20, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolGraphQL, Targets: []*bastion.Target{{ID: "t1", URL: "http://10.0.0.1:8080", Healthy: true}}},
	}

	// Step 1: Add routes.
	for _, r := range routes {
		if err := rm.AddRoute(r); err != nil {
			t.Fatalf("AddRoute(%s): %v", r.ID, err)
		}
	}

	if rm.RouteCount() != 3 {
		t.Fatalf("after add: expected 3 routes, got %d", rm.RouteCount())
	}

	for _, r := range routes {
		if _, ok := rm.GetRoute(r.ID); !ok {
			t.Errorf("after add: route %s not found", r.ID)
		}
	}

	if r := rm.MatchRoute("/svc-alpha/foo", "GET"); r == nil {
		t.Error("after add: MatchRoute(/svc-alpha/foo) returned nil")
	}

	// Step 2: Update targets (simulating instance IP change).
	newTargets := []*bastion.Target{
		{ID: "t2", URL: "http://10.0.0.2:8080", Healthy: true},
		{ID: "t3", URL: "http://10.0.0.3:8080", Healthy: true},
	}

	for _, r := range routes {
		existing, ok := rm.GetRoute(r.ID)
		if !ok {
			t.Fatalf("route %s not found for update", r.ID)
		}
		existing.Targets = newTargets
		if err := rm.UpdateRoute(existing); err != nil {
			t.Fatalf("UpdateRoute(%s): %v", r.ID, err)
		}
	}

	if rm.RouteCount() != 3 {
		t.Fatalf("after update: expected 3 routes, got %d", rm.RouteCount())
	}

	// Step 3: Remove by service name.
	rm.RemoveByServiceName(svc)

	if rm.RouteCount() != 0 {
		t.Fatalf("after remove: expected 0 routes, got %d", rm.RouteCount())
	}

	for _, r := range routes {
		if _, ok := rm.GetRoute(r.ID); ok {
			t.Errorf("after remove: route %s still exists", r.ID)
		}
	}

	if r := rm.MatchRoute("/svc-alpha/foo", "GET"); r != nil {
		t.Errorf("after remove: MatchRoute returned %s, want nil", r.ID)
	}

	// Step 4: Re-add (service reappears).
	for _, r := range routes {
		r.Targets = []*bastion.Target{{ID: "t4", URL: "http://10.0.0.4:8080", Healthy: true}}
		if err := rm.AddRoute(r); err != nil {
			t.Fatalf("re-add AddRoute(%s): %v", r.ID, err)
		}
	}

	if rm.RouteCount() != 3 {
		t.Fatalf("after re-add: expected 3 routes, got %d", rm.RouteCount())
	}

	if r := rm.MatchRoute("/svc-alpha/foo", "GET"); r == nil {
		t.Error("after re-add: MatchRoute(/svc-alpha/foo) returned nil")
	}
}

// TestMatchRoute_DeterministicUnderLoad verifies that with a fixed route
// table, MatchRoute always returns the same route for the same path/method,
// even under heavy concurrent load.
func TestMatchRoute_DeterministicUnderLoad(t *testing.T) {
	rm := NewManager()

	routes := []*bastion.Route{
		{ID: "special", Path: "/api/users/special", Priority: 35, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t1", URL: "http://s:1"}}},
		{ID: "exact", Path: "/api/users", Methods: []string{"GET"}, Priority: 30, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t2", URL: "http://e:1"}}},
		{ID: "param", Path: "/api/users/:id", Priority: 25, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t3", URL: "http://p:1"}}},
		{ID: "wildcard", Path: "/api/*", Priority: 10, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t4", URL: "http://w:1"}}},
		{ID: "other", Path: "/other/*", Priority: 5, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t5", URL: "http://o:1"}}},
	}

	for _, r := range routes {
		if err := rm.AddRoute(r); err != nil {
			t.Fatalf("AddRoute(%s): %v", r.ID, err)
		}
	}

	cases := []struct {
		path     string
		method   string
		expected string
	}{
		{"/api/users/special", "GET", "special"},
		{"/api/users", "GET", "exact"},
		{"/api/users/123", "GET", "param"},
		{"/api/products", "GET", "wildcard"},
		{"/other/stuff", "GET", "other"},
	}

	var failures atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				for _, tc := range cases {
					r := rm.MatchRoute(tc.path, tc.method)
					if r == nil {
						failures.Add(1)
						continue
					}
					if r.ID != tc.expected {
						failures.Add(1)
					}
				}
			}
		}()
	}

	wg.Wait()

	if f := failures.Load(); f > 0 {
		t.Errorf("got %d non-deterministic match results out of %d attempts", f, 50*100*len(cases))
	}
}

// TestGetRoute_ReturnedPointerMutation_DoesNotAffectLiveTable is a
// characterization test documenting the shallow-clone behavior. Mutating a
// route obtained via GetRoute currently bleeds into the live table because
// clone() copies pointers, not structs.
//
// When the shallow-clone bug is fixed (deep copy in clone or GetRoute),
// flip the assertion to expect the original URL.
func TestGetRoute_ReturnedPointerMutation_DoesNotAffectLiveTable(t *testing.T) {
	rm := NewManager()

	route := &bastion.Route{
		ID:       "test-shallow",
		Path:     "/shallow",
		Priority: 10,
		Enabled:  true,
		Source:   bastion.SourceFARP,
		Protocol: bastion.ProtocolHTTP,
		Targets: []*bastion.Target{
			{ID: "t1", URL: "http://original:8080", Healthy: true},
		},
	}

	if err := rm.AddRoute(route); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}

	// Get the route and mutate its target URL.
	got, ok := rm.GetRoute("test-shallow")
	if !ok {
		t.Fatal("route not found")
	}

	got.Targets[0].URL = "http://MUTATED:9999"

	// Re-fetch and check.
	got2, _ := rm.GetRoute("test-shallow")

	// CURRENT BEHAVIOR (shallow clone): mutation is visible.
	// When the bug is fixed, change this to expect "http://original:8080".
	if got2.Targets[0].URL != "http://MUTATED:9999" {
		t.Errorf("expected mutation to be visible (shallow clone behavior), got URL=%s", got2.Targets[0].URL)
	}
}

// TestRemoveByServiceName_DoesNotAffectOtherServices verifies that removing
// one service's routes leaves other services entirely unaffected.
func TestRemoveByServiceName_DoesNotAffectOtherServices(t *testing.T) {
	rm := NewManager()

	serviceRoutes := map[string][]*bastion.Route{
		"service-a": {
			{ID: "a-1", Path: "/a/one", ServiceName: "service-a", Priority: 20, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "ta1", URL: "http://a:1", Healthy: true}}},
			{ID: "a-2", Path: "/a/two", ServiceName: "service-a", Priority: 15, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "ta2", URL: "http://a:2", Healthy: true}}},
		},
		"service-b": {
			{ID: "b-1", Path: "/b/one", ServiceName: "service-b", Priority: 25, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "tb1", URL: "http://b:1", Healthy: true}}},
			{ID: "b-2", Path: "/b/two", ServiceName: "service-b", Priority: 10, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "tb2", URL: "http://b:2", Healthy: true}}},
		},
		"service-c": {
			{ID: "c-1", Path: "/c/one", ServiceName: "service-c", Priority: 30, Enabled: true, Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "tc1", URL: "http://c:1", Healthy: true}}},
		},
	}

	for _, routes := range serviceRoutes {
		for _, r := range routes {
			if err := rm.AddRoute(r); err != nil {
				t.Fatalf("AddRoute(%s): %v", r.ID, err)
			}
		}
	}

	if rm.RouteCount() != 5 {
		t.Fatalf("expected 5 routes, got %d", rm.RouteCount())
	}

	// Remove service-b.
	rm.RemoveByServiceName("service-b")

	if rm.RouteCount() != 3 {
		t.Fatalf("expected 3 routes after removal, got %d", rm.RouteCount())
	}

	// service-a routes intact.
	for _, r := range serviceRoutes["service-a"] {
		if _, ok := rm.GetRoute(r.ID); !ok {
			t.Errorf("service-a route %s missing after removing service-b", r.ID)
		}
		if matched := rm.MatchRoute(r.Path, "GET"); matched == nil || matched.ID != r.ID {
			t.Errorf("MatchRoute(%s) failed for service-a route", r.Path)
		}
	}

	// service-c route intact.
	if _, ok := rm.GetRoute("c-1"); !ok {
		t.Error("service-c route missing after removing service-b")
	}
	if matched := rm.MatchRoute("/c/one", "GET"); matched == nil || matched.ID != "c-1" {
		t.Error("MatchRoute(/c/one) failed for service-c route")
	}

	// service-b routes gone.
	for _, r := range serviceRoutes["service-b"] {
		if _, ok := rm.GetRoute(r.ID); ok {
			t.Errorf("service-b route %s still exists after removal", r.ID)
		}
	}
}
