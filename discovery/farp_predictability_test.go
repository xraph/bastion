package discovery

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// opRecord records a single route registry operation for ordering verification.
type opRecord struct {
	op      string // "add", "update", "remove", "removeByService"
	routeID string
}

// trackingRouteRegistry is a thread-safe mock RouteRegistry that tracks
// operation counts and operation order for verifying FARP discovery behavior.
type trackingRouteRegistry struct {
	mu                  sync.Mutex
	routes              []*Route
	removedServiceNames []string
	ops                 []opRecord // ordered operation log

	addCount    atomic.Int64
	updateCount atomic.Int64
	getCount    atomic.Int64
	removeCount atomic.Int64
}

func (tr *trackingRouteRegistry) AddRoute(route *Route) error {
	tr.addCount.Add(1)
	tr.mu.Lock()
	defer tr.mu.Unlock()

	// Check for duplicate ID.
	for _, r := range tr.routes {
		if r.ID == route.ID {
			return fmt.Errorf("route %q already exists", route.ID)
		}
	}

	tr.routes = append(tr.routes, route)
	tr.ops = append(tr.ops, opRecord{op: "add", routeID: route.ID})
	return nil
}

func (tr *trackingRouteRegistry) RemoveRoute(id string) error {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	for i, r := range tr.routes {
		if r.ID == id {
			tr.routes = append(tr.routes[:i], tr.routes[i+1:]...)
			tr.ops = append(tr.ops, opRecord{op: "remove", routeID: id})
			return nil
		}
	}
	return fmt.Errorf("route %q not found", id)
}

func (tr *trackingRouteRegistry) UpdateRoute(route *Route) error {
	tr.updateCount.Add(1)
	tr.mu.Lock()
	defer tr.mu.Unlock()

	for i, r := range tr.routes {
		if r.ID == route.ID {
			tr.routes[i] = route
			tr.ops = append(tr.ops, opRecord{op: "update", routeID: route.ID})
			return nil
		}
	}
	return fmt.Errorf("route %q not found", route.ID)
}

func (tr *trackingRouteRegistry) GetRoute(id string) (*Route, bool) {
	tr.getCount.Add(1)
	tr.mu.Lock()
	defer tr.mu.Unlock()

	for _, r := range tr.routes {
		if r.ID == id {
			return r, true
		}
	}
	return nil, false
}

func (tr *trackingRouteRegistry) RemoveByServiceName(serviceName string) {
	tr.removeCount.Add(1)
	tr.mu.Lock()
	defer tr.mu.Unlock()

	tr.removedServiceNames = append(tr.removedServiceNames, serviceName)

	filtered := make([]*Route, 0, len(tr.routes))
	for _, r := range tr.routes {
		if r.ServiceName != serviceName {
			filtered = append(filtered, r)
		}
	}
	tr.routes = filtered
}

func (tr *trackingRouteRegistry) ListRoutes() []*Route {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	result := make([]*Route, len(tr.routes))
	copy(result, tr.routes)
	return result
}

func (tr *trackingRouteRegistry) routeCount() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.routes)
}

func (tr *trackingRouteRegistry) routeIDs() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	ids := make([]string, len(tr.routes))
	for i, r := range tr.routes {
		ids[i] = r.ID
	}
	return ids
}

func (tr *trackingRouteRegistry) routesForService(name string) []*Route {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var result []*Route
	for _, r := range tr.routes {
		if r.ServiceName == name {
			result = append(result, r)
		}
	}
	return result
}

func (tr *trackingRouteRegistry) opsLog() []opRecord {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	result := make([]opRecord, len(tr.ops))
	copy(result, tr.ops)
	return result
}

func (tr *trackingRouteRegistry) clearOps() {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.ops = nil
}

