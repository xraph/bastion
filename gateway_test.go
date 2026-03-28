package bastion

import (
	"testing"
)

// =============================================================================
// Basic Tests - These verify the core functionality works
// =============================================================================

func TestNew(t *testing.T) {
	ext := New(
		WithEnabled(true),
		WithBasePath("/gateway"),
	)

	if ext == nil {
		t.Fatal("expected extension, got nil")
	}

	gateway, ok := ext.(*Gateway)
	if !ok {
		t.Fatal("expected *Gateway type")
	}

	if !gateway.config.Enabled {
		t.Error("expected gateway enabled")
	}

	if gateway.config.BasePath != "/gateway" {
		t.Errorf("expected base path /gateway, got %s", gateway.config.BasePath)
	}
}

func TestExtension_Name(t *testing.T) {
	ext := New().(*Gateway)

	if ext.Name() != "bastion" {
		t.Errorf("expected name 'bastion', got %s", ext.Name())
	}
}

func TestExtension_Version(t *testing.T) {
	ext := New().(*Gateway)

	if ext.Version() == "" {
		t.Error("expected non-empty version")
	}
}

// =============================================================================
// Types Tests
// =============================================================================

func TestCircuitState_Values(t *testing.T) {
	states := []CircuitState{CircuitClosed, CircuitOpen, CircuitHalfOpen}

	for _, state := range states {
		if string(state) == "" {
			t.Errorf("circuit state %v has empty string value", state)
		}
	}
}

func TestRouteProtocol_Values(t *testing.T) {
	protocols := []RouteProtocol{
		ProtocolHTTP,
		ProtocolWebSocket,
		ProtocolSSE,
		ProtocolGRPC,
	}

	for _, protocol := range protocols {
		if string(protocol) == "" {
			t.Errorf("protocol %v has empty string value", protocol)
		}
	}
}

func TestConfigToRoute_BasePathJoining(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		rcPath   string
		wantPath string
	}{
		{"empty base path", "", "/portal/*", "/portal/*"},
		{"slash base path", "/", "/portal/*", "/portal/*"},
		{"normal base path", "/gw", "/portal/*", "/gw/portal/*"},
		{"trailing slash base path", "/gw/", "/portal/*", "/gw/portal/*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := RouteConfig{
				Path:    tt.rcPath,
				Enabled: true,
				Targets: []TargetConfig{{URL: "http://localhost:8080", Weight: 1}},
			}
			route := configToRoute(rc, tt.basePath)
			if route.Path != tt.wantPath {
				t.Errorf("configToRoute(basePath=%q, path=%q) = %q, want %q",
					tt.basePath, tt.rcPath, route.Path, tt.wantPath)
			}
		})
	}
}

func TestLoadBalanceStrategy_Values(t *testing.T) {
	strategies := []LoadBalanceStrategy{
		LBRoundRobin,
		LBWeightedRoundRobin,
		LBRandom,
		LBLeastConnections,
	}

	for _, strategy := range strategies {
		if string(strategy) == "" {
			t.Errorf("strategy %v has empty string value", strategy)
		}
	}
}
