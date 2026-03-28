package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/go-utils/log"
)

func newTestLogger() forge.Logger {
	return log.NewTestLogger()
}

func TestAggregator_FetchServiceSpec_Success(t *testing.T) {
	// Create mock OpenAPI server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spec := map[string]any{
			"openapi": "3.1.0",
			"info": map[string]any{
				"title":   "Test API",
				"version": "1.0.0",
			},
			"paths": map[string]any{
				"/users": map[string]any{
					"get": map[string]any{
						"summary": "List users",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(spec)
	}))
	defer server.Close()

	config := DefaultOpenAPIConfig()
	config.FetchTimeout = 5 * time.Second
	logger := newTestLogger()
	rm := &mockRouteRegistry{}

	aggr := NewAggregator(config, logger, rm, nil)

	svc := discoveredOpenAPIService{
		Name:    "test-service",
		Version: "1.0.0",
		SpecURL: server.URL,
	}

	spec := aggr.fetchServiceSpec(context.Background(), svc)

	if spec == nil {
		t.Fatal("expected spec, got nil")
	}

	if !spec.Healthy {
		t.Error("expected healthy spec")
	}

	if spec.Error != "" {
		t.Errorf("unexpected error: %s", spec.Error)
	}

	if spec.PathCount != 1 {
		t.Errorf("expected 1 path, got %d", spec.PathCount)
	}
}

func TestAggregator_FetchServiceSpec_404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	config := DefaultOpenAPIConfig()
	config.FetchTimeout = 5 * time.Second
	logger := newTestLogger()
	rm := &mockRouteRegistry{}

	aggr := NewAggregator(config, logger, rm, nil)

	svc := discoveredOpenAPIService{
		Name:    "test-service",
		Version: "1.0.0",
		SpecURL: server.URL,
	}

	spec := aggr.fetchServiceSpec(context.Background(), svc)

	if spec == nil {
		t.Fatal("expected spec result, got nil")
	}

	if spec.Healthy {
		t.Error("expected unhealthy spec for 404")
	}

	if spec.Error == "" {
		t.Error("expected error message")
	}
}

func TestAggregator_FetchServiceSpec_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	config := DefaultOpenAPIConfig()
	config.FetchTimeout = 5 * time.Second
	logger := newTestLogger()
	rm := &mockRouteRegistry{}

	aggr := NewAggregator(config, logger, rm, nil)

	svc := discoveredOpenAPIService{
		Name:    "test-service",
		Version: "1.0.0",
		SpecURL: server.URL,
	}

	spec := aggr.fetchServiceSpec(context.Background(), svc)

	if spec == nil {
		t.Fatal("expected spec result, got nil")
	}

	if spec.Healthy {
		t.Error("expected unhealthy spec for invalid JSON")
	}

	if spec.Error == "" {
		t.Error("expected error message for invalid JSON")
	}
}

func TestAggregator_BuildMergedSpec(t *testing.T) {
	config := DefaultOpenAPIConfig()
	config.Title = "Test Gateway"
	config.Version = "1.0.0"
	config.MergeStrategy = "prefix"
	logger := newTestLogger()
	rm := &mockRouteRegistry{}

	aggr := NewAggregator(config, logger, rm, nil)

	specs := map[string]*ServiceOpenAPISpec{
		"service-a": {
			ServiceName: "service-a",
			Version:     "1.0.0",
			Healthy:     true,
			Spec: map[string]any{
				"openapi": "3.1.0",
				"paths": map[string]any{
					"/users": map[string]any{
						"get": map[string]any{
							"summary": "List users",
						},
					},
				},
			},
		},
		"service-b": {
			ServiceName: "service-b",
			Version:     "2.0.0",
			Healthy:     true,
			Spec: map[string]any{
				"openapi": "3.1.0",
				"paths": map[string]any{
					"/products": map[string]any{
						"get": map[string]any{
							"summary": "List products",
						},
					},
				},
			},
		},
	}

	merged := aggr.buildMergedSpec(specs)

	if merged == nil {
		t.Fatal("expected merged spec, got nil")
	}

	if merged["openapi"] != "3.1.0" {
		t.Error("expected OpenAPI version 3.1.0")
	}

	info, ok := merged["info"].(map[string]any)
	if !ok {
		t.Fatal("expected info object")
	}

	if info["title"] != "Test Gateway" {
		t.Errorf("expected title 'Test Gateway', got %v", info["title"])
	}

	paths, ok := merged["paths"].(map[string]any)
	if !ok {
		t.Fatal("expected paths object")
	}

	if _, ok := paths["/service-a/users"]; !ok {
		t.Error("expected /service-a/users path")
	}

	if _, ok := paths["/service-b/products"]; !ok {
		t.Error("expected /service-b/products path")
	}

	tags, ok := merged["tags"].([]any)
	if !ok {
		t.Fatal("expected tags array")
	}

	if len(tags) < 2 {
		t.Errorf("expected at least 2 tags (one per service), got %d", len(tags))
	}
}

