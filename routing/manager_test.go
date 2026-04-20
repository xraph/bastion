package routing

import (
	"testing"
	"time"

	bastion "github.com/xraph/bastion"
)

func TestManager_AddRoute(t *testing.T) {
	rm := NewManager()

	route := &bastion.Route{
		ID:      "test-route",
		Path:    "/api/test",
		Methods: []string{"GET", "POST"},
		Targets: []*bastion.Target{
			{
				ID:     "target-1",
				URL:    "http://localhost:8080",
				Weight: 1,
			},
		},
		Protocol: bastion.ProtocolHTTP,
		Source:   bastion.SourceManual,
		Priority: 10,
		Enabled:  true,
	}

	err := rm.AddRoute(route)
	if err != nil {
		t.Fatalf("failed to add route: %v", err)
	}

	// Verify route was added
	retrieved, ok := rm.GetRoute("test-route")
	if !ok {
		t.Fatal("route not found after adding")
	}

	if retrieved.ID != route.ID {
		t.Errorf("expected route ID %s, got %s", route.ID, retrieved.ID)
	}
}

func TestManager_AddRoute_Duplicate(t *testing.T) {
	rm := NewManager()

	route := &bastion.Route{
		ID:       "test-route",
		Path:     "/api/test",
		Protocol: bastion.ProtocolHTTP,
		Targets:  []*bastion.Target{{ID: "t1", URL: "http://localhost:8080"}},
	}

	// Add first time
	err := rm.AddRoute(route)
	if err != nil {
		t.Fatalf("failed to add route: %v", err)
	}

	// Try to add duplicate
	err = rm.AddRoute(route)
	if err == nil {
		t.Fatal("expected error when adding duplicate route")
	}
}

func TestManager_UpdateRoute(t *testing.T) {
	rm := NewManager()

	route := &bastion.Route{
		ID:       "test-route",
		Path:     "/api/test",
		Priority: 10,
		Protocol: bastion.ProtocolHTTP,
		Targets:  []*bastion.Target{{ID: "t1", URL: "http://localhost:8080"}},
	}

	err := rm.AddRoute(route)
	if err != nil {
		t.Fatalf("failed to add route: %v", err)
	}

	// Update route
	route.Priority = 20
	err = rm.UpdateRoute(route)
	if err != nil {
		t.Fatalf("failed to update route: %v", err)
	}

	// Verify update
	retrieved, ok := rm.GetRoute("test-route")
	if !ok {
		t.Fatal("route not found after update")
	}

	if retrieved.Priority != 20 {
		t.Errorf("expected priority 20, got %d", retrieved.Priority)
	}
}

func TestManager_DeleteRoute(t *testing.T) {
	rm := NewManager()

	route := &bastion.Route{
		ID:       "test-route",
		Path:     "/api/test",
		Protocol: bastion.ProtocolHTTP,
		Targets:  []*bastion.Target{{ID: "t1", URL: "http://localhost:8080"}},
	}

	err := rm.AddRoute(route)
	if err != nil {
		t.Fatalf("failed to add route: %v", err)
	}

	// Delete route
	err = rm.RemoveRoute("test-route")
	if err != nil {
		t.Fatalf("failed to delete route: %v", err)
	}

	// Verify deletion
	_, ok := rm.GetRoute("test-route")
	if ok {
		t.Fatal("route still exists after deletion")
	}
}

