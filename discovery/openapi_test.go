package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xraph/farp/merger"
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

	fetched := aggr.schemaFetcher.FetchFromURL(context.Background(), server.URL, "test-service")

	if fetched == nil {
		t.Fatal("expected spec, got nil")
	}

	if !fetched.Healthy {
		t.Error("expected healthy spec")
	}

	if fetched.Error != "" {
		t.Errorf("unexpected error: %s", fetched.Error)
	}

	if fetched.PathCount != 1 {
		t.Errorf("expected 1 path, got %d", fetched.PathCount)
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

	fetched := aggr.schemaFetcher.FetchFromURL(context.Background(), server.URL, "test-service")

	if fetched == nil {
		t.Fatal("expected spec result, got nil")
	}

	if fetched.Healthy {
		t.Error("expected unhealthy spec for 404")
	}

	if fetched.Error == "" {
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

	fetched := aggr.schemaFetcher.FetchFromURL(context.Background(), server.URL, "test-service")

	if fetched == nil {
		t.Fatal("expected spec result, got nil")
	}

	if fetched.Healthy {
		t.Error("expected unhealthy spec for invalid JSON")
	}

	if fetched.Error == "" {
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

func TestAggregator_BuildMergedSpec_DisableServiceTags(t *testing.T) {
	config := DefaultOpenAPIConfig()
	config.Title = "Test Gateway"
	config.Version = "1.0.0"
	config.MergeStrategy = "prefix"
	config.DisableServiceTags = true
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
							"tags":    []any{"existing-tag"},
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

	// Top-level tags should be empty (no service tags created)
	tags, _ := merged["tags"].([]any)
	for _, tag := range tags {
		tagMap, ok := tag.(map[string]any)
		if !ok {
			continue
		}
		tagName, _ := tagMap["name"].(string)
		if tagName == "service-a" || tagName == "service-b" {
			t.Errorf("expected no service tag %q when DisableServiceTags is true", tagName)
		}
	}

	// Operations should not have service-name tags injected
	paths, ok := merged["paths"].(map[string]any)
	if !ok {
		t.Fatal("expected paths object")
	}

	for pathStr, pathItem := range paths {
		pathItemMap, ok := pathItem.(map[string]any)
		if !ok {
			continue
		}
		for _, method := range []string{"get", "post", "put", "delete", "patch"} {
			op, ok := pathItemMap[method]
			if !ok {
				continue
			}
			opMap, ok := op.(map[string]any)
			if !ok {
				continue
			}
			opTags, _ := opMap["tags"].([]any)
			for _, tag := range opTags {
				tagStr, _ := tag.(string)
				if tagStr == "service-a" || tagStr == "service-b" {
					t.Errorf("operation %s %s should not have service tag %q when DisableServiceTags is true", method, pathStr, tagStr)
				}
			}
		}
	}

	// Existing tags from upstream specs should be preserved
	usersPath, ok := paths["/service-a/users"]
	if !ok {
		t.Fatal("expected /service-a/users path")
	}
	usersPathMap := usersPath.(map[string]any)
	getOp := usersPathMap["get"].(map[string]any)
	getTags, _ := getOp["tags"].([]any)
	foundExisting := false
	for _, tag := range getTags {
		if tag == "existing-tag" {
			foundExisting = true
		}
	}
	if !foundExisting {
		t.Error("expected existing-tag to be preserved on GET /service-a/users")
	}
}

func TestAggregator_BuildMergedSpec_ServiceTagOnly(t *testing.T) {
	config := DefaultOpenAPIConfig()
	config.Title = "Test Gateway"
	config.Version = "1.0.0"
	config.MergeStrategy = "prefix"
	config.ServiceTagOnly = true
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
							"tags":    []any{"users", "admin"},
						},
						"post": map[string]any{
							"summary": "Create user",
							"tags":    []any{"users"},
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
							"tags":    []any{"catalog"},
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

	// Top-level tags should only contain service tags
	tags, _ := merged["tags"].([]any)
	for _, tag := range tags {
		tagMap, ok := tag.(map[string]any)
		if !ok {
			continue
		}
		tagName, _ := tagMap["name"].(string)
		if tagName != "service-a" && tagName != "service-b" {
			t.Errorf("unexpected non-service tag %q in top-level tags", tagName)
		}
	}

	paths, ok := merged["paths"].(map[string]any)
	if !ok {
		t.Fatal("expected paths object")
	}

	// Each operation should have exactly one tag: the service name
	for pathStr, pathItem := range paths {
		pathItemMap, ok := pathItem.(map[string]any)
		if !ok {
			continue
		}
		for _, method := range []string{"get", "post", "put", "delete", "patch"} {
			op, ok := pathItemMap[method]
			if !ok {
				continue
			}
			opMap, ok := op.(map[string]any)
			if !ok {
				continue
			}
			opTags, _ := opMap["tags"].([]any)
			if len(opTags) != 1 {
				t.Errorf("operation %s %s: expected exactly 1 tag, got %d: %v", method, pathStr, len(opTags), opTags)
				continue
			}
			tagStr, _ := opTags[0].(string)
			if tagStr != "service-a" && tagStr != "service-b" {
				t.Errorf("operation %s %s: expected service tag, got %q", method, pathStr, tagStr)
			}
		}
	}

	// Upstream tags like "users", "admin", "catalog" should be gone
	usersPath := paths["/service-a/users"].(map[string]any)
	getOp := usersPath["get"].(map[string]any)
	getTags := getOp["tags"].([]any)
	for _, tag := range getTags {
		tagStr, _ := tag.(string)
		if tagStr == "users" || tagStr == "admin" {
			t.Errorf("upstream tag %q should have been stripped", tagStr)
		}
	}
}

func TestAggregator_Refresh_RetainsCachedSpecWhenNewHasFewerPaths(t *testing.T) {
	// Simulate a service that initially returns a full spec, then a partial one.
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var spec map[string]any
		if callCount == 1 {
			// Full spec with 3 paths
			spec = map[string]any{
				"openapi": "3.1.0",
				"info":    map[string]any{"title": "Full", "version": "1.0.0"},
				"paths": map[string]any{
					"/users":    map[string]any{"get": map[string]any{"summary": "List users"}},
					"/products": map[string]any{"get": map[string]any{"summary": "List products"}},
					"/orders":   map[string]any{"get": map[string]any{"summary": "List orders"}},
				},
			}
		} else {
			// Partial spec (service mid-restart)
			spec = map[string]any{
				"openapi": "3.1.0",
				"info":    map[string]any{"title": "Partial", "version": "1.0.0"},
				"paths": map[string]any{
					"/users": map[string]any{"get": map[string]any{"summary": "List users"}},
				},
			}
		}
		_ = json.NewEncoder(w).Encode(spec)
	}))
	defer server.Close()

	config := DefaultOpenAPIConfig()
	config.FetchTimeout = 5 * time.Second
	config.MergeStrategy = "flat"
	logger := newTestLogger()
	rm := &mockRouteRegistry{
		routes: []*Route{
			{
				ServiceName: "test-svc",
				Targets: []*Target{{
					URL:      server.URL,
					Metadata: map[string]string{"openapi": server.URL},
				}},
			},
		},
	}

	aggr := NewAggregator(config, logger, rm, nil)

	// First refresh — fetches full spec
	aggr.Refresh(context.Background())

	spec1 := aggr.MergedSpecMap()
	paths1, _ := spec1["paths"].(map[string]any)
	if len(paths1) != 3 {
		t.Fatalf("first refresh: expected 3 paths, got %d", len(paths1))
	}

	// Second refresh — server returns partial spec, but aggregator should retain cached
	aggr.Refresh(context.Background())

	spec2 := aggr.MergedSpecMap()
	paths2, _ := spec2["paths"].(map[string]any)
	if len(paths2) != 3 {
		t.Errorf("second refresh: expected 3 paths (retained), got %d", len(paths2))
	}
}

