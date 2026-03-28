package plugin

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	bastion "github.com/xraph/bastion"
)

func TestGraphQLGuard_DepthLimit(t *testing.T) {
	guard := NewGraphQLGuard(GraphQLConfig{MaxDepth: 3})

	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:    "within limit",
			query:   `{ users { name } }`,
			wantErr: false,
		},
		{
			name:    "exactly at limit",
			query:   `{ users { posts { title } } }`,
			wantErr: false,
		},
		{
			name:    "exceeds limit",
			query:   `{ users { posts { comments { text } } } }`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(graphQLRequest{Query: tt.query})
			req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			err := guard.Check(req)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

func TestGraphQLGuard_BlockIntrospection(t *testing.T) {
	guard := NewGraphQLGuard(GraphQLConfig{BlockIntrospection: true})

	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:    "normal query",
			query:   `{ users { name } }`,
			wantErr: false,
		},
		{
			name:    "__schema introspection",
			query:   `{ __schema { types { name } } }`,
			wantErr: true,
		},
		{
			name:    "__type introspection",
			query:   `{ __type(name: "User") { fields { name } } }`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(graphQLRequest{Query: tt.query})
			req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))

			err := guard.Check(req)
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGraphQLGuard_AllowedOperations(t *testing.T) {
	guard := NewGraphQLGuard(GraphQLConfig{
		AllowedOperations: []string{"query"},
	})

	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:    "query allowed",
			query:   `query { users { name } }`,
			wantErr: false,
		},
		{
			name:    "mutation blocked",
			query:   `mutation { createUser(name: "test") { id } }`,
			wantErr: true,
		},
		{
			name:    "shorthand query allowed",
			query:   `{ users { name } }`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(graphQLRequest{Query: tt.query})
			req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))

			err := guard.Check(req)
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGraphQLGuard_ComplexityLimit(t *testing.T) {
	guard := NewGraphQLGuard(GraphQLConfig{MaxComplexity: 5})

	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:    "simple query within limit",
			query:   `{ users { name } }`,
			wantErr: false,
		},
		{
			name:    "complex query exceeds limit",
			query:   `{ users { name email posts { title body comments { text author } } } }`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(graphQLRequest{Query: tt.query})
			req := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))

			err := guard.Check(req)
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestGraphQLGuard_GETRequest(t *testing.T) {
	guard := NewGraphQLGuard(GraphQLConfig{MaxDepth: 1, BlockIntrospection: true})

	req := httptest.NewRequest("GET", "/graphql?query={__schema{types{name}}}", nil)
	err := guard.Check(req)

	if err != nil {
		t.Error("GET requests should be skipped")
	}
}

func TestMeasureDepth(t *testing.T) {
	tests := []struct {
		query    string
		expected int
	}{
		{`{ a }`, 1},
		{`{ a { b } }`, 2},
		{`{ a { b { c } } }`, 3},
		{`{ a { b } c { d { e } } }`, 3},
		{``, 0},
	}

	for _, tt := range tests {
		depth := measureDepth(tt.query)
		if depth != tt.expected {
			t.Errorf("measureDepth(%q) = %d, want %d", tt.query, depth, tt.expected)
		}
	}
}

func TestDetectOperationType(t *testing.T) {
	tests := []struct {
		query    string
		expected string
	}{
		{`query { users }`, "query"},
		{`mutation { createUser }`, "mutation"},
		{`subscription { onMessage }`, "subscription"},
		{`{ users }`, "query"},
		{`  query GetUsers { users }`, "query"},
	}

	for _, tt := range tests {
		result := detectOperationType(tt.query)
		if result != tt.expected {
			t.Errorf("detectOperationType(%q) = %q, want %q", tt.query, result, tt.expected)
		}
	}
}

func TestGraphQLPlugin_OnRequest(t *testing.T) {
	p := NewGraphQLPlugin(GraphQLConfig{
		MaxDepth:           3,
		BlockIntrospection: true,
	})

	// Non-GraphQL route should pass through
	route := &bastion.Route{ID: "r1", Protocol: bastion.ProtocolHTTP}
	req := httptest.NewRequest("POST", "/api", nil)

	if err := p.OnRequest(req, route); err != nil {
		t.Errorf("non-graphql route should pass: %v", err)
	}

	// GraphQL route with deep query should fail
	gqlRoute := &bastion.Route{ID: "r2", Protocol: bastion.ProtocolGraphQL}
	body, _ := json.Marshal(graphQLRequest{
		Query: `{ a { b { c { d } } } }`,
	})
	gqlReq := httptest.NewRequest("POST", "/graphql", bytes.NewReader(body))

	if err := p.OnRequest(gqlReq, gqlRoute); err == nil {
		t.Error("expected depth limit error for GraphQL route")
	}
}
