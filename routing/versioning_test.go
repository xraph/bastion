package routing

import (
	"net/http/httptest"
	"testing"

	bastion "github.com/xraph/bastion"
)

func TestVersionRouter_PathStrategy(t *testing.T) {
	vr := NewVersionRouter(VersioningConfig{
		Enabled:  true,
		Strategy: "path",
		Mappings: []VersionMapping{
			{Version: "v1", TargetTags: []string{"stable"}},
			{Version: "v2", TargetTags: []string{"beta"}},
		},
	})

	tests := []struct {
		name    string
		path    string
		want    string
		wantNil bool
	}{
		{"v1 path", "/v1/users", "v1", false},
		{"v2 path", "/v2/users", "v2", false},
		{"no version", "/users", "", true},
		{"unknown version", "/v3/users", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			m := vr.Resolve(req)

			if tt.wantNil && m != nil {
				t.Errorf("expected nil mapping, got %v", m)
			}
			if !tt.wantNil {
				if m == nil {
					t.Fatal("expected mapping, got nil")
				}
				if m.Version != tt.want {
					t.Errorf("expected version %q, got %q", tt.want, m.Version)
				}
			}
		})
	}
}

func TestVersionRouter_HeaderStrategy(t *testing.T) {
	vr := NewVersionRouter(VersioningConfig{
		Enabled:    true,
		Strategy:   "header",
		HeaderName: "Accept-Version",
		Mappings: []VersionMapping{
			{Version: "v1", TargetTags: []string{"stable"}},
			{Version: "v2", TargetTags: []string{"beta"}},
		},
	})

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("Accept-Version", "v2")

	m := vr.Resolve(req)
	if m == nil {
		t.Fatal("expected mapping")
	}
	if m.Version != "v2" {
		t.Errorf("expected v2, got %q", m.Version)
	}
}

func TestVersionRouter_DefaultVersion(t *testing.T) {
	vr := NewVersionRouter(VersioningConfig{
		Enabled:        true,
		Strategy:       "header",
		HeaderName:     "Accept-Version",
		DefaultVersion: "v1",
		Mappings: []VersionMapping{
			{Version: "v1", TargetTags: []string{"stable"}},
		},
	})

	req := httptest.NewRequest("GET", "/users", nil)
	// No version header

	m := vr.Resolve(req)
	if m == nil {
		t.Fatal("expected default mapping")
	}
	if m.Version != "v1" {
		t.Errorf("expected default v1, got %q", m.Version)
	}
}

func TestVersionRouter_FilterTargets(t *testing.T) {
	vr := NewVersionRouter(VersioningConfig{
		Enabled:    true,
		Strategy:   "header",
		HeaderName: "Accept-Version",
		Mappings: []VersionMapping{
			{Version: "v1", TargetTags: []string{"stable"}},
			{Version: "v2", TargetTags: []string{"beta"}},
		},
	})

	targets := []*bastion.Target{
		{ID: "t1", URL: "http://stable:8080", Tags: []string{"stable"}},
		{ID: "t2", URL: "http://beta:8080", Tags: []string{"beta"}},
		{ID: "t3", URL: "http://stable2:8080", Tags: []string{"stable"}},
	}

	// Request v1 -- should get stable targets
	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("Accept-Version", "v1")

	filtered := vr.FilterTargets(req, targets)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 stable targets, got %d", len(filtered))
	}

	for _, f := range filtered {
		hasStable := false
		for _, tag := range f.Tags {
			if tag == "stable" {
				hasStable = true
			}
		}
		if !hasStable {
			t.Errorf("target %s should have stable tag", f.ID)
		}
	}

	// Request v2 -- should get beta target
	req2 := httptest.NewRequest("GET", "/users", nil)
	req2.Header.Set("Accept-Version", "v2")

	filtered2 := vr.FilterTargets(req2, targets)
	if len(filtered2) != 1 {
		t.Fatalf("expected 1 beta target, got %d", len(filtered2))
	}
}

func TestVersionRouter_FilterTargets_NoMatch(t *testing.T) {
	vr := NewVersionRouter(VersioningConfig{
		Enabled:    true,
		Strategy:   "header",
		HeaderName: "Accept-Version",
		Mappings: []VersionMapping{
			{Version: "v1", TargetTags: []string{"nonexistent"}},
		},
	})

	targets := []*bastion.Target{
		{ID: "t1", URL: "http://a:8080", Tags: []string{"stable"}},
	}

	req := httptest.NewRequest("GET", "/users", nil)
	req.Header.Set("Accept-Version", "v1")

	// No targets match tags -- should fall back to all
	filtered := vr.FilterTargets(req, targets)
	if len(filtered) != 1 {
		t.Fatalf("expected fallback to all targets, got %d", len(filtered))
	}
}

func TestVersionRouter_Disabled(t *testing.T) {
	vr := NewVersionRouter(VersioningConfig{Enabled: false})

	req := httptest.NewRequest("GET", "/v1/users", nil)
	m := vr.Resolve(req)

	if m != nil {
		t.Error("disabled router should return nil")
	}
}

func TestExtractPathVersion(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/v1/users", "v1"},
		{"/v2/products", "v2"},
		{"/v10/orders", "v10"},
		{"/users", ""},
		{"/api/v1/users", ""}, // v1 is not first segment
		{"/v/users", ""},      // "v" alone is too short to be meaningful
	}

	for _, tt := range tests {
		got := extractPathVersion(tt.path)
		if got != tt.want {
			t.Errorf("extractPathVersion(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
