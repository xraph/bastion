package discovery

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// mockDiscoveryService is a mock that implements DiscoveryService for testing.
type mockDiscoveryService struct {
	services         []string
	instances        map[string][]*ServiceInstanceInfo
	listErr          error
	discErr          error
	discErrByService map[string]error // per-service errors (takes precedence over discErr)
}

func (m *mockDiscoveryService) ListServices(_ context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}

	return m.services, nil
}

func (m *mockDiscoveryService) DiscoverHealthy(_ context.Context, name string) ([]*ServiceInstanceInfo, error) {
	if m.discErrByService != nil {
		if err, ok := m.discErrByService[name]; ok {
			return nil, err
		}
	}

	if m.discErr != nil {
		return nil, m.discErr
	}

	return m.instances[name], nil
}

// --- Manager Tests ---

func TestManager_MatchesFilter(t *testing.T) {
	tests := []struct {
		name        string
		serviceName string
		filters     []ServiceFilter
		want        bool
	}{
		{
			name:        "no filters",
			serviceName: "user-service",
			filters:     nil,
			want:        true,
		},
		{
			name:        "include match",
			serviceName: "user-service",
			filters:     []ServiceFilter{{IncludeNames: []string{"user-*"}}},
			want:        true,
		},
		{
			name:        "include no match",
			serviceName: "order-service",
			filters:     []ServiceFilter{{IncludeNames: []string{"user-*"}}},
			want:        false,
		},
		{
			name:        "exclude match",
			serviceName: "internal-metrics",
			filters:     []ServiceFilter{{ExcludeNames: []string{"internal-*"}}},
			want:        false,
		},
		{
			name:        "wildcard all",
			serviceName: "anything",
			filters:     []ServiceFilter{{IncludeNames: []string{"*"}}},
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sd := &Manager{
				config: DiscoveryConfig{
					ServiceFilters: tt.filters,
				},
			}

			got := sd.matchesFilter(tt.serviceName)
			if got != tt.want {
				t.Errorf("matchesFilter(%q) = %v, want %v", tt.serviceName, got, tt.want)
			}
		})
	}
}

func TestManager_FilterInstances(t *testing.T) {
	instances := []*ServiceInstanceInfo{
		{
			ID:       "inst-1",
			Name:     "svc",
			Tags:     []string{"api", "v1"},
			Metadata: map[string]string{"region": "us-east-1", "farp.enabled": "true"},
		},
		{
			ID:       "inst-2",
			Name:     "svc",
			Tags:     []string{"internal", "v1"},
			Metadata: map[string]string{"region": "eu-west-1"},
		},
		{
			ID:       "inst-3",
			Name:     "svc",
			Tags:     []string{"api", "v2"},
			Metadata: map[string]string{"region": "us-east-1"},
		},
	}

	tests := []struct {
		name    string
		filters []ServiceFilter
		wantIDs []string
		wantLen int
	}{
		{
			name:    "no filters",
			filters: nil,
			wantLen: 3,
		},
		{
			name:    "include tags - api",
			filters: []ServiceFilter{{IncludeTags: []string{"api"}}},
			wantLen: 2,
		},
		{
			name:    "exclude tags - internal",
			filters: []ServiceFilter{{ExcludeTags: []string{"internal"}}},
			wantLen: 2,
		},
		{
			name:    "require metadata",
			filters: []ServiceFilter{{RequireMetadata: map[string]string{"region": "us-east-1"}}},
			wantLen: 2,
		},
		{
			name:    "include tags and require metadata",
			filters: []ServiceFilter{{IncludeTags: []string{"api"}, RequireMetadata: map[string]string{"region": "us-east-1"}}},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sd := &Manager{
				config: DiscoveryConfig{
					ServiceFilters: tt.filters,
				},
			}

			result := sd.filterInstances(instances)
			if len(result) != tt.wantLen {
				t.Errorf("filterInstances() returned %d instances, want %d", len(result), tt.wantLen)
			}
		})
	}
}

