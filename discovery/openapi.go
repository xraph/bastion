package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xraph/forge"
)

// OpenAPIAggregator fetches, caches, and merges OpenAPI specs from all
// discovered upstream services (via FARP metadata). It exposes:
//   - A unified merged OpenAPI spec combining all service paths under gateway prefixes
//   - Per-service OpenAPI specs fetched and cached from upstream endpoints
//   - A Swagger UI for browsing the aggregated spec
//
// Schema fetching uses the `farp.openapi` metadata key set by the discovery
// extension's SchemaPublisher. The aggregator periodically refreshes specs
// and supports on-demand refresh.
type OpenAPIAggregator struct {
	config     OpenAPIConfig
	logger     forge.Logger
	rm         RouteRegistry
	disc       *Manager
	httpClient *http.Client

	mu             sync.RWMutex
	serviceSpecs   map[string]*ServiceOpenAPISpec // serviceName -> spec
	mergedSpec     map[string]any                 // cached merged spec
	mergedSpecJSON []byte                         // pre-serialized JSON
	lastRefresh    time.Time
	refreshing     bool

	refreshCh chan struct{} // signal channel for debounced reactive refresh
}

// NewAggregator creates a new OpenAPI aggregator.
func NewAggregator(config OpenAPIConfig, logger forge.Logger, rm RouteRegistry, disc *Manager) *OpenAPIAggregator {
	return &OpenAPIAggregator{
		config: config,
		logger: logger,
		rm:     rm,
		disc:   disc,
		httpClient: &http.Client{
			Timeout: config.FetchTimeout,
		},
		serviceSpecs: make(map[string]*ServiceOpenAPISpec),
		refreshCh:    make(chan struct{}, 1),
	}
}

// Start begins the periodic spec refresh loop and subscribes to service
// change events for reactive refresh.
func (oa *OpenAPIAggregator) Start(ctx context.Context) {
	if !oa.config.Enabled {
		return
	}

	// Subscribe to service changes for reactive refresh with debouncing.
	// When services push-register to the gateway, this triggers an OpenAPI
	// spec refresh so /docs updates within ~1 second instead of waiting
	// for the periodic refresh interval.
	if oa.disc != nil {
		oa.disc.OnServiceChange(func(serviceName string, registered bool) {
			oa.logger.Debug("OpenAPI: service change detected, scheduling refresh",
				forge.F("service", serviceName),
				forge.F("registered", registered),
			)
			// Non-blocking send to debounce channel
			select {
			case oa.refreshCh <- struct{}{}:
			default:
				// Already pending
			}
		})
	}

	// Start debounce goroutine for reactive service-change refreshes
	go oa.debounceLoop(ctx)

	// Delayed initial refresh (2s) — allows pull-based discovery to populate
	// services before the first spec fetch. Push-based registrations will
	// trigger their own refresh via the debounce channel.
	go func() {
		select {
		case <-time.After(2 * time.Second):
			oa.Refresh(ctx)
		case <-ctx.Done():
		}
	}()

	// Periodic refresh
	go func() {
		ticker := time.NewTicker(oa.config.RefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				oa.Refresh(ctx)
			}
		}
	}()

	oa.logger.Info("OpenAPI aggregator started",
		forge.F("path", oa.config.Path),
		forge.F("ui_path", oa.config.UIPath),
		forge.F("refresh_interval", oa.config.RefreshInterval),
	)
}

// debounceLoop coalesces rapid service-change signals into a single
// Refresh call, using a 500ms debounce window. This prevents
// overwhelming the upstream spec endpoints when multiple services
// register in quick succession during startup.
func (oa *OpenAPIAggregator) debounceLoop(ctx context.Context) {
	const debounceDelay = 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return
		case <-oa.refreshCh:
			// Wait for the debounce delay, resetting on each new signal
			timer := time.NewTimer(debounceDelay)
		drain:
			for {
				select {
				case <-oa.refreshCh:
					// More changes arrived, reset the timer
					if !timer.Stop() {
						<-timer.C
					}
					timer.Reset(debounceDelay)
				case <-timer.C:
					break drain
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}

			oa.Refresh(ctx)
		}
	}
}