func newFARPTestManager(rm *trackingRouteRegistry, mock *mockDiscoveryService) *Manager {
	return &Manager{
		config: DiscoveryConfig{
			Enabled:      true,
			PollInterval: 30 * time.Second,
			AutoPrefix:   true,
			StripPrefix:  true,
		},
		logger:          newTestLogger(),
		rm:              rm,
		service:         mock,
		discoveredSvcs:  make(map[string]*DiscoveredService),
		servicePrefixes: make(map[string]string),
		serviceLastSeen: make(map[string]time.Time),
		pushInstances:   make(map[string]map[string]*ServiceInstanceInfo),
	}
}

func farpInstance(name, addr string, port int) *ServiceInstanceInfo {
	return &ServiceInstanceInfo{
		ID:      fmt.Sprintf("%s-%s-%d", name, addr, port),
		Name:    name,
		Address: addr,
		Port:    port,
		Healthy: true,
		Metadata: map[string]string{
			"farp.enabled": "true",
			"farp.openapi": "/openapi.json",
		},
	}
}

// TestFARP_RouteAvailability_DuringRefreshCycle verifies that routes remain
// available across consecutive refresh cycles with no gaps.
func TestFARP_RouteAvailability_DuringRefreshCycle(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// First refresh — routes created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	count1 := rm.routeCount()
	if count1 == 0 {
		t.Fatal("expected routes after first refresh")
	}
	ids1 := rm.routeIDs()

	// Second refresh — routes should remain, not be re-created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	count2 := rm.routeCount()
	if count2 != count1 {
		t.Errorf("route count changed between refreshes: %d -> %d", count1, count2)
	}

	ids2 := rm.routeIDs()
	if len(ids1) != len(ids2) {
		t.Fatalf("route ID count changed: %d -> %d", len(ids1), len(ids2))
	}
	for i := range ids1 {
		if ids1[i] != ids2[i] {
			t.Errorf("route ID[%d] changed: %s -> %s", i, ids1[i], ids2[i])
		}
	}

	// No service removals should have occurred.
	if len(rm.removedServiceNames) > 0 {
		t.Errorf("unexpected service removals: %v", rm.removedServiceNames)
	}

	// Add should be called once (initial), update once (second refresh).
	if adds := rm.addCount.Load(); adds != 1 {
		t.Errorf("expected 1 add, got %d", adds)
	}
	if updates := rm.updateCount.Load(); updates != 1 {
		t.Errorf("expected 1 update, got %d", updates)
	}
}

// TestFARP_ServiceReappearance_NoRouteGap verifies that when a service
// disappears and reappears within the grace period, routes are maintained
// throughout — no "no matching route" window.
func TestFARP_ServiceReappearance_NoRouteGap(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)
	sd.config.RemovalGracePeriod = 500 * time.Millisecond

	// Initial refresh.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	initialCount := rm.routeCount()
	if initialCount == 0 {
		t.Fatal("expected routes after initial refresh")
	}

	// Service disappears from ListServices.
	mock.services = []string{}

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after disappearance: %v", err)
	}

	// Within grace period — routes must still exist.
	if rm.routeCount() != initialCount {
		t.Errorf("routes lost during grace period: %d -> %d", initialCount, rm.routeCount())
	}
	if len(rm.removedServiceNames) > 0 {
		t.Errorf("service removed within grace period: %v", rm.removedServiceNames)
	}

	// Service reappears.
	mock.services = []string{"my-svc"}

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after reappearance: %v", err)
	}

	if rm.routeCount() != initialCount {
		t.Errorf("route count changed after reappearance: %d", rm.routeCount())
	}
	if len(rm.removedServiceNames) > 0 {
		t.Errorf("unexpected removal after reappearance: %v", rm.removedServiceNames)
	}
}