func TestManager_BuildPrefix(t *testing.T) {
	tests := []struct {
		name        string
		autoPrefix  bool
		template    string
		serviceName string
		want        string
	}{
		{
			name:        "auto prefix disabled",
			autoPrefix:  false,
			serviceName: "user-service",
			want:        "",
		},
		{
			name:        "default template",
			autoPrefix:  true,
			template:    "",
			serviceName: "user-service",
			want:        "/user-service",
		},
		{
			name:        "custom template",
			autoPrefix:  true,
			template:    "/api/{{.ServiceName}}",
			serviceName: "user-service",
			want:        "/api/user-service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sd := &Manager{
				config: DiscoveryConfig{
					AutoPrefix:     tt.autoPrefix,
					PrefixTemplate: tt.template,
				},
			}

			got := sd.BuildPrefix(tt.serviceName)
			if got != tt.want {
				t.Errorf("BuildPrefix(%q) = %q, want %q", tt.serviceName, got, tt.want)
			}
		})
	}
}

func TestManager_BuildPrefix_WithOverrides(t *testing.T) {
	sd := &Manager{
		config: DiscoveryConfig{
			AutoPrefix: true,
			PrefixOverrides: map[string]string{
				"twinos": "",
				"admin":  "/admin-api",
			},
		},
		servicePrefixes: make(map[string]string),
	}

	// Overridden to root
	if got := sd.BuildPrefix("twinos"); got != "" {
		t.Errorf("BuildPrefix(twinos) = %q, want empty (root)", got)
	}

	// Overridden to custom prefix
	if got := sd.BuildPrefix("admin"); got != "/admin-api" {
		t.Errorf("BuildPrefix(admin) = %q, want /admin-api", got)
	}

	// Non-overridden service uses default template
	if got := sd.BuildPrefix("other"); got != "/other" {
		t.Errorf("BuildPrefix(other) = %q, want /other", got)
	}

	// Case insensitive
	if got := sd.BuildPrefix("Twinos"); got != "" {
		t.Errorf("BuildPrefix(Twinos) = %q, want empty (root, case insensitive)", got)
	}
}

func TestManager_GetServicePrefix_PrefixOverrideTakesPrecedence(t *testing.T) {
	sd := &Manager{
		config: DiscoveryConfig{
			AutoPrefix: true,
			PrefixOverrides: map[string]string{
				"twinos": "",
			},
		},
		servicePrefixes: make(map[string]string),
	}

	// Simulate FARP cache having a different prefix
	sd.servicePrefixes["twinos"] = "/twinos"

	// Override should win over FARP cache
	if got := sd.GetServicePrefix("twinos"); got != "" {
		t.Errorf("GetServicePrefix(twinos) = %q, want empty (override wins over cache)", got)
	}

	// Non-overridden service should use FARP cache
	sd.servicePrefixes["portal"] = "/portal"
	if got := sd.GetServicePrefix("portal"); got != "/portal" {
		t.Errorf("GetServicePrefix(portal) = %q, want /portal (from cache)", got)
	}
}

func TestManager_RoutesFromFARP(t *testing.T) {
	tests := []struct {
		name       string
		metadata   map[string]string
		wantRoutes int
	}{
		{
			name:       "no manifest",
			metadata:   map[string]string{},
			wantRoutes: 0,
		},
		{
			name: "manifest but no endpoints",
			metadata: map[string]string{
				"farp.manifest": "http://10.0.0.1:8080/farp/manifest",
			},
			wantRoutes: 0,
		},
		{
			name: "with openapi endpoint",
			metadata: map[string]string{
				"farp.manifest": "http://10.0.0.1:8080/farp/manifest",
				"farp.openapi":  "/openapi.json",
			},
			wantRoutes: 1,
		},
		{
			name: "with all endpoints",
			metadata: map[string]string{
				"farp.manifest": "http://10.0.0.1:8080/farp/manifest",
				"farp.openapi":  "/openapi.json",
				"farp.asyncapi": "/asyncapi.yaml",
				"farp.graphql":  "/graphql",
			},
			wantRoutes: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sd := &Manager{
				config: DiscoveryConfig{
					AutoPrefix:  true,
					StripPrefix: true,
				},
			}

			instance := &ServiceInstanceInfo{
				ID:       "inst-1",
				Name:     "my-svc",
				Address:  "10.0.0.1",
				Port:     8080,
				Metadata: tt.metadata,
			}

			targets := []*Target{{ID: "t1", URL: "http://10.0.0.1:8080"}}
			routes := sd.routesFromFARP("my-svc", instance, targets)

			if len(routes) != tt.wantRoutes {
				t.Errorf("routesFromFARP() returned %d routes, want %d", len(routes), tt.wantRoutes)
			}

			for _, r := range routes {
				if r.Source != SourceFARP {
					t.Errorf("route %s source = %q, want %q", r.ID, r.Source, SourceFARP)
				}
			}
		})
	}
}