// Refresh fetches all upstream OpenAPI specs and rebuilds the merged spec.
func (oa *OpenAPIAggregator) Refresh(ctx context.Context) {
	oa.mu.Lock()
	if oa.refreshing {
		oa.mu.Unlock()
		return
	}
	oa.refreshing = true
	oa.mu.Unlock()

	defer func() {
		oa.mu.Lock()
		oa.refreshing = false
		oa.mu.Unlock()
	}()

	// Discover services with OpenAPI endpoints
	services := oa.discoverOpenAPIServices()

	// Fetch specs in parallel
	var wg sync.WaitGroup
	specCh := make(chan *ServiceOpenAPISpec, len(services))

	for _, svc := range services {
		wg.Add(1)
		go func(s discoveredOpenAPIService) {
			defer wg.Done()
			spec := oa.fetchServiceSpec(ctx, s)
			specCh <- spec
		}(svc)
	}

	wg.Wait()
	close(specCh)

	// Collect results
	newSpecs := make(map[string]*ServiceOpenAPISpec)
	for spec := range specCh {
		newSpecs[spec.ServiceName] = spec
	}

	// Build merged spec
	merged := oa.buildMergedSpec(newSpecs)

	// Serialize to JSON
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		oa.logger.Error("failed to serialize merged OpenAPI spec", forge.F("error", err))
		return
	}

	// Atomically update
	oa.mu.Lock()
	oa.serviceSpecs = newSpecs
	oa.mergedSpec = merged
	oa.mergedSpecJSON = mergedJSON
	oa.lastRefresh = time.Now()
	oa.mu.Unlock()

	oa.logger.Debug("OpenAPI specs refreshed",
		forge.F("services", len(newSpecs)),
		forge.F("total_paths", countPaths(merged)),
	)
}

// MergedSpec returns the pre-serialized merged OpenAPI spec JSON.
func (oa *OpenAPIAggregator) MergedSpec() []byte {
	oa.mu.RLock()
	defer oa.mu.RUnlock()
	return oa.mergedSpecJSON
}

// MergedSpecMap returns the merged spec as a map.
func (oa *OpenAPIAggregator) MergedSpecMap() map[string]any {
	oa.mu.RLock()
	defer oa.mu.RUnlock()
	return oa.mergedSpec
}

// ServiceSpec returns the cached spec for a specific service.
func (oa *OpenAPIAggregator) ServiceSpec(serviceName string) *ServiceOpenAPISpec {
	oa.mu.RLock()
	defer oa.mu.RUnlock()
	return oa.serviceSpecs[serviceName]
}

// ServiceSpecs returns all cached service specs.
func (oa *OpenAPIAggregator) ServiceSpecs() map[string]*ServiceOpenAPISpec {
	oa.mu.RLock()
	defer oa.mu.RUnlock()

	// Return a copy
	result := make(map[string]*ServiceOpenAPISpec, len(oa.serviceSpecs))
	for k, v := range oa.serviceSpecs {
		result[k] = v
	}

	return result
}

// LastRefresh returns the time of the last spec refresh.
func (oa *OpenAPIAggregator) LastRefresh() time.Time {
	oa.mu.RLock()
	defer oa.mu.RUnlock()
	return oa.lastRefresh
}

// SpecPath returns the configured endpoint path for the aggregated OpenAPI spec.
func (oa *OpenAPIAggregator) SpecPath() string {
	return oa.config.Path
}

// UIPath returns the configured endpoint path for the Swagger UI.
func (oa *OpenAPIAggregator) UIPath() string {
	return oa.config.UIPath
}

// discoveredOpenAPIService holds info about a service with an OpenAPI endpoint.
type discoveredOpenAPIService struct {
	Name       string
	Version    string
	SpecURL    string
	RouteCount int
}

// discoverOpenAPIServices finds all services that expose OpenAPI specs.
func (oa *OpenAPIAggregator) discoverOpenAPIServices() []discoveredOpenAPIService {
	var services []discoveredOpenAPIService

	// Get discovered services from the discovery component
	if oa.disc != nil {
		for _, svc := range oa.disc.DiscoveredServices() {
			// Check if this service is excluded
			if oa.isExcluded(svc.Name) {
				continue
			}

			// Look for OpenAPI endpoint in metadata
			specURL := ""
			if url, ok := svc.Metadata["farp.openapi"]; ok && url != "" {
				specURL = url
			} else if path, ok := svc.Metadata["farp.openapi.path"]; ok && path != "" {
				// Build full URL from service address
				specURL = fmt.Sprintf("http://%s:%d%s", svc.Address, svc.Port, path)
			}

			if specURL == "" {
				continue
			}

			services = append(services, discoveredOpenAPIService{
				Name:       svc.Name,
				Version:    svc.Version,
				SpecURL:    specURL,
				RouteCount: svc.RouteCount,
			})
		}
	}

	// Also check route-level metadata for non-FARP services that still expose OpenAPI
	routes := oa.rm.ListRoutes()
	knownServices := make(map[string]bool)
	for _, s := range services {
		knownServices[s.Name] = true
	}

	for _, route := range routes {
		if route.ServiceName == "" || knownServices[route.ServiceName] {
			continue
		}

		if oa.isExcluded(route.ServiceName) {
			continue
		}

		// Check if any target has an OpenAPI endpoint
		for _, target := range route.Targets {
			if openapiURL, ok := target.Metadata["openapi"]; ok && openapiURL != "" {
				specURL := openapiURL
				// If it's a relative path, resolve against the target URL
				if !strings.HasPrefix(specURL, "http://") && !strings.HasPrefix(specURL, "https://") {
					specURL = target.URL + specURL
				}
				services = append(services, discoveredOpenAPIService{
					Name:    route.ServiceName,
					SpecURL: specURL,
				})
				knownServices[route.ServiceName] = true

				break
			}
		}
	}

	return services
}