// TestFARP_ServiceReappearance_AfterGracePeriod verifies that when a service
// disappears past the grace period, routes are removed. When it reappears,
// routes are re-created.
func TestFARP_ServiceReappearance_AfterGracePeriod(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)
	sd.config.RemovalGracePeriod = 50 * time.Millisecond

	// Initial refresh.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	initialCount := rm.routeCount()
	if initialCount == 0 {
		t.Fatal("expected routes after initial refresh")
	}

	// Service disappears.
	mock.services = []string{}
	mock.instances = map[string][]*ServiceInstanceInfo{}

	// Still within grace period.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh within grace: %v", err)
	}
	if len(rm.removedServiceNames) > 0 {
		t.Error("service removed within grace period")
	}

	// Wait past grace period.
	time.Sleep(80 * time.Millisecond)

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after grace: %v", err)
	}

	if rm.routeCount() != 0 {
		t.Errorf("expected 0 routes after grace period, got %d", rm.routeCount())
	}
	if len(rm.removedServiceNames) == 0 {
		t.Error("expected service removal after grace period")
	}

	// Service reappears.
	mock.services = []string{"my-svc"}
	mock.instances = map[string][]*ServiceInstanceInfo{
		"my-svc": {farpInstance("my-svc", "10.0.0.2", 8080)},
	}

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after reappearance: %v", err)
	}

	if rm.routeCount() == 0 {
		t.Error("expected routes after service reappearance")
	}
}

// TestFARP_MultipleServices_IndependentRouting verifies that removing one
// FARP service's routes does not affect other services.
func TestFARP_MultipleServices_IndependentRouting(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"svc-alpha", "svc-beta", "svc-gamma"},
		instances: map[string][]*ServiceInstanceInfo{
			"svc-alpha": {farpInstance("svc-alpha", "10.0.0.1", 8080)},
			"svc-beta":  {farpInstance("svc-beta", "10.0.0.2", 8080)},
			"svc-gamma": {farpInstance("svc-gamma", "10.0.0.3", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)
	sd.config.RemovalGracePeriod = 0 // immediate removal

	// Initial refresh — all 3 services create routes.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	alphaRoutes := rm.routesForService("svc-alpha")
	betaRoutes := rm.routesForService("svc-beta")
	gammaRoutes := rm.routesForService("svc-gamma")

	if len(alphaRoutes) == 0 || len(betaRoutes) == 0 || len(gammaRoutes) == 0 {
		t.Fatalf("expected routes for all services: alpha=%d, beta=%d, gamma=%d",
			len(alphaRoutes), len(betaRoutes), len(gammaRoutes))
	}

	totalBefore := rm.routeCount()

	// Remove svc-beta from discovery. Set its last-seen to the past so it
	// exceeds the 0-second grace period.
	mock.services = []string{"svc-alpha", "svc-gamma"}
	delete(mock.instances, "svc-beta")
	sd.mu.Lock()
	sd.serviceLastSeen["svc-beta"] = time.Now().Add(-time.Minute)
	sd.mu.Unlock()

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after beta removal: %v", err)
	}

	// svc-alpha and svc-gamma routes must still exist.
	alphaAfter := rm.routesForService("svc-alpha")
	gammaAfter := rm.routesForService("svc-gamma")
	betaAfter := rm.routesForService("svc-beta")

	if len(alphaAfter) != len(alphaRoutes) {
		t.Errorf("svc-alpha route count changed: %d -> %d", len(alphaRoutes), len(alphaAfter))
	}
	if len(gammaAfter) != len(gammaRoutes) {
		t.Errorf("svc-gamma route count changed: %d -> %d", len(gammaRoutes), len(gammaAfter))
	}
	if len(betaAfter) != 0 {
		t.Errorf("svc-beta routes still exist: %d", len(betaAfter))
	}

	expectedTotal := totalBefore - len(betaRoutes)
	if rm.routeCount() != expectedTotal {
		t.Errorf("total route count: got %d, want %d", rm.routeCount(), expectedTotal)
	}
}