func TestManager_Refresh_NilService(t *testing.T) {
	sd := &Manager{
		config:         DiscoveryConfig{Enabled: true},
		service:        nil,
		discoveredSvcs: make(map[string]*DiscoveredService),
	}

	err := sd.refresh(context.Background())
	if err != nil {
		t.Errorf("refresh with nil service should return nil, got: %v", err)
	}
}

func TestManager_Refresh_ListError(t *testing.T) {
	mock := &mockDiscoveryService{
		listErr: fmt.Errorf("connection refused"),
	}

	sd := &Manager{
		config:         DiscoveryConfig{Enabled: true},
		service:        mock,
		discoveredSvcs: make(map[string]*DiscoveredService),
	}

	err := sd.refresh(context.Background())
	if err == nil {
		t.Error("expected error from refresh when ListServices fails")
	}
}

func TestNewManager(t *testing.T) {
	mock := &mockDiscoveryService{}
	rm := &mockRouteRegistry{}
	config := DiscoveryConfig{
		Enabled:      true,
		PollInterval: 30 * time.Second,
	}

	sd := NewManager(config, nil, rm, mock)

	if sd == nil {
		t.Fatal("expected Manager, got nil")
	}

	if sd.service != mock {
		t.Error("expected service to be mock")
	}
}

func TestManager_DiscoveredServices_Empty(t *testing.T) {
	mock := &mockDiscoveryService{}
	rm := &mockRouteRegistry{}
	config := DiscoveryConfig{Enabled: true, PollInterval: 30 * time.Second}
	sd := NewManager(config, nil, rm, mock)

	services := sd.DiscoveredServices()
	if len(services) != 0 {
		t.Errorf("expected 0 discovered services, got %d", len(services))
	}
}

// --- Helper tests ---

func TestHasAllTags(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		required []string
		want     bool
	}{
		{name: "all present", tags: []string{"a", "b", "c"}, required: []string{"a", "b"}, want: true},
		{name: "missing tag", tags: []string{"a", "b"}, required: []string{"a", "c"}, want: false},
		{name: "empty required", tags: []string{"a"}, required: []string{}, want: true},
		{name: "empty tags", tags: []string{}, required: []string{"a"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasAllTags(tt.tags, tt.required)
			if got != tt.want {
				t.Errorf("hasAllTags(%v, %v) = %v, want %v", tt.tags, tt.required, got, tt.want)
			}
		})
	}
}