func TestAggregator_Refresh_RetainsCachedSpecOnFetchFailure(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			spec := map[string]any{
				"openapi": "3.1.0",
				"info":    map[string]any{"title": "OK", "version": "1.0.0"},
				"paths": map[string]any{
					"/users": map[string]any{"get": map[string]any{"summary": "List users"}},
				},
			}
			_ = json.NewEncoder(w).Encode(spec)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	config := DefaultOpenAPIConfig()
	config.FetchTimeout = 5 * time.Second
	config.MergeStrategy = "flat"
	logger := newTestLogger()
	rm := &mockRouteRegistry{
		routes: []*Route{
			{
				ServiceName: "test-svc",
				Targets: []*Target{{
					URL:      server.URL,
					Metadata: map[string]string{"openapi": server.URL},
				}},
			},
		},
	}

	aggr := NewAggregator(config, logger, rm, nil)

	// First refresh — success
	aggr.Refresh(context.Background())

	spec1 := aggr.MergedSpecMap()
	paths1, _ := spec1["paths"].(map[string]any)
	if len(paths1) != 1 {
		t.Fatalf("first refresh: expected 1 path, got %d", len(paths1))
	}

	// Second refresh — server returns 503, aggregator should retain cached spec
	aggr.Refresh(context.Background())

	spec2 := aggr.MergedSpecMap()
	paths2, _ := spec2["paths"].(map[string]any)
	if len(paths2) != 1 {
		t.Errorf("second refresh: expected 1 path (retained), got %d", len(paths2))
	}
}