// TestFARP_ConcurrentRefresh_NoRouteLoss verifies that concurrent Refresh
// calls do not lose routes.
func TestFARP_ConcurrentRefresh_NoRouteLoss(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	expectedCount := rm.routeCount()
	if expectedCount == 0 {
		t.Fatal("expected routes after initial refresh")
	}

	// Concurrent refreshes.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sd.Refresh(context.Background())
		}()
	}
	wg.Wait()

	// Route count should be stable.
	if rm.routeCount() != expectedCount {
		t.Errorf("route count changed after concurrent refreshes: %d -> %d",
			expectedCount, rm.routeCount())
	}

	// No removals.
	if len(rm.removedServiceNames) > 0 {
		t.Errorf("unexpected service removals: %v", rm.removedServiceNames)
	}
}

// TestFARP_TargetUpdate_RoutePathPreserved verifies that when instance
// targets change between refreshes, the route path stays the same.
func TestFARP_TargetUpdate_RoutePathPreserved(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {
				farpInstance("my-svc", "10.0.0.1", 8080),
				farpInstance("my-svc", "10.0.0.2", 8080),
			},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	// Record paths.
	initialRoutes := rm.routesForService("my-svc")
	if len(initialRoutes) == 0 {
		t.Fatal("expected routes for my-svc")
	}

	pathsByID := make(map[string]string)
	for _, r := range initialRoutes {
		pathsByID[r.ID] = r.Path
	}

	// Change instances (different IPs, different count).
	mock.instances["my-svc"] = []*ServiceInstanceInfo{
		farpInstance("my-svc", "10.0.0.3", 9090),
		farpInstance("my-svc", "10.0.0.4", 9090),
		farpInstance("my-svc", "10.0.0.5", 9090),
	}

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after target change: %v", err)
	}

	// Paths must be identical.
	updatedRoutes := rm.routesForService("my-svc")
	for _, r := range updatedRoutes {
		origPath, ok := pathsByID[r.ID]
		if !ok {
			t.Errorf("unexpected route ID %s after target update", r.ID)
			continue
		}
		if r.Path != origPath {
			t.Errorf("route %s path changed: %s -> %s", r.ID, origPath, r.Path)
		}
	}

	// Targets should reflect the new instances.
	for _, r := range updatedRoutes {
		if len(r.Targets) != 3 {
			t.Errorf("route %s: expected 3 targets, got %d", r.ID, len(r.Targets))
		}
	}
}

// TestFARP_ProcessService_Idempotent verifies that calling processService
// multiple times with the same data produces the same state: 1 add followed
// by N-1 updates.
func TestFARP_ProcessService_Idempotent(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{}

	sd := newFARPTestManager(rm, mock)

	instances := []*ServiceInstanceInfo{
		farpInstance("my-svc", "10.0.0.1", 8080),
	}

	// Call processService 5 times.
	for i := 0; i < 5; i++ {
		sd.processService("my-svc", instances)
	}

	// Should have 1 add (first call) and 4 updates (subsequent calls).
	if adds := rm.addCount.Load(); adds != 1 {
		t.Errorf("expected 1 add, got %d", adds)
	}
	if updates := rm.updateCount.Load(); updates != 4 {
		t.Errorf("expected 4 updates, got %d", updates)
	}

	// Route count should be 1 (the FARP HTTP route).
	routes := rm.routesForService("my-svc")
	if len(routes) != 1 {
		t.Errorf("expected 1 route, got %d", len(routes))
	}
}