func TestHasAnyTag(t *testing.T) {
	tests := []struct {
		name  string
		tags  []string
		check []string
		want  bool
	}{
		{name: "has one", tags: []string{"a", "b"}, check: []string{"b", "c"}, want: true},
		{name: "has none", tags: []string{"a", "b"}, check: []string{"c", "d"}, want: false},
		{name: "empty check", tags: []string{"a"}, check: []string{}, want: false},
		{name: "empty tags", tags: []string{}, check: []string{"a"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasAnyTag(tt.tags, tt.check)
			if got != tt.want {
				t.Errorf("hasAnyTag(%v, %v) = %v, want %v", tt.tags, tt.check, got, tt.want)
			}
		})
	}
}

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		pattern string
		want    bool
	}{
		{name: "exact match", s: "test", pattern: "test", want: true},
		{name: "no match", s: "test", pattern: "other", want: false},
		{name: "star all", s: "anything", pattern: "*", want: true},
		{name: "prefix star", s: "user-service", pattern: "user-*", want: true},
		{name: "prefix no match", s: "order-service", pattern: "user-*", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchWildcard(tt.s, tt.pattern)
			if got != tt.want {
				t.Errorf("matchWildcard(%q, %q) = %v, want %v", tt.s, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestUnique(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  int
	}{
		{name: "no duplicates", input: []string{"a", "b", "c"}, want: 3},
		{name: "all duplicates", input: []string{"a", "a", "a"}, want: 1},
		{name: "some duplicates", input: []string{"a", "b", "a", "c", "b"}, want: 3},
		{name: "empty", input: []string{}, want: 0},
		{name: "nil", input: nil, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unique(tt.input)
			if len(got) != tt.want {
				t.Errorf("unique(%v) returned %d elements, want %d", tt.input, len(got), tt.want)
			}
		})
	}
}

func TestMatchesRequiredMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		required map[string]string
		want     bool
	}{
		{
			name:     "all match",
			metadata: map[string]string{"a": "1", "b": "2"},
			required: map[string]string{"a": "1"},
			want:     true,
		},
		{
			name:     "missing key",
			metadata: map[string]string{"a": "1"},
			required: map[string]string{"b": "1"},
			want:     false,
		},
		{
			name:     "wrong value",
			metadata: map[string]string{"a": "1"},
			required: map[string]string{"a": "2"},
			want:     false,
		},
		{
			name:     "empty required",
			metadata: map[string]string{"a": "1"},
			required: map[string]string{},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesRequiredMetadata(tt.metadata, tt.required)
			if got != tt.want {
				t.Errorf("matchesRequiredMetadata(%v, %v) = %v, want %v", tt.metadata, tt.required, got, tt.want)
			}
		})
	}
}

// --- Route preservation tests ---

// newTestManager creates a Manager with pre-discovered service and routes for testing.
func newTestManager(rm *mockRouteRegistry, mock *mockDiscoveryService) *Manager {
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
	}
}

func TestRefresh_DiscoverError_KeepsRoutes(t *testing.T) {
	rm := &mockRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {
				{ID: "inst-1", Name: "my-svc", Address: "10.0.0.1", Port: 8080, Healthy: true},
			},
		},
	}

	sd := newTestManager(rm, mock)

	// Initial refresh — service is discovered and routes are created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh failed: %v", err)
	}

	if len(rm.routes) == 0 {
		t.Fatal("expected routes to be created after initial refresh")
	}

	initialRouteCount := len(rm.routes)

	// Now simulate a transient DiscoverHealthy error for this service.
	mock.discErrByService = map[string]error{
		"my-svc": fmt.Errorf("connection refused"),
	}

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh with discover error failed: %v", err)
	}

	// Routes should NOT have been removed.
	if len(rm.routes) != initialRouteCount {
		t.Errorf("expected %d routes to be preserved, got %d", initialRouteCount, len(rm.routes))
	}

	if len(rm.removedServiceNames) > 0 {
		t.Errorf("expected no service removals, got %v", rm.removedServiceNames)
	}

	// Service should be marked unhealthy.
	sd.mu.RLock()
	svc, ok := sd.discoveredSvcs["my-svc"]
	sd.mu.RUnlock()
	if !ok {
		t.Fatal("expected my-svc to still be in discoveredSvcs")
	}
	if svc.Healthy {
		t.Error("expected my-svc to be marked unhealthy")
	}
}