// fetchServiceSpec fetches the OpenAPI spec from a single upstream service.
func (oa *OpenAPIAggregator) fetchServiceSpec(ctx context.Context, svc discoveredOpenAPIService) *ServiceOpenAPISpec {
	result := &ServiceOpenAPISpec{
		ServiceName: svc.Name,
		Version:     svc.Version,
		SpecURL:     svc.SpecURL,
		FetchedAt:   time.Now(),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc.SpecURL, nil)
	if err != nil {
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		oa.logger.Debug("failed to create OpenAPI fetch request",
			forge.F("service", svc.Name),
			forge.F("url", svc.SpecURL),
			forge.F("error", err),
		)
		return result
	}

	req.Header.Set("Accept", "application/json")

	resp, err := oa.httpClient.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("fetch failed: %v", err)
		oa.logger.Debug("failed to fetch OpenAPI spec",
			forge.F("service", svc.Name),
			forge.F("url", svc.SpecURL),
			forge.F("error", err),
		)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		oa.logger.Debug("non-200 response for OpenAPI spec",
			forge.F("service", svc.Name),
			forge.F("status", resp.StatusCode),
		)
		return result
	}

	// Read with a size limit (10MB max)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		result.Error = fmt.Sprintf("read failed: %v", err)
		return result
	}

	// Parse the spec
	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		result.Error = fmt.Sprintf("invalid JSON: %v", err)
		oa.logger.Debug("failed to parse OpenAPI spec",
			forge.F("service", svc.Name),
			forge.F("error", err),
		)
		return result
	}

	result.Spec = spec
	result.Healthy = true
	result.PathCount = countPaths(spec)

	oa.logger.Debug("fetched OpenAPI spec",
		forge.F("service", svc.Name),
		forge.F("paths", result.PathCount),
	)

	return result
}

// buildMergedSpec builds a unified OpenAPI 3.1.0 spec from all service specs.
func (oa *OpenAPIAggregator) buildMergedSpec(specs map[string]*ServiceOpenAPISpec) map[string]any {
	mergedPaths := make(map[string]any)
	mergedTags := make([]any, 0)
	mergedSchemas := make(map[string]any)
	mergedSecuritySchemes := make(map[string]any)
	serviceNames := make([]string, 0, len(specs))

	// Sort service names for deterministic output
	for name := range specs {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)

	tagSet := make(map[string]bool)

	for _, name := range serviceNames {
		svcSpec := specs[name]
		if svcSpec.Spec == nil || svcSpec.Error != "" {
			continue
		}

		spec := svcSpec.Spec

		// Extract paths
		paths, _ := spec["paths"].(map[string]any)

		// Determine the prefix for this service's paths.
		// GetServicePrefix respects the FARP manifest routing strategy
		// (e.g., root-mounted services get "" instead of "/{name}").
		prefix := ""
		if oa.config.MergeStrategy == "prefix" {
			if oa.disc != nil {
				prefix = oa.disc.GetServicePrefix(name)
			} else {
				prefix = "/" + normalizeServiceName(name)
			}
		}

		// Add a service-level tag
		serviceTag := name
		if !tagSet[serviceTag] {
			tagDesc := fmt.Sprintf("Operations from %s", name)
			if svcSpec.Version != "" {
				tagDesc += " v" + svcSpec.Version
			}
			mergedTags = append(mergedTags, map[string]any{
				"name":        serviceTag,
				"description": tagDesc,
			})
			tagSet[serviceTag] = true
		}

		// Build a ref mapping for THIS service's schemas so we can
		// precisely rewrite $ref pointers without cross-service corruption.
		refMap := make(map[string]string)

		// Merge components/schemas (namespace to avoid conflicts)
		if components, ok := spec["components"].(map[string]any); ok {
			if schemas, ok := components["schemas"].(map[string]any); ok {
				for schemaName, schema := range schemas {
					namespacedName := name + "_" + schemaName
					mergedSchemas[namespacedName] = schema
					refMap["#/components/schemas/"+schemaName] = "#/components/schemas/" + namespacedName
				}
			}

			if secSchemes, ok := components["securitySchemes"].(map[string]any); ok {
				for schemeName, scheme := range secSchemes {
					namespacedName := name + "_" + schemeName
					mergedSecuritySchemes[namespacedName] = scheme
				}
			}
		}

		// Merge paths and rewrite $ref pointers for THIS service only
		for path, pathItem := range paths {
			// Skip paths belonging to excluded extensions
			if oa.isExtensionPathExcluded(name, path) {
				continue
			}

			mergedPath := prefix + path
			if mergedPath == "" {
				mergedPath = "/"
			}

			// Tag all operations with the service name
			taggedPathItem := tagOperations(pathItem, serviceTag)

			// Strip requestBody from GET/HEAD/DELETE — upstream generators
			// (e.g., protoc-gen-openapiv2) may incorrectly add them.
			taggedPathItem = sanitizeOperations(taggedPathItem)

			// Rewrite $ref pointers in this service's paths using exact mapping
			rewriteRefsWithMap(taggedPathItem, refMap)

			if _, exists := mergedPaths[mergedPath]; exists && oa.config.MergeStrategy == "prefix" {
				// Prefix strategy shouldn't have conflicts, but handle gracefully
				oa.logger.Debug("path conflict in merged spec",
					forge.F("path", mergedPath),
					forge.F("service", name),
				)
			}

			mergedPaths[mergedPath] = taggedPathItem
		}

		// Rewrite $ref pointers inside this service's schemas (schema→schema refs)
		if components, ok := spec["components"].(map[string]any); ok {
			if schemas, ok := components["schemas"].(map[string]any); ok {
				for schemaName := range schemas {
					namespacedName := name + "_" + schemaName
					if schema, exists := mergedSchemas[namespacedName]; exists {
						rewriteRefsWithMap(schema, refMap)
					}
				}
			}
		}
	}

	// Build the merged spec
	merged := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       oa.config.Title,
			"description": oa.config.Description,
			"version":     oa.config.Version,
		},
		"paths": mergedPaths,
		"tags":  mergedTags,
	}

	// Add contact info if configured
	if oa.config.ContactName != "" || oa.config.ContactEmail != "" {
		info := merged["info"].(map[string]any)
		contact := make(map[string]any)
		if oa.config.ContactName != "" {
			contact["name"] = oa.config.ContactName
		}
		if oa.config.ContactEmail != "" {
			contact["email"] = oa.config.ContactEmail
		}
		info["contact"] = contact
	}

	// Add components if we have any
	if len(mergedSchemas) > 0 || len(mergedSecuritySchemes) > 0 {
		components := make(map[string]any)
		if len(mergedSchemas) > 0 {
			components["schemas"] = mergedSchemas
		}
		if len(mergedSecuritySchemes) > 0 {
			components["securitySchemes"] = mergedSecuritySchemes
		}
		merged["components"] = components
	}

	// Add x-gateway metadata
	merged["x-gateway"] = map[string]any{
		"generatedAt":     time.Now().UTC().Format(time.RFC3339),
		"serviceCount":    len(specs),
		"pathCount":       len(mergedPaths),
		"refreshInterval": oa.config.RefreshInterval.String(),
	}

	return merged
}

