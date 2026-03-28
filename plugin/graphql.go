package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode"

	bastion "github.com/xraph/bastion"
)

// GraphQLConfig configures GraphQL-specific gateway behaviour.
type GraphQLConfig struct {
	// MaxDepth limits query nesting depth (0 = unlimited).
	MaxDepth int `json:"maxDepth"`

	// MaxComplexity limits estimated query complexity (0 = unlimited).
	MaxComplexity int `json:"maxComplexity"`

	// BlockIntrospection rejects __schema and __type queries.
	BlockIntrospection bool `json:"blockIntrospection"`

	// AllowedOperations limits which operation types are permitted.
	// Empty = all allowed. Values: "query", "mutation", "subscription".
	AllowedOperations []string `json:"allowedOperations,omitempty"`
}

// graphQLRequest is the JSON body of a GraphQL HTTP request.
type graphQLRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables,omitempty"`
}

// GraphQLGuard enforces depth limits, complexity limits, and introspection
// blocking on GraphQL routes.
type GraphQLGuard struct {
	config GraphQLConfig
}

// NewGraphQLGuard creates a guard with the given configuration.
func NewGraphQLGuard(cfg GraphQLConfig) *GraphQLGuard {
	return &GraphQLGuard{config: cfg}
}

// Check validates a GraphQL HTTP request against the configured rules.
// It returns an error string suitable for a JSON error response, or "" if OK.
func (g *GraphQLGuard) Check(r *http.Request) error {
	if r.Method != http.MethodPost {
		return nil // GET queries have limited depth; skip enforcement
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var gqlReq graphQLRequest
	if err := json.Unmarshal(body, &gqlReq); err != nil {
		return nil // not valid GraphQL JSON — let upstream handle it
	}

	query := gqlReq.Query
	if query == "" {
		return nil
	}

	// Check introspection
	if g.config.BlockIntrospection && containsIntrospection(query) {
		return errors.New("introspection queries are not allowed")
	}

	// Check allowed operations
	if len(g.config.AllowedOperations) > 0 {
		opType := detectOperationType(query)
		if opType != "" && !containsString(g.config.AllowedOperations, opType) {
			return fmt.Errorf("operation type %q is not allowed", opType)
		}
	}

	// Check depth
	if g.config.MaxDepth > 0 {
		depth := measureDepth(query)
		if depth > g.config.MaxDepth {
			return fmt.Errorf("query depth %d exceeds maximum allowed depth of %d", depth, g.config.MaxDepth)
		}
	}

	// Check complexity
	if g.config.MaxComplexity > 0 {
		complexity := estimateComplexity(query)
		if complexity > g.config.MaxComplexity {
			return fmt.Errorf("query complexity %d exceeds maximum allowed complexity of %d", complexity, g.config.MaxComplexity)
		}
	}

	return nil
}

// containsIntrospection checks if the query contains __schema or __type.
func containsIntrospection(query string) bool {
	lower := strings.ToLower(query)
	return strings.Contains(lower, "__schema") || strings.Contains(lower, "__type")
}

// detectOperationType returns "query", "mutation", or "subscription".
func detectOperationType(query string) string {
	trimmed := strings.TrimLeftFunc(query, unicode.IsSpace)

	// Shorthand query (no keyword)
	if strings.HasPrefix(trimmed, "{") {
		return "query"
	}

	lower := strings.ToLower(trimmed)
	for _, op := range []string{"mutation", "subscription", "query"} {
		if strings.HasPrefix(lower, op) {
			return op
		}
	}

	return ""
}

// measureDepth counts the maximum nesting depth by tracking braces.
// It ignores braces inside strings.
func measureDepth(query string) int {
	maxDepth := 0
	current := 0
	inString := false

	for i := 0; i < len(query); i++ {
		ch := query[i]

		if ch == '"' && (i == 0 || query[i-1] != '\\') {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch ch {
		case '{':
			current++
			if current > maxDepth {
				maxDepth = current
			}
		case '}':
			if current > 0 {
				current--
			}
		}
	}

	return maxDepth
}

// estimateComplexity provides a simple heuristic: count the number of
// field selections (non-brace, non-argument tokens at each level).
// Each opening brace adds a multiplier for nested selections.
func estimateComplexity(query string) int {
	// Simple heuristic: count fields (alphanumeric tokens not preceded by
	// keywords). Each field at depth N contributes N to complexity.
	complexity := 0
	depth := 0
	inString := false
	i := 0

	for i < len(query) {
		ch := query[i]

		if ch == '"' && (i == 0 || query[i-1] != '\\') {
			inString = !inString
			i++
			continue
		}

		if inString {
			i++
			continue
		}

		switch ch {
		case '{':
			depth++
			i++
		case '}':
			if depth > 0 {
				depth--
			}
			i++
		case '(':
			// Skip arguments block entirely
			parenDepth := 1
			i++
			for i < len(query) && parenDepth > 0 {
				switch query[i] {
				case '(':
					parenDepth++
				case ')':
					parenDepth--
				}
				i++
			}
		default:
			if isFieldStart(ch) {
				// Read the identifier
				start := i
				for i < len(query) && isIdentChar(query[i]) {
					i++
				}
				word := query[start:i]
				// Skip GraphQL keywords
				if !isGraphQLKeyword(word) && depth > 0 {
					complexity += depth
				}
			} else {
				i++
			}
		}
	}

	return complexity
}

func isFieldStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isIdentChar(ch byte) bool {
	return isFieldStart(ch) || (ch >= '0' && ch <= '9')
}

var graphQLKeywords = map[string]bool{
	"query": true, "mutation": true, "subscription": true,
	"fragment": true, "on": true, "true": true, "false": true, "null": true,
}

func isGraphQLKeyword(word string) bool {
	return graphQLKeywords[strings.ToLower(word)]
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if strings.EqualFold(v, s) {
			return true
		}
	}

	return false
}

// GraphQLPlugin is a GatewayPlugin that enforces GraphQL rules on
// routes with Protocol == ProtocolGraphQL.
type GraphQLPlugin struct {
	bastion.BasePlugin
	mu     sync.RWMutex
	guards map[string]*GraphQLGuard // routeID -> guard
	global *GraphQLGuard
}

// NewGraphQLPlugin creates a plugin with a global GraphQL config.
// Per-route configs can be set via SetRouteConfig.
func NewGraphQLPlugin(cfg GraphQLConfig) *GraphQLPlugin {
	return &GraphQLPlugin{
		BasePlugin: bastion.BasePlugin{PluginName: "graphql"},
		guards:     make(map[string]*GraphQLGuard),
		global:     NewGraphQLGuard(cfg),
	}
}

// SetRouteConfig sets a per-route GraphQL configuration.
func (p *GraphQLPlugin) SetRouteConfig(routeID string, cfg GraphQLConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.guards[routeID] = NewGraphQLGuard(cfg)
}

// OnRequest validates GraphQL requests on GraphQL-protocol routes.
func (p *GraphQLPlugin) OnRequest(r *http.Request, route *bastion.Route) error {
	if route.Protocol != bastion.ProtocolGraphQL {
		return nil
	}

	p.mu.RLock()
	guard, ok := p.guards[route.ID]
	p.mu.RUnlock()

	if !ok {
		guard = p.global
	}

	return guard.Check(r)
}