// The merger routes paths through the service manifest but copies every
// component the upstream declared, so a service that mounts routes the gateway
// does not aggregate (portal's /relay, /dashboard, /herald) leaves its schemas
// behind with nothing pointing at them. These guard that the published document
// only carries schemas something can actually reach, and -- more importantly --
// that the walk treats every part of the document except components.schemas as
// a root, so pruning can never remove a schema that is still referenced.

func TestPruneUnreferencedSchemas_DropsOrphansKeepsReachable(t *testing.T) {
	merged := map[string]any{
		"paths": map[string]any{
			"/users": map[string]any{
				"get": map[string]any{
					"responses": map[string]any{
						"200": map[string]any{
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"$ref": "#/components/schemas/User",
									},
								},
							},
						},
					},
				},
			},
		},
		"components": map[string]any{
			"schemas": map[string]any{
				// Reached from the path, and pulls Address in transitively.
				"User": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"address": map[string]any{"$ref": "#/components/schemas/Address"},
					},
				},
				"Address": map[string]any{"type": "object"},
				// Declared by an upstream whose routes were never aggregated.
				"RelayEvent": map[string]any{"type": "object"},
				// An orphan referencing a live schema must not keep itself alive.
				"OrphanHolder": map[string]any{
					"properties": map[string]any{
						"other": map[string]any{"$ref": "#/components/schemas/AlsoOrphan"},
					},
				},
				"AlsoOrphan": map[string]any{"type": "object"},
			},
		},
	}

	removed := pruneUnreferencedSchemas(merged)

	if removed != 3 {
		t.Fatalf("expected 3 schemas pruned, got %d", removed)
	}

	schemas := merged["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"User", "Address"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("%s is reachable and must survive the prune", name)
		}
	}
	for _, name := range []string{"RelayEvent", "OrphanHolder", "AlsoOrphan"} {
		if _, ok := schemas[name]; ok {
			t.Errorf("%s is unreachable and must be pruned", name)
		}
	}
}

func TestPruneUnreferencedSchemas_NonSchemaComponentsAreRoots(t *testing.T) {
	// A shared response, parameter, requestBody or securityScheme is retained
	// wholesale, so anything it points at is still live even with no path
	// mentioning it. Walking only paths would delete these and publish a
	// document whose own components dangle.
	merged := map[string]any{
		"paths": map[string]any{},
		"components": map[string]any{
			"responses": map[string]any{
				"NotFound": map[string]any{
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": map[string]any{"$ref": "#/components/schemas/Problem"},
						},
					},
				},
			},
			"parameters": map[string]any{
				"PageParam": map[string]any{
					"schema": map[string]any{"$ref": "#/components/schemas/Page"},
				},
			},
			"schemas": map[string]any{
				"Problem": map[string]any{"type": "object"},
				"Page":    map[string]any{"type": "integer"},
				"Unused":  map[string]any{"type": "object"},
			},
		},
	}

	if removed := pruneUnreferencedSchemas(merged); removed != 1 {
		t.Fatalf("expected only Unused pruned, got %d removed", removed)
	}

	schemas := merged["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := schemas["Problem"]; !ok {
		t.Error("Problem is held by a retained response and must survive")
	}
	if _, ok := schemas["Page"]; !ok {
		t.Error("Page is held by a retained parameter and must survive")
	}
}