// addGatewayRoutes adds the gateway's own admin API routes to the merged spec
// with complete request/response schemas.
func (oa *OpenAPIAggregator) addGatewayRoutes(paths map[string]any, tags *[]any, tagSet map[string]bool) {
	if !tagSet["gateway"] {
		*tags = append(*tags, map[string]any{
			"name":        "gateway",
			"description": "Gateway administration API",
		})
		tagSet["gateway"] = true
	}

	// --- Shared parameter and schema references ---

	idParam := map[string]any{
		"name":        "id",
		"in":          "path",
		"required":    true,
		"description": "Route identifier",
		"schema":      map[string]any{"type": "string"},
	}

	errorResponse := map[string]any{
		"description": "Error response",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"error": map[string]any{"type": "string", "description": "Error message"},
					},
				},
			},
		},
	}

	targetSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":       map[string]any{"type": "string", "description": "Target identifier"},
			"url":      map[string]any{"type": "string", "format": "uri", "description": "Target URL"},
			"weight":   map[string]any{"type": "integer", "minimum": 0, "description": "Load balancing weight"},
			"healthy":  map[string]any{"type": "boolean", "description": "Health status"},
			"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tags"},
			"metadata": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Custom metadata"},
		},
	}

	routeSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "description": "Unique route identifier"},
			"path":        map[string]any{"type": "string", "description": "Route path pattern"},
			"methods":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "HTTP methods"},
			"targets":     map[string]any{"type": "array", "items": targetSchema, "description": "Upstream targets"},
			"stripPrefix": map[string]any{"type": "boolean", "description": "Strip path prefix before forwarding"},
			"protocol":    map[string]any{"type": "string", "enum": []string{"http", "websocket", "sse", "grpc", "graphql"}, "description": "Protocol type"},
			"source":      map[string]any{"type": "string", "enum": []string{"manual", "farp", "discovery"}, "description": "How route was created"},
			"serviceName": map[string]any{"type": "string", "description": "Associated service name"},
			"priority":    map[string]any{"type": "integer", "description": "Route priority"},
			"enabled":     map[string]any{"type": "boolean", "description": "Route enabled status"},
			"updatedAt":   map[string]any{"type": "string", "format": "date-time", "description": "Last update timestamp"},
		},
	}

	createRouteSchema := map[string]any{
		"type":     "object",
		"required": []string{"path", "targets"},
		"properties": map[string]any{
			"path":        map[string]any{"type": "string", "description": "Route path pattern"},
			"methods":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "HTTP methods"},
			"targets":     map[string]any{"type": "array", "items": targetSchema, "description": "Upstream targets"},
			"stripPrefix": map[string]any{"type": "boolean", "description": "Strip path prefix before forwarding"},
			"protocol":    map[string]any{"type": "string", "enum": []string{"http", "websocket", "sse", "grpc", "graphql"}, "default": "http"},
			"priority":    map[string]any{"type": "integer", "default": 10},
			"enabled":     map[string]any{"type": "boolean", "default": true},
		},
	}

	statsSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"totalRequests": map[string]any{"type": "integer", "format": "int64"},
			"totalErrors":   map[string]any{"type": "integer", "format": "int64"},
			"rateLimited":   map[string]any{"type": "integer", "format": "int64"},
			"circuitBreaks": map[string]any{"type": "integer", "format": "int64"},
			"cacheHits":     map[string]any{"type": "integer", "format": "int64"},
			"cacheMisses":   map[string]any{"type": "integer", "format": "int64"},
			"uptime":        map[string]any{"type": "integer", "format": "int64", "description": "Uptime in seconds"},
			"routeStats":    map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "object"}},
		},
	}

	discoveredServiceSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":         map[string]any{"type": "string"},
			"version":      map[string]any{"type": "string"},
			"address":      map[string]any{"type": "string"},
			"port":         map[string]any{"type": "integer"},
			"protocols":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"schemaTypes":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"capabilities": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"healthy":      map[string]any{"type": "boolean"},
			"metadata":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"routeCount":   map[string]any{"type": "integer"},
			"discoveredAt": map[string]any{"type": "string", "format": "date-time"},
		},
	}

	openapiServiceSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"serviceName": map[string]any{"type": "string"},
			"version":     map[string]any{"type": "string"},
			"specUrl":     map[string]any{"type": "string", "format": "uri"},
			"healthy":     map[string]any{"type": "boolean"},
			"pathCount":   map[string]any{"type": "integer"},
			"error":       map[string]any{"type": "string"},
			"fetchedAt":   map[string]any{"type": "string", "format": "date-time"},
		},
	}

	// --- Route definitions ---

	gatewayRoutes := map[string]map[string]any{
		"/gateway/api/routes": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "List all gateway routes",
				"description": "Returns all configured routes including manually created, FARP-discovered, and service-discovery routes.",
				"operationId": "listRoutes",
				"parameters": []map[string]any{
					{"name": "source", "in": "query", "description": "Filter by route source", "schema": map[string]any{"type": "string", "enum": []string{"manual", "farp", "discovery"}}},
					{"name": "protocol", "in": "query", "description": "Filter by protocol", "schema": map[string]any{"type": "string", "enum": []string{"http", "websocket", "sse", "grpc", "graphql"}}},
				},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "List of routes",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type":  "array",
									"items": routeSchema,
								},
							},
						},
					},
				},
			},
			"post": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Create a new route",
				"description": "Creates a new manually configured gateway route with the specified targets.",
				"operationId": "createRoute",
				"requestBody": map[string]any{
					"required": true,
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": createRouteSchema,
						},
					},
				},
				"responses": map[string]any{
					"201": map[string]any{
						"description": "Route created successfully",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": routeSchema,
							},
						},
					},
					"400": errorResponse,
				},
			},
		},
		"/gateway/api/routes/{id}": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Get route by ID",
				"description": "Returns a single route by its unique identifier.",
				"operationId": "getRoute",
				"parameters":  []map[string]any{idParam},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Route details",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": routeSchema,
							},
						},
					},
					"404": errorResponse,
				},
			},
			"put": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Update route",
				"description": "Updates an existing route's configuration.",
				"operationId": "updateRoute",
				"parameters":  []map[string]any{idParam},
				"requestBody": map[string]any{
					"required": true,
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": createRouteSchema,
						},
					},
				},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Route updated successfully",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": routeSchema,
							},
						},
					},
					"400": errorResponse,
					"404": errorResponse,
				},
			},
			"delete": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Delete route",
				"description": "Deletes a route by ID. Routes created by FARP or discovery may be re-created on next refresh.",
				"operationId": "deleteRoute",
				"parameters":  []map[string]any{idParam},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Route deleted successfully",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type":       "object",
									"properties": map[string]any{"status": map[string]any{"type": "string"}},
								},
							},
						},
					},
					"404": errorResponse,
				},
			},
		},
		"/gateway/api/routes/{id}/enable": {
			"post": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Enable route",
				"description": "Enables a previously disabled route.",
				"operationId": "enableRoute",
				"parameters":  []map[string]any{idParam},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Route enabled",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": routeSchema,
							},
						},
					},
					"404": errorResponse,
				},
			},
		},
		"/gateway/api/routes/{id}/disable": {
			"post": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Disable route",
				"description": "Disables a route without deleting it.",
				"operationId": "disableRoute",
				"parameters":  []map[string]any{idParam},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Route disabled",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": routeSchema,
							},
						},
					},
					"404": errorResponse,
				},
			},
		},
		"/gateway/api/upstreams": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "List all upstream targets",
				"description": "Returns all upstream targets across all routes with their health status.",
				"operationId": "listUpstreams",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "List of upstream targets",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type":  "array",
									"items": targetSchema,
								},
							},
						},
					},
				},
			},
		},
		"/gateway/api/stats": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Get gateway statistics",
				"description": "Returns aggregate gateway statistics including request counts, error rates, and cache metrics.",
				"operationId": "getStats",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Gateway statistics",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": statsSchema,
							},
						},
					},
				},
			},
		},
		"/gateway/api/stats/routes": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Get per-route statistics",
				"description": "Returns traffic statistics broken down by route.",
				"operationId": "getRouteStats",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Per-route statistics",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type": "object",
									"additionalProperties": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"path":          map[string]any{"type": "string"},
											"totalRequests": map[string]any{"type": "integer", "format": "int64"},
											"totalErrors":   map[string]any{"type": "integer", "format": "int64"},
											"avgLatencyMs":  map[string]any{"type": "number", "format": "double"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		"/gateway/api/config": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Get gateway configuration",
				"description": "Returns the current gateway configuration (read-only).",
				"operationId": "getConfig",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Gateway configuration",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{"type": "object"},
							},
						},
					},
				},
			},
		},
		"/gateway/api/discovery/services": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "List discovered services",
				"description": "Returns all services discovered via FARP and service discovery, including their health, protocols, and route counts.",
				"operationId": "listDiscoveredServices",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "List of discovered services",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type":  "array",
									"items": discoveredServiceSchema,
								},
							},
						},
					},
				},
			},
		},
		"/gateway/api/discovery/refresh": {
			"post": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Refresh service discovery",
				"description": "Triggers an immediate re-scan of all services.",
				"operationId": "refreshDiscovery",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Discovery refresh initiated",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type":       "object",
									"properties": map[string]any{"status": map[string]any{"type": "string"}},
								},
							},
						},
					},
				},
			},
		},
		"/gateway/api/openapi/services": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "List services with OpenAPI specs",
				"description": "Returns a summary of all services with their OpenAPI spec status, path counts, and fetch errors.",
				"operationId": "listOpenAPIServices",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Services with their OpenAPI spec status",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"services":    map[string]any{"type": "array", "items": openapiServiceSchema},
										"totalCount":  map[string]any{"type": "integer"},
										"lastRefresh": map[string]any{"type": "string", "format": "date-time"},
									},
								},
							},
						},
					},
				},
			},
		},
		"/gateway/api/openapi/services/{service}": {
			"get": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Get service OpenAPI spec",
				"description": "Returns the cached OpenAPI spec for a specific upstream service.",
				"operationId": "getServiceOpenAPISpec",
				"parameters": []map[string]any{
					{"name": "service", "in": "path", "required": true, "description": "Service name", "schema": map[string]any{"type": "string"}},
				},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "OpenAPI specification for the service",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{"type": "object"},
							},
						},
					},
					"404": errorResponse,
					"502": errorResponse,
				},
			},
		},
		"/gateway/api/openapi/refresh": {
			"post": map[string]any{
				"tags":        []string{"gateway"},
				"summary":     "Refresh OpenAPI specs",
				"description": "Triggers an immediate refresh of all upstream OpenAPI specs.",
				"operationId": "refreshOpenAPISpecs",
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Refresh initiated",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{
									"type":       "object",
									"properties": map[string]any{"status": map[string]any{"type": "string"}},
								},
							},
						},
					},
				},
			},
		},
	}

	for path, pathItem := range gatewayRoutes {
		paths[path] = pathItem
	}
}