func TestCountPaths(t *testing.T) {
	tests := []struct {
		name     string
		spec     map[string]any
		expected int
	}{
		{
			name: "empty paths",
			spec: map[string]any{
				"paths": map[string]any{},
			},
			expected: 0,
		},
		{
			name: "multiple paths",
			spec: map[string]any{
				"paths": map[string]any{
					"/users":    map[string]any{},
					"/products": map[string]any{},
					"/orders":   map[string]any{},
				},
			},
			expected: 3,
		},
		{
			name:     "no paths key",
			spec:     map[string]any{},
			expected: 0,
		},
		{
			name: "paths is not a map",
			spec: map[string]any{
				"paths": "invalid",
			},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := countPaths(tt.spec)
			if count != tt.expected {
				t.Errorf("expected %d paths, got %d", tt.expected, count)
			}
		})
	}
}

func TestTagOperations(t *testing.T) {
	pathItem := map[string]any{
		"get": map[string]any{
			"summary": "Get item",
			"tags":    []any{"existing"},
		},
		"post": map[string]any{
			"summary": "Create item",
		},
	}

	result := tagOperations(pathItem, "service-a")

	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}

	getOp, ok := resultMap["get"].(map[string]any)
	if !ok {
		t.Fatal("expected GET operation")
	}

	getTags, ok := getOp["tags"].([]any)
	if !ok {
		t.Fatal("expected tags array in GET")
	}

	hasServiceTag := false
	for _, tag := range getTags {
		if tag == "service-a" {
			hasServiceTag = true
			break
		}
	}

	if !hasServiceTag {
		t.Error("expected service-a tag in GET operation")
	}

	postOp, ok := resultMap["post"].(map[string]any)
	if !ok {
		t.Fatal("expected POST operation")
	}

	postTags, ok := postOp["tags"].([]any)
	if !ok {
		t.Fatal("expected tags array in POST")
	}

	hasServiceTagPost := false
	for _, tag := range postTags {
		if tag == "service-a" {
			hasServiceTagPost = true
			break
		}
	}

	if !hasServiceTagPost {
		t.Error("expected service-a tag in POST operation")
	}
}

func TestRewriteRefsWithMap(t *testing.T) {
	obj := map[string]any{
		"schema": map[string]any{
			"$ref": "#/components/schemas/User",
		},
		"items": map[string]any{
			"$ref": "#/components/schemas/Product",
		},
		"unrelated": map[string]any{
			"$ref": "#/components/schemas/Unknown",
		},
	}

	refMap := map[string]string{
		"#/components/schemas/User":    "#/components/schemas/service-a_User",
		"#/components/schemas/Product": "#/components/schemas/service-a_Product",
	}

	rewriteRefsWithMap(obj, refMap)

	// User ref should be rewritten
	schema, ok := obj["schema"].(map[string]any)
	if !ok {
		t.Fatal("expected schema map")
	}
	ref, ok := schema["$ref"].(string)
	if !ok {
		t.Fatal("expected $ref string")
	}
	if ref != "#/components/schemas/service-a_User" {
		t.Errorf("expected #/components/schemas/service-a_User, got %s", ref)
	}

	// Product ref should be rewritten
	items, ok := obj["items"].(map[string]any)
	if !ok {
		t.Fatal("expected items map")
	}
	ref2, ok := items["$ref"].(string)
	if !ok {
		t.Fatal("expected $ref string in items")
	}
	if ref2 != "#/components/schemas/service-a_Product" {
		t.Errorf("expected #/components/schemas/service-a_Product, got %s", ref2)
	}

	// Unknown ref should be LEFT UNCHANGED (not in map)
	unrelated, ok := obj["unrelated"].(map[string]any)
	if !ok {
		t.Fatal("expected unrelated map")
	}
	ref3, ok := unrelated["$ref"].(string)
	if !ok {
		t.Fatal("expected $ref string in unrelated")
	}
	if ref3 != "#/components/schemas/Unknown" {
		t.Errorf("expected #/components/schemas/Unknown (unchanged), got %s", ref3)
	}
}

