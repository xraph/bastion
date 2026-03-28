package bastion

import (
	"testing"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/go-utils/log"
)

func newMockLogger() forge.Logger {
	return log.NewTestLogger()
}

// mockRouteRegistry is a minimal RouteRegistry for testing.
type mockRouteRegistry struct {
	routes map[string]*Route
}

func newMockRouteRegistry() *mockRouteRegistry {
	return &mockRouteRegistry{routes: make(map[string]*Route)}
}

func (m *mockRouteRegistry) AddRoute(route *Route) error {
	m.routes[route.ID] = route
	return nil
}

func (m *mockRouteRegistry) RemoveRoute(id string) error {
	delete(m.routes, id)
	return nil
}

func (m *mockRouteRegistry) UpdateRoute(route *Route) error {
	m.routes[route.ID] = route
	return nil
}

func (m *mockRouteRegistry) GetRoute(id string) (*Route, bool) {
	r, ok := m.routes[id]
	return r, ok
}

func (m *mockRouteRegistry) ListRoutes() []*Route {
	routes := make([]*Route, 0, len(m.routes))
	for _, r := range m.routes {
		routes = append(routes, r)
	}
	return routes
}

func (m *mockRouteRegistry) MatchRoute(path, method string) *Route {
	return nil
}

func (m *mockRouteRegistry) RouteCount() int {
	return len(m.routes)
}

func (m *mockRouteRegistry) RemoveBySource(source RouteSource) {}

func (m *mockRouteRegistry) RemoveByServiceName(serviceName string) {}

func (m *mockRouteRegistry) OnRouteChange(fn func(RouteEvent)) {}

// =============================================================================
// OpenAPI Aggregator Tests
// =============================================================================

func TestNewOpenAPIAggregator(t *testing.T) {
	config := DefaultOpenAPIConfig()
	logger := newMockLogger()
	rm := newMockRouteRegistry()

	aggr := NewOpenAPIAggregator(config, logger, rm, nil)

	if aggr == nil {
		t.Fatal("expected aggregator, got nil")
	}
}

func TestOpenAPIAggregator_MergedSpecJSON(t *testing.T) {
	config := DefaultOpenAPIConfig()
	config.Title = "Test Gateway"
	logger := newMockLogger()
	rm := newMockRouteRegistry()

	aggr := NewOpenAPIAggregator(config, logger, rm, nil)

	// Initially should be nil
	specJSON := aggr.MergedSpec()
	if specJSON != nil {
		t.Error("expected nil spec before refresh")
	}
}

func TestOpenAPIAggregator_ServiceSpec(t *testing.T) {
	config := DefaultOpenAPIConfig()
	logger := newMockLogger()
	rm := newMockRouteRegistry()

	aggr := NewOpenAPIAggregator(config, logger, rm, nil)

	// Initially should return nil
	spec := aggr.ServiceSpec("non-existent")
	if spec != nil {
		t.Error("expected nil for non-existent service")
	}
}

func TestOpenAPIAggregator_HandleMergedSpec_NotReady(t *testing.T) {
	config := DefaultOpenAPIConfig()
	logger := newMockLogger()
	rm := newMockRouteRegistry()

	aggr := NewOpenAPIAggregator(config, logger, rm, nil)

	// Test that MergedSpec returns nil before any refresh
	specJSON := aggr.MergedSpec()
	if specJSON != nil {
		t.Error("expected nil spec before any refresh")
	}
}

func TestOpenAPIConfig_Defaults(t *testing.T) {
	config := DefaultOpenAPIConfig()

	if config.Path == "" {
		t.Error("expected default path")
	}

	if config.UIPath == "" {
		t.Error("expected default UI path")
	}

	if config.RefreshInterval == 0 {
		t.Error("expected default refresh interval")
	}

	if config.FetchTimeout == 0 {
		t.Error("expected default fetch timeout")
	}

	if config.MergeStrategy == "" {
		t.Error("expected default merge strategy")
	}
}

func TestServiceOpenAPISpec(t *testing.T) {
	spec := &ServiceOpenAPISpec{
		ServiceName: "test-service",
		Version:     "1.0.0",
		SpecURL:     "http://localhost:8080/openapi.json",
		Healthy:     true,
		PathCount:   5,
		FetchedAt:   time.Now(),
	}

	if spec.ServiceName != "test-service" {
		t.Errorf("expected service name test-service, got %s", spec.ServiceName)
	}

	if !spec.Healthy {
		t.Error("expected healthy spec")
	}

	if spec.PathCount != 5 {
		t.Errorf("expected 5 paths, got %d", spec.PathCount)
	}
}