// tagOperations adds a tag to all operations in a path item.
func tagOperations(pathItem any, tag string) any {
	pathItemMap, ok := pathItem.(map[string]any)
	if !ok {
		return pathItem
	}

	methods := []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
	for _, method := range methods {
		if op, ok := pathItemMap[method]; ok {
			if opMap, ok := op.(map[string]any); ok {
				existingTags, _ := opMap["tags"].([]any)
				// Add service tag if not already present
				hasTag := false
				for _, t := range existingTags {
					if t == tag {
						hasTag = true
						break
					}
				}
				if !hasTag {
					opMap["tags"] = append(existingTags, tag)
				}
			}
		}
	}

	return pathItemMap
}

// sanitizeOperations removes requestBody from HTTP methods that must not
// have a body (GET, HEAD, DELETE) per the HTTP and OpenAPI specifications.
// This handles upstream generators (e.g., protoc-gen-openapiv2) that
// incorrectly add requestBody to GET endpoints.
func sanitizeOperations(pathItem any) any {
	pathItemMap, ok := pathItem.(map[string]any)
	if !ok {
		return pathItem
	}

	noBodyMethods := []string{"get", "head", "delete"}
	for _, method := range noBodyMethods {
		if op, ok := pathItemMap[method]; ok {
			if opMap, ok := op.(map[string]any); ok {
				delete(opMap, "requestBody")
			}
		}
	}

	return pathItemMap
}