func TestRefresh_ZeroInstances_MarksUnhealthy(t *testing.T) {
	rm := &mockRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {
				{ID: "inst-1", Name: "my-svc", Address: "10.0.0.1", Port: 8080, Healthy: true},
			},
		},
	}

	sd := newTestManager(rm, mock)

	// Initial refresh — routes created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh failed: %v", err)
	}

	if len(rm.routes) == 0 {
		t.Fatal("expected routes after initial refresh")
	}

	// Store a reference to verify target health later
	initialRouteCount := len(rm.routes)

	// Now simulate service returning 0 healthy instances (e.g., during restart).
	mock.instances["my-svc"] = nil

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh with zero instances failed: %v", err)
	}

	// Routes should NOT have been removed.
	if len(rm.routes) != initialRouteCount {
		t.Errorf("expected %d routes preserved, got %d", initialRouteCount, len(rm.routes))
	}

	if len(rm.removedServiceNames) > 0 {
		t.Errorf("expected no service removals, got %v", rm.removedServiceNames)
	}

	// All targets should be marked unhealthy.
	for _, route := range rm.routes {
		if route.ServiceName != "my-svc" {
			continue
		}
		for _, target := range route.Targets {
			if target.Healthy {
				t.Errorf("expected target %s to be unhealthy", target.ID)
			}
		}
	}
}

func TestRefresh_GracePeriod(t *testing.T) {
	rm := &mockRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {
				{ID: "inst-1", Name: "my-svc", Address: "10.0.0.1", Port: 8080, Healthy: true},
			},
		},
	}

	sd := newTestManager(rm, mock)
	sd.config.RemovalGracePeriod = 100 * time.Millisecond // short for testing

	// Initial refresh — routes created.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh failed: %v", err)
	}

	if len(rm.routes) == 0 {
		t.Fatal("expected routes after initial refresh")
	}

	// Remove service from ListServices entirely.
	mock.services = []string{}
	mock.instances = map[string][]*ServiceInstanceInfo{}

	// Immediate refresh — within grace period, routes should be kept.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh within grace period failed: %v", err)
	}

	if len(rm.removedServiceNames) > 0 {
		t.Errorf("expected no removals within grace period, got %v", rm.removedServiceNames)
	}

	// Wait for grace period to elapse.
	time.Sleep(150 * time.Millisecond)

	// Refresh again — now past grace period, routes should be removed.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after grace period failed: %v", err)
	}

	if len(rm.removedServiceNames) == 0 {
		t.Error("expected service removal after grace period elapsed")
	}
}

func TestRefresh_ServiceRecovers(t *testing.T) {
	rm := &mockRouteRegistry{}
	mock := &mockDiscoveryService{
		services: []string{"my-svc"},
		instances: map[string][]*ServiceInstanceInfo{
			"my-svc": {
				{ID: "inst-1", Name: "my-svc", Address: "10.0.0.1", Port: 8080, Healthy: true},
			},
		},
	}

	sd := newTestManager(rm, mock)

	// Initial refresh — routes created with healthy targets.
	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh failed: %v", err)
	}

	// Simulate outage — 0 instances.
	mock.instances["my-svc"] = nil

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh during outage failed: %v", err)
	}

	// Verify targets are unhealthy.
	for _, route := range rm.routes {
		if route.ServiceName != "my-svc" {
			continue
		}
		for _, target := range route.Targets {
			if target.Healthy {
				t.Fatalf("expected target %s to be unhealthy during outage", target.ID)
			}
		}
	}

	// Service recovers — healthy instances again.
	mock.instances["my-svc"] = []*ServiceInstanceInfo{
		{ID: "inst-1", Name: "my-svc", Address: "10.0.0.1", Port: 8080, Healthy: true},
	}

	if err := sd.refresh(context.Background()); err != nil {
		t.Fatalf("refresh after recovery failed: %v", err)
	}

	// Verify targets are healthy again.
	found := false
	for _, route := range rm.routes {
		if route.ServiceName != "my-svc" {
			continue
		}
		found = true
		for _, target := range route.Targets {
			if !target.Healthy {
				t.Errorf("expected target %s to be healthy after recovery", target.ID)
			}
		}
	}

	if !found {
		t.Error("expected routes for my-svc to exist after recovery")
	}
}