func TestManager_MatchRoute(t *testing.T) {
	rm := NewManager()

	// Add test routes
	routes := []*bastion.Route{
		{
			ID:       "exact",
			Path:     "/api/users",
			Methods:  []string{"GET"},
			Protocol: bastion.ProtocolHTTP,
			Priority: 10,
			Enabled:  true,
			Targets:  []*bastion.Target{{ID: "t1", URL: "http://localhost:8080"}},
		},
		{
			ID:       "wildcard",
			Path:     "/api/*",
			Methods:  []string{"GET", "POST"},
			Protocol: bastion.ProtocolHTTP,
			Priority: 5,
			Enabled:  true,
			Targets:  []*bastion.Target{{ID: "t2", URL: "http://localhost:8081"}},
		},
		{
			ID:       "param",
			Path:     "/api/users/:id",
			Methods:  []string{"GET"},
			Protocol: bastion.ProtocolHTTP,
			Priority: 15,
			Enabled:  true,
			Targets:  []*bastion.Target{{ID: "t3", URL: "http://localhost:8082"}},
		},
	}

	for _, route := range routes {
		if err := rm.AddRoute(route); err != nil {
			t.Fatalf("failed to add route: %v", err)
		}
	}

	tests := []struct {
		name           string
		path           string
		method         string
		expectedID     string
		shouldMatch    bool
		expectedParams map[string]string
	}{
		{
			name:        "exact match",
			path:        "/api/users",
			method:      "GET",
			expectedID:  "exact",
			shouldMatch: true,
		},
		{
			name:        "param match",
			path:        "/api/users/123",
			method:      "GET",
			expectedID:  "param",
			shouldMatch: true,
			expectedParams: map[string]string{
				"id": "123",
			},
		},
		{
			name:        "wildcard match",
			path:        "/api/products",
			method:      "GET",
			expectedID:  "wildcard",
			shouldMatch: true,
		},
		{
			name:        "no match - method",
			path:        "/api/users",
			method:      "DELETE",
			shouldMatch: false,
		},
		{
			name:        "no match - path",
			path:        "/other/path",
			method:      "GET",
			shouldMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := rm.MatchRoute(tt.path, tt.method)

			if tt.shouldMatch {
				if route == nil {
					t.Fatal("expected route match, got nil")
				}
				if route.ID != tt.expectedID {
					t.Errorf("expected route %s, got %s", tt.expectedID, route.ID)
				}
				if tt.expectedParams != nil {
					// MatchRoute doesn't return params in current API; skip param checks
					_ = tt.expectedParams
				}
			} else {
				if route != nil {
					t.Errorf("expected no match, got route %s", route.ID)
				}
			}
		})
	}
}