// rewriteRefsWithMap rewrites $ref pointers using an exact mapping.
// Only refs that exist in refMap are rewritten; all others are left unchanged.
// This ensures per-service precision — each service's refs are mapped using
// only that service's schema name mapping, preventing cross-service corruption.
func rewriteRefsWithMap(obj any, refMap map[string]string) {
	if len(refMap) == 0 {
		return
	}

	switch v := obj.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			if newRef, exists := refMap[ref]; exists {
				v["$ref"] = newRef
			}
		}

		for _, val := range v {
			rewriteRefsWithMap(val, refMap)
		}

	case []any:
		for _, item := range v {
			rewriteRefsWithMap(item, refMap)
		}
	}
}

// countPaths counts the number of paths in an OpenAPI spec.
func countPaths(spec map[string]any) int {
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		return 0
	}

	return len(paths)
}

// isExcluded checks if a service name is in the exclusion list.
func (oa *OpenAPIAggregator) isExcluded(name string) bool {
	for _, excluded := range oa.config.ExcludeServices {
		if strings.EqualFold(excluded, name) {
			return true
		}
	}
	return false
}

// findExtensionFilter returns the ExtensionPathFilter for the given service,
// or nil if none is configured. It checks both ServiceName and ServiceNames.
func (oa *OpenAPIAggregator) findExtensionFilter(serviceName string) *ExtensionPathFilter {
	for i := range oa.config.ExtensionFilters {
		if oa.config.ExtensionFilters[i].MatchesService(serviceName) {
			return &oa.config.ExtensionFilters[i]
		}
	}
	return nil
}