// TestFARP_DeregisterDuringRefresh verifies that concurrent DeregisterService
// and Refresh calls don't panic and produce a consistent final state.
func TestFARP_DeregisterDuringRefresh(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	if rm.routeCount() == 0 {
		t.Fatal("expected routes after initial refresh")
	}

	// Concurrent refresh + deregister.
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = sd.Refresh(context.Background())
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		sd.DeregisterService(context.Background(), "my-svc")
	}()

	wg.Wait()

	// No panics = success. Final state should be consistent:
	// either routes exist (refresh re-created them last) or they don't
	// (deregister ran last).
	routes := rm.routesForService("my-svc")

	// If routes exist, they must have valid targets.
	for _, r := range routes {
		if len(r.Targets) == 0 {
			t.Errorf("route %s has no targets", r.ID)
		}
	}

	// If routes don't exist, deregister must have been called.
	if len(routes) == 0 {
		found := false
		for _, name := range rm.removedServiceNames {
			if name == "my-svc" {
				found = true
				break
			}
		}
		if !found {
			t.Error("routes are empty but no deregister was recorded")
		}
	}
}

// TestFARP_ProcessService_PathUpdated_WhenRouteExists verifies that when a
// FARP prefix changes (e.g., via PrefixOverrides or manifest strategy change),
// processService propagates the new path to existing routes so requests to the
// new path are correctly matched.
func TestFARP_ProcessService_PathUpdated_WhenRouteExists(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh — route created with path /my-svc/*.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	routes := rm.routesForService("my-svc")
	if len(routes) == 0 {
		t.Fatal("expected routes for my-svc")
	}

	initialPath := routes[0].Path
	if initialPath != "/my-svc/*" {
		t.Fatalf("expected initial path /my-svc/*, got %s", initialPath)
	}

	// Change the prefix via PrefixOverrides (simulating a FARP strategy change).
	sd.config.PrefixOverrides = map[string]string{
		"my-svc": "/api/v2",
	}

	// Refresh — processService should update the route's path to /api/v2/*.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after prefix change: %v", err)
	}

	routes = rm.routesForService("my-svc")
	if len(routes) == 0 {
		t.Fatal("expected routes after second refresh")
	}

	if routes[0].Path != "/api/v2/*" {
		t.Errorf("expected path to be updated to /api/v2/*, got %s", routes[0].Path)
	}
}

// TestFARP_ServiceRestart_NewEndpointsReflected verifies that when a service
// restarts and adds new protocol endpoints (e.g., adds GraphQL), the new routes
// appear without requiring a gateway restart.
func TestFARP_ServiceRestart_NewEndpointsReflected(t *testing.T) {
	rm := &trackingRouteRegistry{}

	// Service initially has only OpenAPI endpoint.
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {{
				ID:      "inst-1",
				Name:    "my-svc",
				Address: "10.0.0.1",
				Port:    8080,
				Healthy: true,
				Metadata: map[string]string{
					"farp.enabled": "true",
					"farp.openapi": "/openapi.json",
				},
			}},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh — only HTTP route created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	initialRoutes := rm.routesForService("my-svc")
	if len(initialRoutes) != 1 {
		t.Fatalf("expected 1 initial route, got %d", len(initialRoutes))
	}
	if initialRoutes[0].ID != "farp-my-svc-http" {
		t.Fatalf("expected farp-my-svc-http, got %s", initialRoutes[0].ID)
	}

	// Service restarts with additional endpoints (GraphQL and WebSocket).
	mock.instances["my-svc"] = []*ServiceInstanceInfo{{
		ID:      "inst-1",
		Name:    "my-svc",
		Address: "10.0.0.1",
		Port:    8080,
		Healthy: true,
		Metadata: map[string]string{
			"farp.enabled":  "true",
			"farp.openapi":  "/openapi.json",
			"farp.graphql":  "/graphql",
			"farp.asyncapi": "/asyncapi.yaml",
		},
	}}

	// Refresh — new routes should appear.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after restart: %v", err)
	}

	updatedRoutes := rm.routesForService("my-svc")
	if len(updatedRoutes) != 3 {
		t.Errorf("expected 3 routes after restart (http + graphql + ws), got %d", len(updatedRoutes))
		for _, r := range updatedRoutes {
			t.Logf("  route: %s path=%s protocol=%s", r.ID, r.Path, r.Protocol)
		}
	}

	// Verify each expected route exists.
	routeByID := make(map[string]*Route)
	for _, r := range updatedRoutes {
		routeByID[r.ID] = r
	}

	for _, expectedID := range []string{"farp-my-svc-http", "farp-my-svc-graphql", "farp-my-svc-ws"} {
		if _, ok := routeByID[expectedID]; !ok {
			t.Errorf("expected route %s not found after restart", expectedID)
		}
	}
}

// TestFARP_ServiceRestart_RemovedEndpointsCleaned verifies that when a service
// restarts and drops a protocol endpoint (e.g., removes GraphQL), the stale
// route is cleaned up without requiring a gateway restart.
func TestFARP_ServiceRestart_RemovedEndpointsCleaned(t *testing.T) {
	rm := &trackingRouteRegistry{}

	// Service initially has OpenAPI + GraphQL + WebSocket.
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {{
				ID:      "inst-1",
				Name:    "my-svc",
				Address: "10.0.0.1",
				Port:    8080,
				Healthy: true,
				Metadata: map[string]string{
					"farp.enabled":  "true",
					"farp.openapi":  "/openapi.json",
					"farp.graphql":  "/graphql",
					"farp.asyncapi": "/asyncapi.yaml",
				},
			}},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh — 3 routes created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	if rm.routeCount() != 3 {
		t.Fatalf("expected 3 initial routes, got %d", rm.routeCount())
	}

	// Service restarts with only OpenAPI (dropped GraphQL and WebSocket).
	mock.instances["my-svc"] = []*ServiceInstanceInfo{{
		ID:      "inst-1",
		Name:    "my-svc",
		Address: "10.0.0.1",
		Port:    8080,
		Healthy: true,
		Metadata: map[string]string{
			"farp.enabled": "true",
			"farp.openapi": "/openapi.json",
		},
	}}

	// Refresh — stale routes should be removed.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after restart: %v", err)
	}

	updatedRoutes := rm.routesForService("my-svc")
	if len(updatedRoutes) != 1 {
		t.Errorf("expected 1 route after restart (only http), got %d", len(updatedRoutes))
		for _, r := range updatedRoutes {
			t.Logf("  route: %s path=%s protocol=%s", r.ID, r.Path, r.Protocol)
		}
	}

	if len(updatedRoutes) > 0 && updatedRoutes[0].ID != "farp-my-svc-http" {
		t.Errorf("expected remaining route to be farp-my-svc-http, got %s", updatedRoutes[0].ID)
	}
}

// TestFARP_ServiceRestart_ProtocolSwap verifies that when a service swaps
// its protocols entirely (e.g., from HTTP+WS to gRPC), the old routes are
// removed and new ones are created.
func TestFARP_ServiceRestart_ProtocolSwap(t *testing.T) {
	rm := &trackingRouteRegistry{}

	// Service initially serves HTTP + WebSocket.
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {{
				ID:      "inst-1",
				Name:    "my-svc",
				Address: "10.0.0.1",
				Port:    8080,
				Healthy: true,
				Metadata: map[string]string{
					"farp.enabled":  "true",
					"farp.openapi":  "/openapi.json",
					"farp.asyncapi": "/asyncapi.yaml",
				},
			}},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh — HTTP + WS routes.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	if rm.routeCount() != 2 {
		t.Fatalf("expected 2 initial routes, got %d", rm.routeCount())
	}

	// Verify initial routes.
	initialIDs := make(map[string]bool)
	for _, r := range rm.routesForService("my-svc") {
		initialIDs[r.ID] = true
	}
	if !initialIDs["farp-my-svc-http"] || !initialIDs["farp-my-svc-ws"] {
		t.Fatalf("expected farp-my-svc-http and farp-my-svc-ws, got %v", initialIDs)
	}

	// Service restarts with only GraphQL (dropped HTTP and WS).
	mock.instances["my-svc"] = []*ServiceInstanceInfo{{
		ID:      "inst-1",
		Name:    "my-svc",
		Address: "10.0.0.1",
		Port:    8080,
		Healthy: true,
		Metadata: map[string]string{
			"farp.enabled": "true",
			"farp.graphql": "/graphql",
		},
	}}

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after protocol swap: %v", err)
	}

	updatedRoutes := rm.routesForService("my-svc")
	if len(updatedRoutes) != 1 {
		t.Errorf("expected 1 route after swap, got %d", len(updatedRoutes))
		for _, r := range updatedRoutes {
			t.Logf("  route: %s path=%s protocol=%s", r.ID, r.Path, r.Protocol)
		}
	}

	if len(updatedRoutes) > 0 {
		if updatedRoutes[0].ID != "farp-my-svc-graphql" {
			t.Errorf("expected farp-my-svc-graphql, got %s", updatedRoutes[0].ID)
		}
		if updatedRoutes[0].Protocol != ProtocolGraphQL {
			t.Errorf("expected protocol graphql, got %s", updatedRoutes[0].Protocol)
		}
	}
}

// TestFARP_ServiceRestart_AllFieldsPropagated verifies that when a service
// restarts and its FARP metadata changes (methods, stripPrefix, priority),
// all fields are propagated to the existing route.
func TestFARP_ServiceRestart_AllFieldsPropagated(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	routes := rm.routesForService("my-svc")
	if len(routes) == 0 {
		t.Fatal("expected routes")
	}

	origRoute := routes[0]
	origPath := origRoute.Path
	origStripPrefix := origRoute.StripPrefix

	// Change prefix and strip-prefix config.
	sd.config.PrefixOverrides = map[string]string{"my-svc": "/v2/api"}
	sd.config.StripPrefix = !origStripPrefix

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after config change: %v", err)
	}

	routes = rm.routesForService("my-svc")
	if len(routes) == 0 {
		t.Fatal("expected routes after refresh")
	}

	updated := routes[0]

	if updated.Path == origPath {
		t.Errorf("path should have changed from %s", origPath)
	}
	if updated.Path != "/v2/api/*" {
		t.Errorf("expected path /v2/api/*, got %s", updated.Path)
	}
	if updated.StripPrefix == origStripPrefix {
		t.Errorf("stripPrefix should have changed from %v", origStripPrefix)
	}
}

// TestFARP_NoRouteGap_DuringProcessService verifies that at no point during
// processService does a service lose all its routes. New routes must be
// added/updated BEFORE stale routes are removed, so there is always at least
// one matchable route while the route set is being transitioned.
func TestFARP_NoRouteGap_DuringProcessService(t *testing.T) {
	rm := &trackingRouteRegistry{}

	// Service initially has HTTP + WebSocket.
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {{
				ID:      "inst-1",
				Name:    "my-svc",
				Address: "10.0.0.1",
				Port:    8080,
				Healthy: true,
				Metadata: map[string]string{
					"farp.enabled":  "true",
					"farp.openapi":  "/openapi.json",
					"farp.asyncapi": "/asyncapi.yaml",
				},
			}},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh — HTTP + WS routes created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	if rm.routeCount() != 2 {
		t.Fatalf("expected 2 initial routes, got %d", rm.routeCount())
	}

	// Service restarts with only GraphQL (drops HTTP + WS, adds GraphQL).
	// This is the worst case: all old route IDs are stale, all new IDs are new.
	mock.instances["my-svc"] = []*ServiceInstanceInfo{{
		ID:      "inst-1",
		Name:    "my-svc",
		Address: "10.0.0.1",
		Port:    8080,
		Healthy: true,
		Metadata: map[string]string{
			"farp.enabled": "true",
			"farp.graphql": "/graphql",
		},
	}}

	rm.clearOps()

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after restart: %v", err)
	}

	// Verify operation ordering: all adds/updates must come BEFORE any removes.
	ops := rm.opsLog()
	sawRemove := false
	for _, op := range ops {
		if op.op == "remove" {
			sawRemove = true
		}
		if sawRemove && (op.op == "add" || op.op == "update") {
			t.Errorf("add/update after remove — route gap possible: %s(%s) after remove",
				op.op, op.routeID)
		}
	}

	// The add of the new GraphQL route should appear before the removal
	// of the old HTTP and WS routes.
	if len(ops) < 3 {
		t.Fatalf("expected at least 3 ops (1 add + 2 removes), got %d: %v", len(ops), ops)
	}

	if ops[0].op != "add" || ops[0].routeID != "farp-my-svc-graphql" {
		t.Errorf("first op should be add(farp-my-svc-graphql), got %s(%s)", ops[0].op, ops[0].routeID)
	}
}

// TestFARP_NoRouteGap_FARPFallbackFlap verifies that when a FARP manifest
// fetch fails intermittently, the fallback catch-all route (discovery-*) is
// added before the old FARP routes (farp-*) are removed, preventing a gap.
func TestFARP_NoRouteGap_FARPFallbackFlap(t *testing.T) {
	rm := &trackingRouteRegistry{}

	// Service with full FARP metadata (will produce farp-* route IDs).
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh — FARP route created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	routes := rm.routesForService("my-svc")
	if len(routes) == 0 {
		t.Fatal("expected routes after initial refresh")
	}

	initialID := routes[0].ID
	if initialID != "farp-my-svc-http" {
		t.Fatalf("expected farp-my-svc-http, got %s", initialID)
	}

	// Simulate FARP metadata disappearing (service restarts without
	// farp.enabled or farp.openapi — falls back to discovery catch-all).
	mock.instances["my-svc"] = []*ServiceInstanceInfo{{
		ID:       "inst-1",
		Name:     "my-svc",
		Address:  "10.0.0.1",
		Port:     8080,
		Healthy:  true,
		Metadata: map[string]string{}, // no FARP metadata
	}}

	rm.clearOps()

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh with no FARP: %v", err)
	}

	// Verify the catch-all was added before the FARP route was removed.
	ops := rm.opsLog()
	sawRemove := false
	for _, op := range ops {
		if op.op == "remove" {
			sawRemove = true
		}
		if sawRemove && (op.op == "add" || op.op == "update") {
			t.Errorf("add/update after remove — route gap: %s(%s) after remove", op.op, op.routeID)
		}
	}

	// Final state: only the discovery catch-all should exist.
	routes = rm.routesForService("my-svc")
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].ID != "discovery-my-svc" {
		t.Errorf("expected discovery-my-svc, got %s", routes[0].ID)
	}
}

// TestFARP_RegisterService_NoRouteGap verifies that push-based registration
// (RegisterService) also maintains the add-before-remove ordering.
func TestFARP_RegisterService_NoRouteGap(t *testing.T) {
	rm := &trackingRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {farpInstance("my-svc", "10.0.0.1", 8080)},
		},
	}

	sd := newFARPTestManager(rm, mock)

	// Initial refresh to create FARP routes.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}

	initialCount := rm.routeCount()
	if initialCount == 0 {
		t.Fatal("expected routes after initial refresh")
	}

	rm.clearOps()

	// Push registration with same FARP metadata — should update, not gap.
	info := farpInstance("my-svc", "10.0.0.2", 9090)
	if err := sd.RegisterService(context.Background(), info); err != nil {
		t.Fatalf("RegisterService: %v", err)
	}

	// Verify no removes happened (same route IDs → no stale routes).
	ops := rm.opsLog()
	for _, op := range ops {
		if op.op == "remove" {
			t.Errorf("unexpected remove during push registration: remove(%s)", op.routeID)
		}
	}

	// Route count should be unchanged.
	if rm.routeCount() != initialCount {
		t.Errorf("route count changed during push registration: %d -> %d", initialCount, rm.routeCount())
	}
}