func TestRewriteRefsWithMap_Nested(t *testing.T) {
	// Test that refs inside nested arrays and objects are also rewritten
	obj := map[string]any{
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type": "array",
					"items": map[string]any{
						"$ref": "#/components/schemas/Item",
					},
				},
			},
		},
		"responses": map[string]any{
			"200": map[string]any{
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"$ref": "#/components/schemas/Response",
						},
					},
				},
			},
		},
	}

	refMap := map[string]string{
		"#/components/schemas/Item":     "#/components/schemas/svc_Item",
		"#/components/schemas/Response": "#/components/schemas/svc_Response",
	}

	rewriteRefsWithMap(obj, refMap)

	// Check nested item ref
	items := obj["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["items"].(map[string]any)
	if items["$ref"] != "#/components/schemas/svc_Item" {
		t.Errorf("expected nested ref rewrite, got %s", items["$ref"])
	}

	// Check deeply nested response ref
	respSchema := obj["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if respSchema["$ref"] != "#/components/schemas/svc_Response" {
		t.Errorf("expected nested response ref rewrite, got %s", respSchema["$ref"])
	}
}

func TestRewriteRefsWithMap_EmptyMap(t *testing.T) {
	obj := map[string]any{
		"schema": map[string]any{
			"$ref": "#/components/schemas/User",
		},
	}

	// Empty refMap should leave everything unchanged
	rewriteRefsWithMap(obj, map[string]string{})

	schema := obj["schema"].(map[string]any)
	if schema["$ref"] != "#/components/schemas/User" {
		t.Errorf("expected unchanged ref with empty map, got %s", schema["$ref"])
	}
}

func TestSanitizeOperations(t *testing.T) {
	pathItem := map[string]any{
		"get": map[string]any{
			"summary": "List items",
			"requestBody": map[string]any{
				"content": map[string]any{},
			},
		},
		"post": map[string]any{
			"summary": "Create item",
			"requestBody": map[string]any{
				"content": map[string]any{},
			},
		},
		"delete": map[string]any{
			"summary": "Delete item",
			"requestBody": map[string]any{
				"content": map[string]any{},
			},
		},
		"head": map[string]any{
			"summary": "Check item",
			"requestBody": map[string]any{
				"content": map[string]any{},
			},
		},
		"put": map[string]any{
			"summary": "Update item",
			"requestBody": map[string]any{
				"content": map[string]any{},
			},
		},
	}

	result := sanitizeOperations(pathItem)
	resultMap := result.(map[string]any)

	// GET, HEAD, DELETE should have requestBody stripped
	for _, method := range []string{"get", "head", "delete"} {
		op := resultMap[method].(map[string]any)
		if _, hasBody := op["requestBody"]; hasBody {
			t.Errorf("%s should not have requestBody", method)
		}
		if _, hasSummary := op["summary"]; !hasSummary {
			t.Errorf("%s should still have summary", method)
		}
	}

	// POST and PUT should keep requestBody
	for _, method := range []string{"post", "put"} {
		op := resultMap[method].(map[string]any)
		if _, hasBody := op["requestBody"]; !hasBody {
			t.Errorf("%s should keep requestBody", method)
		}
	}
}

// --- Mock RouteRegistry for tests ---

type mockRouteRegistry struct {
	routes              []*Route
	removedServiceNames []string // tracks RemoveByServiceName calls
}

func (m *mockRouteRegistry) AddRoute(route *Route) error {
	m.routes = append(m.routes, route)
	return nil
}