// isExtensionPathExcluded checks if an upstream path for the given service
// should be excluded based on extension path filter rules. It delegates to
// the shared filtering logic that checks include/exclude prefixes and
// any-segment matching against known/allowed extensions.
func (oa *OpenAPIAggregator) isExtensionPathExcluded(serviceName, upstreamPath string) bool {
	filter := oa.findExtensionFilter(serviceName)
	if filter == nil {
		return false
	}
	return isExtensionPathExcludedByFilter(upstreamPath, filter)
}

// isExtensionPathExcludedByFilter implements the filtering logic for the
// discovery package, mirroring the root bastion.IsExtensionPathExcluded.
//
// Priority order:
//  1. IncludePrefixes → include (highest priority)
//  2. ExcludePrefixes → exclude
//  3. Any segment matches KnownExtensions but NOT AllowedExtensions → exclude
//  4. Otherwise → include
func isExtensionPathExcludedByFilter(upstreamPath string, filter *ExtensionPathFilter) bool {
	if filter == nil {
		return false
	}

	// Normalize path for prefix matching.
	normalized := upstreamPath
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}

	// 1. IncludePrefixes — highest priority whitelist.
	for _, prefix := range filter.IncludePrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return false
		}
	}

	// 2. ExcludePrefixes — explicit path exclusion.
	for _, prefix := range filter.ExcludePrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}

	// 3. Any-segment matching against KnownExtensions.
	if len(filter.KnownExtensions) == 0 {
		return false
	}

	segments := splitPathSegments(upstreamPath)
	for _, seg := range segments {
		isKnown := false
		for _, ext := range filter.KnownExtensions {
			if strings.EqualFold(ext, seg) {
				isKnown = true
				break
			}
		}
		if !isKnown {
			continue
		}

		isAllowed := false
		for _, ext := range filter.AllowedExtensions {
			if strings.EqualFold(ext, seg) {
				isAllowed = true
				break
			}
		}
		if !isAllowed {
			return true
		}
	}

	return false
}

// splitPathSegments splits a URL path into its non-empty segments.
func splitPathSegments(path string) []string {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return nil
	}
	parts := strings.Split(path, "/")
	segments := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			segments = append(segments, p)
		}
	}
	return segments
}

// --- HTTP Handlers ---

// HandleMergedSpec serves the aggregated OpenAPI spec as JSON.
func (oa *OpenAPIAggregator) HandleMergedSpec(ctx forge.Context) error {
	specJSON := oa.MergedSpec()
	if specJSON == nil {
		return ctx.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "OpenAPI spec not yet available, try again shortly",
		})
	}

	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().Header().Set("Cache-Control", "public, max-age=30")
	ctx.Response().WriteHeader(http.StatusOK)
	_, err := ctx.Response().Write(specJSON)

	return err
}