func TestPruneUnreferencedSchemas_FollowsDiscriminatorMapping(t *testing.T) {
	// A oneOf discriminator names its variants in a mapping whose values are
	// refs. They are the only pointer to those variants, so a walk that only
	// understands "$ref" keys would prune a schema the document still resolves.
	merged := map[string]any{
		"paths": map[string]any{
			"/pets": map[string]any{
				"get": map[string]any{
					"responses": map[string]any{
						"200": map[string]any{
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{"$ref": "#/components/schemas/Pet"},
								},
							},
						},
					},
				},
			},
		},
		"components": map[string]any{
			"schemas": map[string]any{
				"Pet": map[string]any{
					"discriminator": map[string]any{
						"propertyName": "kind",
						"mapping": map[string]any{
							"cat": "#/components/schemas/Cat",
							"dog": "Dog",
						},
					},
				},
				"Cat":   map[string]any{"type": "object"},
				"Dog":   map[string]any{"type": "object"},
				"Gecko": map[string]any{"type": "object"},
			},
		},
	}

	if removed := pruneUnreferencedSchemas(merged); removed != 1 {
		t.Fatalf("expected only Gecko pruned, got %d removed", removed)
	}

	schemas := merged["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"Cat", "Dog"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("%s is named by the discriminator mapping and must survive", name)
		}
	}
}

func TestPruneUnreferencedSchemas_ToleratesMissingSections(t *testing.T) {
	// Refresh can hand this a spec built before any service reported, and a
	// prune that panicked on an absent components block would take the whole
	// gateway's /openapi.json down with it.
	for name, merged := range map[string]map[string]any{
		"no components": {"paths": map[string]any{}},
		"no schemas":    {"paths": map[string]any{}, "components": map[string]any{}},
		"empty":         {},
	} {
		if removed := pruneUnreferencedSchemas(merged); removed != 0 {
			t.Errorf("%s: expected 0 removed, got %d", name, removed)
		}
	}
}

func TestPruneUnreferencedSchemas_WalksTypedMergerStructs(t *testing.T) {
	// pathItemToMap does not flatten the document: it stores op.Responses,
	// op.RequestBody and op.Parameters as the merger's own Go types, and only
	// the innermost Schema is a map[string]any. A walk that understands
	// map[string]any and []any alone therefore reaches no $ref at all and
	// prunes every schema in the document while the paths still reference them.
	// This builds the merged map the way buildMergedSpec does, through the real
	// pathItemToMap, so the walk has to cross those types to pass.
	item := merger.PathItem{
		Get: &merger.Operation{
			OperationID: "listUsers",
			Parameters: []merger.Parameter{{
				Name:   "page",
				In:     "query",
				Schema: map[string]any{"$ref": "#/components/schemas/Page"},
			}},
			Responses: map[string]merger.Response{
				"200": {
					Description: "ok",
					Content: map[string]merger.MediaType{
						"application/json": {
							Schema: map[string]any{"$ref": "#/components/schemas/User"},
						},
					},
				},
			},
		},
		Post: &merger.Operation{
			OperationID: "createUser",
			RequestBody: &merger.RequestBody{
				Content: map[string]merger.MediaType{
					"application/json": {
						Schema: map[string]any{"$ref": "#/components/schemas/NewUser"},
					},
				},
			},
		},
	}

	merged := map[string]any{
		"paths": map[string]any{"/users": pathItemToMap(item)},
		"components": map[string]any{
			"schemas": map[string]any{
				"User":    map[string]any{"type": "object"},
				"NewUser": map[string]any{"type": "object"},
				"Page":    map[string]any{"type": "integer"},
				"Orphan":  map[string]any{"type": "object"},
			},
		},
	}

	if removed := pruneUnreferencedSchemas(merged); removed != 1 {
		t.Fatalf("expected only Orphan pruned, got %d removed", removed)
	}

	schemas := merged["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"User", "NewUser", "Page"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("%s is referenced through the merger's typed structs and must survive", name)
		}
	}
}