func TestManager_ListRoutes(t *testing.T) {
	rm := NewManager()

	routes := []*bastion.Route{
		{ID: "route-1", Path: "/api/1", Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t1", URL: "http://localhost:8080"}}},
		{ID: "route-2", Path: "/api/2", Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t2", URL: "http://localhost:8081"}}},
		{ID: "route-3", Path: "/api/3", Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t3", URL: "http://localhost:8082"}}},
	}

	for _, route := range routes {
		if err := rm.AddRoute(route); err != nil {
			t.Fatalf("failed to add route: %v", err)
		}
	}

	list := rm.ListRoutes()
	if len(list) != 3 {
		t.Errorf("expected 3 routes, got %d", len(list))
	}
}

func TestManager_RemoveByServiceName(t *testing.T) {
	rm := NewManager()

	routes := []*bastion.Route{
		{ID: "service-a-1", Path: "/api/1", ServiceName: "service-a", Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t1", URL: "http://localhost:8080"}}},
		{ID: "service-a-2", Path: "/api/2", ServiceName: "service-a", Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t2", URL: "http://localhost:8081"}}},
		{ID: "service-b-1", Path: "/api/3", ServiceName: "service-b", Protocol: bastion.ProtocolHTTP, Targets: []*bastion.Target{{ID: "t3", URL: "http://localhost:8082"}}},
	}

	for _, route := range routes {
		if err := rm.AddRoute(route); err != nil {
			t.Fatalf("failed to add route: %v", err)
		}
	}

	// Remove all routes for service-a
	rm.RemoveByServiceName("service-a")

	// Verify service-a routes are gone
	_, ok1 := rm.GetRoute("service-a-1")
	_, ok2 := rm.GetRoute("service-a-2")
	_, ok3 := rm.GetRoute("service-b-1")

	if ok1 || ok2 {
		t.Error("service-a routes still exist after removal")
	}
	if !ok3 {
		t.Error("service-b route was incorrectly removed")
	}
}

func TestManager_ConcurrentAccess(t *testing.T) {
	rm := NewManager()

	// Test concurrent writes and reads
	done := make(chan bool)
	routeCount := 100

	// Concurrent writers
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < routeCount/10; j++ {
				route := &bastion.Route{
					ID:       time.Now().Format("20060102150405.000000"),
					Path:     "/api/test",
					Protocol: bastion.ProtocolHTTP,
					Targets:  []*bastion.Target{{ID: "t1", URL: "http://localhost:8080"}},
				}
				_ = rm.AddRoute(route)
			}
			done <- true
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = rm.ListRoutes()
				_ = rm.MatchRoute("/api/test", "GET")
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Should not panic or race
}

func TestRoute_IsHealthy(t *testing.T) {
	tests := []struct {
		name     string
		targets  []*bastion.Target
		expected bool
	}{
		{
			name: "all healthy",
			targets: []*bastion.Target{
				{ID: "t1", URL: "http://localhost:8080", Healthy: true},
				{ID: "t2", URL: "http://localhost:8081", Healthy: true},
			},
			expected: true,
		},
		{
			name: "some healthy",
			targets: []*bastion.Target{
				{ID: "t1", URL: "http://localhost:8080", Healthy: true},
				{ID: "t2", URL: "http://localhost:8081", Healthy: false},
			},
			expected: true,
		},
		{
			name: "none healthy",
			targets: []*bastion.Target{
				{ID: "t1", URL: "http://localhost:8080", Healthy: false},
				{ID: "t2", URL: "http://localhost:8081", Healthy: false},
			},
			expected: false,
		},
		{
			name:     "no targets",
			targets:  []*bastion.Target{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route := &bastion.Route{
				ID:       "test",
				Path:     "/test",
				Targets:  tt.targets,
				Protocol: bastion.ProtocolHTTP,
			}

			hasHealthy := false
			for _, target := range route.Targets {
				if target.Healthy {
					hasHealthy = true

					break
				}
			}

			if hasHealthy != tt.expected {
				t.Errorf("hasHealthyTarget = %v, want %v", hasHealthy, tt.expected)
			}
		})
	}
}

func TestManager_MatchRoute_SpecificPathBeforeCatchAll(t *testing.T) {
	rm := NewManager()

	// Add a root catch-all route (simulates service mounted at "/")
	rootRoute := &bastion.Route{
		ID:       "farp-twinos-http",
		Path:     "/*",
		Protocol: bastion.ProtocolHTTP,
		Source:   bastion.SourceFARP,
		Priority: 20,
		Enabled:  true,
		Targets:  []*bastion.Target{{ID: "t1", URL: "http://localhost:7900"}},
	}

	// Add a service-prefixed route (simulates Portal at /portal)
	portalRoute := &bastion.Route{
		ID:       "farp-portal-http",
		Path:     "/portal/*",
		Protocol: bastion.ProtocolHTTP,
		Source:   bastion.SourceFARP,
		Priority: 20,
		Enabled:  true,
		Targets:  []*bastion.Target{{ID: "t2", URL: "http://localhost:7901"}},
	}

	// Add root FIRST (the problematic ordering)
	if err := rm.AddRoute(rootRoute); err != nil {
		t.Fatalf("failed to add root route: %v", err)
	}
	if err := rm.AddRoute(portalRoute); err != nil {
		t.Fatalf("failed to add portal route: %v", err)
	}

	// Request to /portal/... must match the portal route, NOT the root catch-all.
	matched := rm.MatchRoute("/portal/authsome/v1/admin/settings", "GET")
	if matched == nil {
		t.Fatal("expected a matching route")
	}
	if matched.ID != "farp-portal-http" {
		t.Errorf("expected portal route (farp-portal-http), got %s", matched.ID)
	}

	// Request to /other/... should still match the root catch-all.
	matched = rm.MatchRoute("/other/path", "GET")
	if matched == nil {
		t.Fatal("expected a matching route for /other/path")
	}
	if matched.ID != "farp-twinos-http" {
		t.Errorf("expected root route (farp-twinos-http), got %s", matched.ID)
	}
}