// HandleServiceSpec serves the OpenAPI spec for a specific service.
func (oa *OpenAPIAggregator) HandleServiceSpec(ctx forge.Context) error {
	serviceName := ctx.Param("service")
	if serviceName == "" {
		return ctx.JSON(http.StatusBadRequest, map[string]string{"error": "service name required"})
	}

	spec := oa.ServiceSpec(serviceName)
	if spec == nil {
		return ctx.JSON(http.StatusNotFound, map[string]string{"error": "service not found"})
	}

	if spec.Error != "" {
		return ctx.JSON(http.StatusBadGateway, map[string]any{
			"error":       spec.Error,
			"serviceName": spec.ServiceName,
			"specUrl":     spec.SpecURL,
		})
	}

	return ctx.JSON(http.StatusOK, spec.Spec)
}

// HandleServiceList returns a summary of all services with their OpenAPI spec status.
func (oa *OpenAPIAggregator) HandleServiceList(ctx forge.Context) error {
	specs := oa.ServiceSpecs()

	type serviceSummary struct {
		ServiceName string    `json:"serviceName"`
		Version     string    `json:"version"`
		SpecURL     string    `json:"specUrl"`
		Healthy     bool      `json:"healthy"`
		PathCount   int       `json:"pathCount"`
		Error       string    `json:"error,omitempty"`
		FetchedAt   time.Time `json:"fetchedAt"`
	}

	summaries := make([]serviceSummary, 0, len(specs))
	for _, spec := range specs {
		summaries = append(summaries, serviceSummary{
			ServiceName: spec.ServiceName,
			Version:     spec.Version,
			SpecURL:     spec.SpecURL,
			Healthy:     spec.Healthy,
			PathCount:   spec.PathCount,
			Error:       spec.Error,
			FetchedAt:   spec.FetchedAt,
		})
	}

	// Sort by name for deterministic output
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].ServiceName < summaries[j].ServiceName
	})

	return ctx.JSON(http.StatusOK, map[string]any{
		"services":    summaries,
		"totalCount":  len(summaries),
		"lastRefresh": oa.LastRefresh(),
	})
}

// HandleRefresh triggers an immediate spec refresh.
func (oa *OpenAPIAggregator) HandleRefresh(ctx forge.Context) error {
	go oa.Refresh(ctx.Request().Context())

	return ctx.JSON(http.StatusOK, map[string]string{
		"status": "refresh initiated",
	})
}

// HandleGatewaySpec serves an OpenAPI spec containing only the gateway's
// own admin API routes (route management, stats, discovery, etc.).
// This is separate from the aggregated upstream service spec.
func (oa *OpenAPIAggregator) HandleGatewaySpec(ctx forge.Context) error {
	paths := make(map[string]any)
	tags := make([]any, 0)
	tagSet := make(map[string]bool)

	oa.addGatewayRoutes(paths, &tags, tagSet)

	spec := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       oa.config.Title + " - Admin API",
			"description": "Gateway administration and management API",
			"version":     oa.config.Version,
		},
		"paths": paths,
		"tags":  tags,
	}

	return ctx.JSON(http.StatusOK, spec)
}

// HandleSwaggerUI serves a Swagger UI page for the aggregated spec.
// Deprecated: Use SwaggerUIHandler with an explicit spec URL instead.
func (oa *OpenAPIAggregator) HandleSwaggerUI(ctx forge.Context) error {
	return oa.serveSwaggerUI(ctx, oa.config.Path)
}

// SwaggerUIHandler returns a handler that serves a Swagger UI page pointing
// to the given spec URL. Use this to create handlers at different paths that
// all point to the correct aggregated spec location.
func (oa *OpenAPIAggregator) SwaggerUIHandler(specURL string) forge.Handler {
	return func(ctx forge.Context) error {
		return oa.serveSwaggerUI(ctx, specURL)
	}
}

// serveSwaggerUI renders the Swagger UI HTML page pointing to the given spec URL.
func (oa *OpenAPIAggregator) serveSwaggerUI(ctx forge.Context, specURL string) error {
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
  <title>%s - API Documentation</title>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" >
  <style>
    html { box-sizing: border-box; overflow: -moz-scrollbars-vertical; overflow-y: scroll; }
    *, *:before, *:after { box-sizing: inherit; }
    body { margin: 0; background: #fafafa; }
    .swagger-ui .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js"></script>
  <script>
    window.onload = function() {
      SwaggerUIBundle({
        url: "%s",
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIStandalonePreset
        ],
        plugins: [
          SwaggerUIBundle.plugins.DownloadUrl
        ],
        layout: "StandaloneLayout",
        defaultModelsExpandDepth: 1,
        defaultModelExpandDepth: 1,
        docExpansion: "list",
        filter: true,
        showExtensions: true,
        tagsSorter: "alpha",
        operationsSorter: "alpha"
      });
    }
  </script>
</body>
</html>`, oa.config.Title, specURL)

	ctx.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	ctx.Response().WriteHeader(http.StatusOK)
	_, err := ctx.Response().Write([]byte(html))

	return err
}