func (m *mockRouteRegistry) RemoveRoute(id string) error {
	for i, r := range m.routes {
		if r.ID == id {
			m.routes = append(m.routes[:i], m.routes[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockRouteRegistry) UpdateRoute(route *Route) error {
	for i, r := range m.routes {
		if r.ID == route.ID {
			m.routes[i] = route
			return nil
		}
	}
	return nil
}

func (m *mockRouteRegistry) GetRoute(id string) (*Route, bool) {
	for _, r := range m.routes {
		if r.ID == id {
			return r, true
		}
	}
	return nil, false
}

func (m *mockRouteRegistry) RemoveByServiceName(serviceName string) {
	m.removedServiceNames = append(m.removedServiceNames, serviceName)
	filtered := make([]*Route, 0, len(m.routes))
	for _, r := range m.routes {
		if r.ServiceName != serviceName {
			filtered = append(filtered, r)
		}
	}
	m.routes = filtered
}

func (m *mockRouteRegistry) ListRoutes() []*Route {
	result := make([]*Route, len(m.routes))
	copy(result, m.routes)
	return result
}

func TestAggregator_BuildMergedSpec_WithExtensionFilter(t *testing.T) {
	config := DefaultOpenAPIConfig()
	config.Title = "Test Gateway"
	config.Version = "1.0.0"
	config.MergeStrategy = "prefix"
	config.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:       "twinos",
			KnownExtensions:   []string{"cortex", "authsome", "webhooks", "dispatch"},
			AllowedExtensions: []string{"authsome"},
		},
	}
	logger := newTestLogger()
	rm := &mockRouteRegistry{}

	aggr := NewAggregator(config, logger, rm, nil)

	specs := map[string]*ServiceOpenAPISpec{
		"twinos": {
			ServiceName: "twinos",
			Version:     "1.0.0",
			Healthy:     true,
			Spec: map[string]any{
				"openapi": "3.1.0",
				"paths": map[string]any{
					"/users":                  map[string]any{"get": map[string]any{"summary": "List users"}},
					"/cortex/agents":          map[string]any{"get": map[string]any{"summary": "List agents"}},
					"/cortex/agents/{name}":   map[string]any{"get": map[string]any{"summary": "Get agent"}},
					"/authsome/permissions":   map[string]any{"get": map[string]any{"summary": "List permissions"}},
					"/webhooks/subscriptions": map[string]any{"get": map[string]any{"summary": "List subscriptions"}},
					"/api/dispatch/v1/jobs":   map[string]any{"get": map[string]any{"summary": "List jobs"}},
					"/api/dispatch/v1/crons":  map[string]any{"get": map[string]any{"summary": "List crons"}},
					"/api/v1/formulas":        map[string]any{"get": map[string]any{"summary": "List formulas"}},
					"/v1/auth/signin":         map[string]any{"post": map[string]any{"summary": "Sign in"}},
				},
			},
		},
	}

	merged := aggr.buildMergedSpec(specs)

	paths, ok := merged["paths"].(map[string]any)
	if !ok {
		t.Fatal("expected paths object")
	}

	// Native service paths should be included
	if _, ok := paths["/twinos/users"]; !ok {
		t.Error("expected /twinos/users path (native service path)")
	}
	if _, ok := paths["/twinos/api/v1/formulas"]; !ok {
		t.Error("expected /twinos/api/v1/formulas path (native API path)")
	}
	if _, ok := paths["/twinos/v1/auth/signin"]; !ok {
		t.Error("expected /twinos/v1/auth/signin path (authsome uses /v1/auth, no segment matches)")
	}

	// Allowed extension should be included
	if _, ok := paths["/twinos/authsome/permissions"]; !ok {
		t.Error("expected /twinos/authsome/permissions path (allowed extension)")
	}

	// Excluded extensions (first-segment match) should NOT be included
	if _, ok := paths["/twinos/cortex/agents"]; ok {
		t.Error("did not expect /twinos/cortex/agents path (excluded extension)")
	}
	if _, ok := paths["/twinos/cortex/agents/{name}"]; ok {
		t.Error("did not expect /twinos/cortex/agents/{name} path (excluded extension)")
	}
	if _, ok := paths["/twinos/webhooks/subscriptions"]; ok {
		t.Error("did not expect /twinos/webhooks/subscriptions path (excluded extension)")
	}

	// Excluded extensions (nested/any-segment match) should NOT be included
	if _, ok := paths["/twinos/api/dispatch/v1/jobs"]; ok {
		t.Error("did not expect /twinos/api/dispatch/v1/jobs path (dispatch at segment 2)")
	}
	if _, ok := paths["/twinos/api/dispatch/v1/crons"]; ok {
		t.Error("did not expect /twinos/api/dispatch/v1/crons path (dispatch at segment 2)")
	}
}

func TestAggregator_BuildMergedSpec_WithExcludePrefixes(t *testing.T) {
	config := DefaultOpenAPIConfig()
	config.MergeStrategy = "prefix"
	config.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:     "twinos",
			KnownExtensions: []string{"cortex"},
			ExcludePrefixes: []string{"/v1/admin", "/v1/instances"},
		},
	}
	logger := newTestLogger()
	rm := &mockRouteRegistry{}

	aggr := NewAggregator(config, logger, rm, nil)

	specs := map[string]*ServiceOpenAPISpec{
		"twinos": {
			ServiceName: "twinos",
			Healthy:     true,
			Spec: map[string]any{
				"openapi": "3.1.0",
				"paths": map[string]any{
					"/api/v1/formulas":  map[string]any{"get": map[string]any{"summary": "List formulas"}},
					"/v1/admin/tenants": map[string]any{"get": map[string]any{"summary": "List tenants"}},
					"/v1/instances":     map[string]any{"get": map[string]any{"summary": "List instances"}},
					"/cortex/agents":    map[string]any{"get": map[string]any{"summary": "List agents"}},
				},
			},
		},
	}

	merged := aggr.buildMergedSpec(specs)
	paths, _ := merged["paths"].(map[string]any)

	// Native path should be included
	if _, ok := paths["/twinos/api/v1/formulas"]; !ok {
		t.Error("expected /twinos/api/v1/formulas path")
	}

	// Exclude-prefix paths should be excluded
	if _, ok := paths["/twinos/v1/admin/tenants"]; ok {
		t.Error("did not expect /twinos/v1/admin/tenants (exclude prefix)")
	}
	if _, ok := paths["/twinos/v1/instances"]; ok {
		t.Error("did not expect /twinos/v1/instances (exclude prefix)")
	}

	// Known extension should also be excluded
	if _, ok := paths["/twinos/cortex/agents"]; ok {
		t.Error("did not expect /twinos/cortex/agents (known extension)")
	}
}

