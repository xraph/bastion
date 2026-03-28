package discovery

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RouteProtocol defines the protocol type for a gateway route.
type RouteProtocol string

const (
	// ProtocolHTTP is standard HTTP/HTTPS proxying.
	ProtocolHTTP RouteProtocol = "http"

	// ProtocolWebSocket is WebSocket proxying.
	ProtocolWebSocket RouteProtocol = "websocket"

	// ProtocolSSE is Server-Sent Events proxying.
	ProtocolSSE RouteProtocol = "sse"

	// ProtocolGRPC is gRPC proxying.
	ProtocolGRPC RouteProtocol = "grpc"

	// ProtocolGraphQL is GraphQL proxying (HTTP-based with special handling).
	ProtocolGraphQL RouteProtocol = "graphql"
)

// RouteSource indicates how a route was created.
type RouteSource string

const (
	// SourceManual indicates a manually configured route.
	SourceManual RouteSource = "manual"

	// SourceFARP indicates a route auto-generated from FARP schemas.
	SourceFARP RouteSource = "farp"

	// SourceDiscovery indicates a route from service discovery.
	SourceDiscovery RouteSource = "discovery"
)

// Route represents a configured gateway route (local copy of the root type).
type Route struct {
	ID          string        `json:"id"`
	Path        string        `json:"path"`
	Methods     []string      `json:"methods,omitempty"`
	Targets     []*Target     `json:"targets"`
	StripPrefix bool          `json:"stripPrefix"`
	Protocol    RouteProtocol `json:"protocol"`
	Source      RouteSource   `json:"source"`
	ServiceName string        `json:"serviceName,omitempty"`
	Priority    int           `json:"priority"`
	Enabled     bool          `json:"enabled"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

// Target represents an upstream service endpoint (local copy of the root type).
type Target struct {
	ID       string            `json:"id"`
	URL      string            `json:"url"`
	Weight   int               `json:"weight"`
	Healthy  bool              `json:"healthy"`
	Tags     []string          `json:"tags,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// DiscoveredService represents a service found via FARP/discovery.
type DiscoveredService struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Address      string            `json:"address"`
	Port         int               `json:"port"`
	Protocols    []string          `json:"protocols"`
	SchemaTypes  []string          `json:"schemaTypes"`
	Capabilities []string          `json:"capabilities"`
	Healthy      bool              `json:"healthy"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	RouteCount   int               `json:"routeCount"`
	DiscoveredAt time.Time         `json:"discoveredAt"`
}

// DiscoveryConfig holds FARP auto-discovery settings.
type DiscoveryConfig struct {
	Enabled        bool            `json:"enabled" yaml:"enabled"`
	PollInterval   time.Duration   `json:"pollInterval" yaml:"poll_interval"`
	WatchMode      bool            `json:"watchMode" yaml:"watch_mode"`
	ServiceFilters []ServiceFilter `json:"serviceFilters,omitempty" yaml:"service_filters"`
	AutoPrefix     bool            `json:"autoPrefix" yaml:"auto_prefix"`
	PrefixTemplate string          `json:"prefixTemplate,omitempty" yaml:"prefix_template"`
	StripPrefix    bool            `json:"stripPrefix" yaml:"strip_prefix"`

	// PrefixOverrides allows overriding the route prefix for specific services.
	// The key is the service name (case-insensitive) and the value is the
	// prefix to use. An empty string "" means mount at root (no prefix).
	//
	// Example: {"twinos": ""} mounts twinos at / instead of /twinos.
	// Example: {"twinos": "/api"} mounts twinos at /api instead of /twinos.
	//
	// This takes precedence over AutoPrefix, PrefixTemplate, and the FARP
	// manifest routing strategy.
	PrefixOverrides map[string]string `json:"prefixOverrides,omitempty" yaml:"prefix_overrides"`

	// FetchTimeout is the timeout for HTTP requests such as fetching
	// FARP manifests from discovered services. Defaults to 10s.
	FetchTimeout time.Duration `json:"fetchTimeout,omitempty" yaml:"fetch_timeout"`

	// RemovalGracePeriod is how long to keep routes for a service that has
	// disappeared from ListServices before actually removing them. This
	// handles transient mDNS/discovery backend issues. Defaults to 60s.
	RemovalGracePeriod time.Duration `json:"removalGracePeriod,omitempty" yaml:"removal_grace_period"`
}

// ServiceFilter defines a filter for discovered services.
type ServiceFilter struct {
	IncludeNames    []string          `json:"includeNames,omitempty" yaml:"include_names"`
	ExcludeNames    []string          `json:"excludeNames,omitempty" yaml:"exclude_names"`
	IncludeTags     []string          `json:"includeTags,omitempty" yaml:"include_tags"`
	ExcludeTags     []string          `json:"excludeTags,omitempty" yaml:"exclude_tags"`
	RequireMetadata map[string]string `json:"requireMetadata,omitempty" yaml:"require_metadata"`
}

// DiscoveryService is the interface that the gateway requires from a discovery provider.
type DiscoveryService interface {
	// ListServices lists all registered service names.
	ListServices(ctx context.Context) ([]string, error)

	// DiscoverHealthy returns healthy instances for a service.
	DiscoverHealthy(ctx context.Context, serviceName string) ([]*ServiceInstanceInfo, error)
}

// ServiceInstanceInfo represents a discovered service instance.
type ServiceInstanceInfo struct {
	ID       string
	Name     string
	Version  string
	Address  string
	Port     int
	Tags     []string
	Metadata map[string]string
	Healthy  bool
}

// URL returns the full URL for the service instance.
func (si *ServiceInstanceInfo) URL(scheme string) string {
	if scheme == "" {
		scheme = "http"
	}

	return fmt.Sprintf("%s://%s:%d", scheme, si.Address, si.Port)
}

// IsHealthy returns whether the instance is healthy.
func (si *ServiceInstanceInfo) IsHealthy() bool {
	return si.Healthy
}

// RouteRegistry is the interface for route management operations used
// by the discovery package. This avoids importing the root bastion package.
type RouteRegistry interface {
	// AddRoute adds a new route to the table.
	AddRoute(route *Route) error

	// RemoveRoute removes a single route by ID.
	RemoveRoute(id string) error

	// UpdateRoute updates an existing route.
	UpdateRoute(route *Route) error

	// GetRoute returns a route by ID.
	GetRoute(id string) (*Route, bool)

	// RemoveByServiceName removes all routes for a service.
	RemoveByServiceName(serviceName string)

	// ListRoutes returns all routes.
	ListRoutes() []*Route
}

// OpenAPIConfig holds configuration for the OpenAPI aggregation feature.
type OpenAPIConfig struct {
	// Enabled turns on/off the OpenAPI aggregation feature
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Path is the endpoint path to serve the aggregated OpenAPI spec
	Path string `json:"path" yaml:"path"`

	// UIPath is the endpoint path to serve the Swagger UI
	UIPath string `json:"uiPath" yaml:"ui_path"`

	// Title is the title for the aggregated spec
	Title string `json:"title" yaml:"title"`

	// Description is the description for the aggregated spec
	Description string `json:"description" yaml:"description"`

	// Version is the version for the aggregated spec
	Version string `json:"version" yaml:"version"`

	// RefreshInterval is how often to re-fetch upstream specs
	RefreshInterval time.Duration `json:"refreshInterval" yaml:"refresh_interval"`

	// FetchTimeout is the timeout for fetching a single upstream spec
	FetchTimeout time.Duration `json:"fetchTimeout" yaml:"fetch_timeout"`

	// StripServicePrefix controls whether service prefixes are included in paths
	StripServicePrefix bool `json:"stripServicePrefix" yaml:"strip_service_prefix"`

	// MergeStrategy controls how conflicting paths are handled:
	// "prefix" (default) - prefix all paths with /{serviceName}
	// "flat" - merge paths as-is, last wins on conflict
	MergeStrategy string `json:"mergeStrategy" yaml:"merge_strategy"`

	// IncludeGatewayRoutes includes the gateway's own admin routes in the spec
	IncludeGatewayRoutes bool `json:"includeGatewayRoutes" yaml:"include_gateway_routes"`

	// ExcludeServices is a list of service names to exclude from aggregation
	ExcludeServices []string `json:"excludeServices,omitempty" yaml:"exclude_services"`

	// ContactName is the contact name for the spec info
	ContactName string `json:"contactName,omitempty" yaml:"contact_name"`

	// ContactEmail is the contact email for the spec info
	ContactEmail string `json:"contactEmail,omitempty" yaml:"contact_email"`

	// EnableRootDocs registers the aggregated spec and Swagger UI at root-level
	// paths (/openapi.json and /docs) in addition to the dashboard-prefixed paths.
	// Use this when the gateway should be the primary API documentation surface.
	EnableRootDocs bool `json:"enableRootDocs" yaml:"enable_root_docs"`

	// RootUIPath is the root-level Swagger UI path (default: "/docs").
	// Only used when EnableRootDocs is true.
	RootUIPath string `json:"rootUiPath,omitempty" yaml:"root_ui_path"`

	// EnableGatewayDocs serves the gateway's own admin API documentation
	// at the dashboard-prefixed swagger path (e.g., /gateway/swagger).
	// Disabled by default.
	EnableGatewayDocs bool `json:"enableGatewayDocs" yaml:"enable_gateway_docs"`

	// ExtensionFilters defines per-service extension path filtering rules.
	// When a service loads Forge extensions, their paths appear as
	// /{extension-name}/... in the service's OpenAPI spec. These filters
	// control which extension paths are exposed through the gateway.
	ExtensionFilters []ExtensionPathFilter `json:"extensionFilters,omitempty" yaml:"extension_filters"`
}

// ExtensionPathFilter defines per-service extension path filtering.
// When a service loads Forge extensions (e.g., "cortex", "dispatch"),
// their endpoints appear in the service's API under extension-specific
// base paths (e.g., /cortex/..., /api/dispatch/v1/...).
// This filter controls which extension paths the gateway exposes.
type ExtensionPathFilter struct {
	// ServiceName is the name of a single service to filter (e.g., "portal").
	// For filtering multiple services with the same rules, use ServiceNames.
	// At least one of ServiceName or ServiceNames must be set.
	ServiceName string `json:"serviceName,omitempty" yaml:"service_name"`

	// ServiceNames lists multiple service names this filter applies to.
	// This allows a single filter definition to cover several services
	// (e.g., ["portal", "twinos", "admin-api"]).
	ServiceNames []string `json:"serviceNames,omitempty" yaml:"service_names"`

	// KnownExtensions lists ALL extension names loaded by this service.
	// Any path segment matching a known extension triggers filtering.
	// This handles both direct mounts (/{ext}/...) and nested mounts
	// (/api/{ext}/v1/...). Paths with no segment matching any known
	// extension are treated as native service paths and always pass through.
	KnownExtensions []string `json:"knownExtensions" yaml:"known_extensions"`

	// AllowedExtensions lists which of the known extensions should be
	// included. All other known extensions are excluded.
	// If empty and KnownExtensions is set, NO extension paths are included.
	AllowedExtensions []string `json:"allowedExtensions,omitempty" yaml:"allowed_extensions"`

	// ExcludePrefixes lists explicit upstream path prefixes to exclude.
	// Use this for paths where the extension name does not appear as a
	// path segment (e.g., "/v1/admin" for ctrlplane extension paths).
	// Each entry must start with "/".
	ExcludePrefixes []string `json:"excludePrefixes,omitempty" yaml:"exclude_prefixes"`

	// IncludePrefixes lists explicit upstream path prefixes that should
	// always be included, overriding both KnownExtensions and
	// ExcludePrefixes. Use this to whitelist specific paths. Highest priority.
	// Each entry must start with "/".
	IncludePrefixes []string `json:"includePrefixes,omitempty" yaml:"include_prefixes"`

	// BlockTraffic controls whether the gateway also blocks proxy traffic
	// to excluded extension paths (returns 403), not just hides them from
	// the OpenAPI spec. Default: false.
	BlockTraffic bool `json:"blockTraffic" yaml:"block_traffic"`
}

// MatchesService reports whether this filter applies to the given service name.
// It checks both the singular ServiceName and the ServiceNames list (case-insensitive).
func (f *ExtensionPathFilter) MatchesService(name string) bool {
	if strings.EqualFold(f.ServiceName, name) {
		return true
	}
	for _, sn := range f.ServiceNames {
		if strings.EqualFold(sn, name) {
			return true
		}
	}
	return false
}

// AllServiceNames returns the deduplicated list of service names this filter
// applies to (combining ServiceName and ServiceNames).
func (f *ExtensionPathFilter) AllServiceNames() []string {
	seen := make(map[string]bool)
	var names []string
	if f.ServiceName != "" {
		lower := strings.ToLower(f.ServiceName)
		seen[lower] = true
		names = append(names, f.ServiceName)
	}
	for _, sn := range f.ServiceNames {
		lower := strings.ToLower(sn)
		if !seen[lower] {
			seen[lower] = true
			names = append(names, sn)
		}
	}
	return names
}

// DefaultOpenAPIConfig returns defaults for OpenAPI aggregation.
func DefaultOpenAPIConfig() OpenAPIConfig {
	return OpenAPIConfig{
		Enabled:         true,
		Path:            "/openapi.json",
		UIPath:          "/swagger",
		Title:           "API Gateway",
		Description:     "Aggregated API specification from all upstream services",
		Version:         "1.0.0",
		RefreshInterval: 30 * time.Second,
		FetchTimeout:    10 * time.Second,
		MergeStrategy:   "prefix",
	}
}

// ServiceOpenAPISpec holds a cached OpenAPI spec for a single upstream service.
type ServiceOpenAPISpec struct {
	ServiceName string         `json:"serviceName"`
	Version     string         `json:"version"`
	SpecURL     string         `json:"specUrl"`
	Spec        map[string]any `json:"spec,omitempty"`
	FetchedAt   time.Time      `json:"fetchedAt"`
	Error       string         `json:"error,omitempty"`
	Healthy     bool           `json:"healthy"`
	PathCount   int            `json:"pathCount"`
}

// ServiceChangeHook is called when a service is registered or deregistered.
// serviceName identifies the service; registered is true for registrations,
// false for deregistrations.
type ServiceChangeHook func(serviceName string, registered bool)
