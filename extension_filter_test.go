package bastion

import (
	"testing"
)

func TestIsExtensionPathExcluded(t *testing.T) {
	filter := &ExtensionPathFilter{
		ServiceName:       "twinos",
		KnownExtensions:   []string{"cortex", "authsome", "webhooks", "dispatch"},
		AllowedExtensions: []string{"authsome"},
	}

	tests := []struct {
		name     string
		path     string
		filter   *ExtensionPathFilter
		excluded bool
	}{
		// --- First-segment matches (existing behaviour) ---
		{
			name:     "excluded extension first segment",
			path:     "/cortex/agents",
			filter:   filter,
			excluded: true,
		},
		{
			name:     "excluded extension nested path",
			path:     "/cortex/agents/my-agent/run",
			filter:   filter,
			excluded: true,
		},
		{
			name:     "excluded extension root",
			path:     "/webhooks",
			filter:   filter,
			excluded: true,
		},
		{
			name:     "allowed extension",
			path:     "/authsome/permissions",
			filter:   filter,
			excluded: false,
		},
		{
			name:     "native service path",
			path:     "/users",
			filter:   filter,
			excluded: false,
		},
		{
			name:     "native service path nested",
			path:     "/users/123/profile",
			filter:   filter,
			excluded: false,
		},
		{
			name:     "root path",
			path:     "/",
			filter:   filter,
			excluded: false,
		},
		{
			name:     "empty path",
			path:     "",
			filter:   filter,
			excluded: false,
		},
		{
			name:     "nil filter",
			path:     "/cortex/agents",
			filter:   nil,
			excluded: false,
		},
		{
			name:     "empty known extensions",
			path:     "/cortex/agents",
			filter:   &ExtensionPathFilter{KnownExtensions: nil},
			excluded: false,
		},
		{
			name:     "case insensitive match",
			path:     "/Cortex/agents",
			filter:   filter,
			excluded: true,
		},
		{
			name:     "case insensitive allowed",
			path:     "/Authsome/permissions",
			filter:   filter,
			excluded: false,
		},
		{
			name: "empty allowed list excludes all known",
			path: "/cortex/agents",
			filter: &ExtensionPathFilter{
				KnownExtensions: []string{"cortex", "authsome"},
			},
			excluded: true,
		},

		// --- Any-segment matching (NEW: fixes dispatch-style nesting) ---
		{
			name:     "nested extension /api/dispatch/v1/jobs",
			path:     "/api/dispatch/v1/jobs",
			filter:   filter,
			excluded: true,
		},
		{
			name:     "nested extension /api/dispatch/v1/crons/{id}",
			path:     "/api/dispatch/v1/crons/abc123",
			filter:   filter,
			excluded: true,
		},
		{
			name:     "nested allowed extension /api/authsome/v1/check",
			path:     "/api/authsome/v1/check",
			filter:   filter,
			excluded: false,
		},
		{
			name:     "native path with api prefix",
			path:     "/api/v1/formulas/ai/generate",
			filter:   filter,
			excluded: false,
		},
		{
			name:     "native v1 path",
			path:     "/v1/auth/signin",
			filter:   filter,
			excluded: false,
		},

		// --- ExcludePrefixes ---
		{
			name: "explicit prefix exclusion",
			path: "/v1/admin/tenants",
			filter: &ExtensionPathFilter{
				KnownExtensions: []string{"cortex"},
				ExcludePrefixes: []string{"/v1/admin"},
			},
			excluded: true,
		},
		{
			name: "explicit prefix exclusion nested",
			path: "/v1/admin/tenants/abc/quota",
			filter: &ExtensionPathFilter{
				KnownExtensions: []string{"cortex"},
				ExcludePrefixes: []string{"/v1/admin"},
			},
			excluded: true,
		},
		{
			name: "explicit prefix no match",
			path: "/api/v1/formulas",
			filter: &ExtensionPathFilter{
				KnownExtensions: []string{"cortex"},
				ExcludePrefixes: []string{"/v1/admin"},
			},
			excluded: false,
		},

		// --- IncludePrefixes override ---
		{
			name: "include prefix overrides known extension match",
			path: "/cortex/special",
			filter: &ExtensionPathFilter{
				KnownExtensions: []string{"cortex"},
				IncludePrefixes: []string{"/cortex/special"},
			},
			excluded: false,
		},
		{
			name: "include prefix overrides exclude prefix",
			path: "/v1/admin/health",
			filter: &ExtensionPathFilter{
				ExcludePrefixes: []string{"/v1/admin"},
				IncludePrefixes: []string{"/v1/admin/health"},
			},
			excluded: false,
		},
		{
			name: "exclude prefix without include override",
			path: "/v1/admin/tenants",
			filter: &ExtensionPathFilter{
				ExcludePrefixes: []string{"/v1/admin"},
				IncludePrefixes: []string{"/v1/admin/health"},
			},
			excluded: true,
		},

		// --- Combined filters ---
		{
			name: "full config: native path passes through",
			path: "/api/v1/formulas/ai/generate",
			filter: &ExtensionPathFilter{
				KnownExtensions:   []string{"cortex", "dispatch", "webhooks"},
				AllowedExtensions: []string{"authsome"},
				ExcludePrefixes:   []string{"/v1/admin", "/v1/instances"},
			},
			excluded: false,
		},
		{
			name: "full config: nested dispatch excluded",
			path: "/api/dispatch/v1/jobs",
			filter: &ExtensionPathFilter{
				KnownExtensions:   []string{"cortex", "dispatch", "webhooks"},
				AllowedExtensions: []string{},
				ExcludePrefixes:   []string{"/v1/admin"},
			},
			excluded: true,
		},
		{
			name: "full config: explicit prefix excluded",
			path: "/v1/instances/abc/deploy",
			filter: &ExtensionPathFilter{
				KnownExtensions: []string{"cortex"},
				ExcludePrefixes: []string{"/v1/admin", "/v1/instances"},
			},
			excluded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsExtensionPathExcluded(tt.path, tt.filter)
			if got != tt.excluded {
				t.Errorf("IsExtensionPathExcluded(%q) = %v, want %v", tt.path, got, tt.excluded)
			}
		})
	}
}

