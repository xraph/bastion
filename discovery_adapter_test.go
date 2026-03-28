package bastion

import (
	"context"
	"testing"
	"time"
)

// =============================================================================
// Discovery Adapter Tests
// =============================================================================

// mockDiscoveryService is a mock that implements DiscoveryService for testing.
type mockDiscoveryService struct {
	services  []string
	instances map[string][]*ServiceInstanceInfo
	listErr   error
	discErr   error
}

func (m *mockDiscoveryService) ListServices(_ context.Context) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}

	return m.services, nil
}

func (m *mockDiscoveryService) DiscoverHealthy(_ context.Context, serviceName string) ([]*ServiceInstanceInfo, error) {
	if m.discErr != nil {
		return nil, m.discErr
	}

	return m.instances[serviceName], nil
}

func TestServiceInstanceInfo_URL(t *testing.T) {
	tests := []struct {
		name     string
		instance *ServiceInstanceInfo
		scheme   string
		want     string
	}{
		{
			name:     "default scheme",
			instance: &ServiceInstanceInfo{Address: "10.0.0.1", Port: 8080},
			scheme:   "",
			want:     "http://10.0.0.1:8080",
		},
		{
			name:     "https scheme",
			instance: &ServiceInstanceInfo{Address: "10.0.0.1", Port: 443},
			scheme:   "https",
			want:     "https://10.0.0.1:443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.instance.URL(tt.scheme)
			if got != tt.want {
				t.Errorf("URL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceInstanceInfo_IsHealthy(t *testing.T) {
	healthy := &ServiceInstanceInfo{Healthy: true}
	unhealthy := &ServiceInstanceInfo{Healthy: false}

	if !healthy.IsHealthy() {
		t.Error("expected healthy instance to be healthy")
	}

	if unhealthy.IsHealthy() {
		t.Error("expected unhealthy instance to not be healthy")
	}
}

// =============================================================================
// ServiceDiscovery Integration Tests (public API only)
// =============================================================================

func TestNewServiceDiscovery(t *testing.T) {
	mock := &mockDiscoveryService{}
	config := DiscoveryConfig{
		Enabled:      true,
		PollInterval: 30 * time.Second,
	}

	sd := NewServiceDiscovery(config, nil, nil, mock)

	if sd == nil {
		t.Fatal("expected ServiceDiscovery, got nil")
	}
}

func TestServiceDiscovery_DiscoveredServices_Empty(t *testing.T) {
	mock := &mockDiscoveryService{}
	config := DiscoveryConfig{Enabled: true, PollInterval: 30 * time.Second}
	sd := NewServiceDiscovery(config, nil, nil, mock)

	services := sd.DiscoveredServices()
	if len(services) != 0 {
		t.Errorf("expected 0 discovered services, got %d", len(services))
	}
}

// =============================================================================
// Gateway Discovery ConfigOption Tests
// =============================================================================

func TestWithDiscoveryPollInterval(t *testing.T) {
	config := DefaultConfig()
	WithDiscoveryPollInterval(5 * time.Second)(&config)

	if config.Discovery.PollInterval != 5*time.Second {
		t.Errorf("expected poll interval 5s, got %v", config.Discovery.PollInterval)
	}
}

func TestWithDiscoveryWatchMode(t *testing.T) {
	config := DefaultConfig()
	WithDiscoveryWatchMode(false)(&config)

	if config.Discovery.WatchMode {
		t.Error("expected watch mode false")
	}
}

func TestWithDiscoveryAutoPrefix(t *testing.T) {
	config := DefaultConfig()
	WithDiscoveryAutoPrefix(false)(&config)

	if config.Discovery.AutoPrefix {
		t.Error("expected auto prefix false")
	}
}

func TestWithDiscoveryPrefixTemplate(t *testing.T) {
	config := DefaultConfig()
	WithDiscoveryPrefixTemplate("/api/{{.ServiceName}}")(&config)

	if config.Discovery.PrefixTemplate != "/api/{{.ServiceName}}" {
		t.Errorf("expected prefix template, got %s", config.Discovery.PrefixTemplate)
	}
}

func TestWithDiscoveryStripPrefix(t *testing.T) {
	config := DefaultConfig()
	WithDiscoveryStripPrefix(false)(&config)

	if config.Discovery.StripPrefix {
		t.Error("expected strip prefix false")
	}
}

func TestWithDiscoveryServiceFilters(t *testing.T) {
	config := DefaultConfig()
	filters := []ServiceFilter{
		{
			IncludeNames:    []string{"user-*"},
			ExcludeNames:    []string{"internal-*"},
			IncludeTags:     []string{"production"},
			ExcludeTags:     []string{"deprecated"},
			RequireMetadata: map[string]string{"farp.enabled": "true"},
		},
	}

	WithDiscoveryServiceFilters(filters...)(&config)

	if len(config.Discovery.ServiceFilters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(config.Discovery.ServiceFilters))
	}

	f := config.Discovery.ServiceFilters[0]
	if len(f.IncludeNames) != 1 || f.IncludeNames[0] != "user-*" {
		t.Errorf("unexpected include names: %v", f.IncludeNames)
	}

	if len(f.IncludeTags) != 1 || f.IncludeTags[0] != "production" {
		t.Errorf("unexpected include tags: %v", f.IncludeTags)
	}

	if len(f.RequireMetadata) != 1 {
		t.Errorf("unexpected require metadata: %v", f.RequireMetadata)
	}
}
