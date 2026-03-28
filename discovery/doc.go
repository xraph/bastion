// Package discovery provides FARP-based service discovery and OpenAPI specification
// aggregation for the Bastion API gateway.
//
// # Overview
//
// The discovery package automatically discovers upstream services, generates gateway
// routes, and aggregates OpenAPI specifications into a unified API documentation
// endpoint. It integrates with the FARP (Forge API Registration Protocol) for rich
// service metadata and routing configuration.
//
// # Service Discovery
//
// The [Manager] periodically polls a [DiscoveryService] for available services
// and automatically creates gateway routes. The discovery flow is:
//
//  1. List all registered services from the discovery provider
//  2. For each service, discover healthy instances
//  3. Check for FARP metadata on instances (farp.enabled, farp.manifest, etc.)
//  4. If FARP is enabled, fetch the full FARP [SchemaManifest] for rich routing config
//  5. Fall back to flat FARP metadata keys if manifest fetch fails
//  6. Create gateway routes with appropriate protocols, prefixes, and priorities
//  7. Remove routes for services that are no longer registered
//
// # FARP Integration
//
// When a service publishes FARP metadata via the forge discovery extension, the
// following metadata keys are used:
//
//   - farp.enabled: "true" to indicate the service supports FARP
//   - farp.manifest: URL to the service's SchemaManifest endpoint
//   - farp.openapi: URL to the service's OpenAPI spec
//   - farp.openapi.path: Path to the OpenAPI spec (combined with service address)
//   - farp.asyncapi: URL to the service's AsyncAPI spec
//   - farp.graphql: URL to the service's GraphQL introspection endpoint
//   - farp.capabilities: Comma-separated list of capabilities (rest, grpc, websocket, etc.)
//
// When the full manifest is available (via farp.manifest), the discovery manager
// uses the manifest's [RoutingConfig] to determine:
//
//   - Mount strategy: root, service, versioned, custom, instance, or subdomain
//   - Base path for custom mounting
//   - Route priority for conflict resolution
//   - Whether to strip the path prefix before forwarding
//
// # OpenAPI Aggregation
//
// The [OpenAPIAggregator] fetches OpenAPI specs from all discovered services and
// merges them into a unified OpenAPI 3.1.0 specification. It provides:
//
//   - Merged spec endpoint (default: /openapi.json) — all service specs combined
//   - Swagger UI endpoint (default: /swagger) — interactive API documentation
//   - Per-service spec endpoints — individual service specs
//   - Service list endpoint — summary of all services with spec status
//   - On-demand refresh endpoint — trigger immediate spec re-fetch
//
// # Configuration
//
// Service discovery is configured via [DiscoveryConfig]:
//
//	discovery.DiscoveryConfig{
//	    Enabled:        true,
//	    PollInterval:   30 * time.Second,
//	    AutoPrefix:     true,
//	    PrefixTemplate: "/{{.ServiceName}}",
//	    StripPrefix:    true,
//	    FetchTimeout:   10 * time.Second,
//	}
//
// OpenAPI aggregation is configured via [OpenAPIConfig]:
//
//	discovery.OpenAPIConfig{
//	    Enabled:              true,
//	    Path:                 "/openapi.json",
//	    UIPath:               "/swagger",
//	    Title:                "API Gateway",
//	    RefreshInterval:      30 * time.Second,
//	    FetchTimeout:         10 * time.Second,
//	    MergeStrategy:        "prefix",
//	    IncludeGatewayRoutes: true,
//	}
package discovery