func TestAggregator_BuildMergedSpec_NoFilterDoesNotAffectPaths(t *testing.T) {
	config := DefaultOpenAPIConfig()
	config.MergeStrategy = "prefix"
	// No extension filters configured
	logger := newTestLogger()
	rm := &mockRouteRegistry{}

	aggr := NewAggregator(config, logger, rm, nil)

	specs := map[string]*ServiceOpenAPISpec{
		"twinos": {
			ServiceName: "twinos",
			Healthy:     true,
			Spec: map[string]any{
				"openapi": "3.1.0",
				"paths": map[string]any{
					"/users":         map[string]any{"get": map[string]any{"summary": "List users"}},
					"/cortex/agents": map[string]any{"get": map[string]any{"summary": "List agents"}},
				},
			},
		},
	}

	merged := aggr.buildMergedSpec(specs)

	paths, ok := merged["paths"].(map[string]any)
	if !ok {
		t.Fatal("expected paths object")
	}

	if _, ok := paths["/twinos/users"]; !ok {
		t.Error("expected /twinos/users path")
	}

	if _, ok := paths["/twinos/cortex/agents"]; !ok {
		t.Error("expected /twinos/cortex/agents path (no filter configured)")
	}
}

func TestSplitPathSegments(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := splitPathSegments(tt.path)
			if len(got) != len(tt.expected) {
				t.Fatalf("splitPathSegments(%q) = %v, want %v", tt.path, got, tt.expected)
			}
			for i := range got {
				if got[i] != tt.expected[i] {
					t.Errorf("splitPathSegments(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.expected[i])
				}
			}
		})
	}
}