func TestExtensionPathFilter_MatchesService(t *testing.T) {
	tests := []struct {
		name    string
		filter  ExtensionPathFilter
		service string
		matches bool
	}{
		{
			name:    "matches ServiceName",
			filter:  ExtensionPathFilter{ServiceName: "portal"},
			service: "portal",
			matches: true,
		},
		{
			name:    "matches ServiceName case insensitive",
			filter:  ExtensionPathFilter{ServiceName: "Portal"},
			service: "portal",
			matches: true,
		},
		{
			name:    "matches ServiceNames entry",
			filter:  ExtensionPathFilter{ServiceNames: []string{"portal", "twinos"}},
			service: "twinos",
			matches: true,
		},
		{
			name:    "matches ServiceNames case insensitive",
			filter:  ExtensionPathFilter{ServiceNames: []string{"Portal", "Twinos"}},
			service: "twinos",
			matches: true,
		},
		{
			name:    "matches either ServiceName or ServiceNames",
			filter:  ExtensionPathFilter{ServiceName: "portal", ServiceNames: []string{"twinos"}},
			service: "twinos",
			matches: true,
		},
		{
			name:    "no match",
			filter:  ExtensionPathFilter{ServiceName: "portal", ServiceNames: []string{"twinos"}},
			service: "other-service",
			matches: false,
		},
		{
			name:    "empty filter matches nothing",
			filter:  ExtensionPathFilter{},
			service: "portal",
			matches: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.filter.MatchesService(tt.service)
			if got != tt.matches {
				t.Errorf("MatchesService(%q) = %v, want %v", tt.service, got, tt.matches)
			}
		})
	}
}

func TestPathSegments(t *testing.T) {
	tests := []struct {
		path     string
		expected []string
	}{
		{"/cortex/agents", []string{"cortex", "agents"}},
		{"/api/dispatch/v1/jobs", []string{"api", "dispatch", "v1", "jobs"}},
		{"/users", []string{"users"}},
		{"/", nil},
		{"", nil},
		{"cortex/agents", []string{"cortex", "agents"}},
		{"/a/b/c", []string{"a", "b", "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := pathSegments(tt.path)
			if len(got) != len(tt.expected) {
				t.Fatalf("pathSegments(%q) = %v, want %v", tt.path, got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("pathSegments(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.expected[i])
				}
			}
		})
	}
}
