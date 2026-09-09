package bastion

import (
	"github.com/xraph/forge"

	disc "github.com/xraph/bastion/discovery"
	"github.com/xraph/bastion/middleware"
	"github.com/xraph/bastion/security"
)

// ServiceDiscovery is a backward-compatible alias for discovery.Manager.
type ServiceDiscovery = disc.Manager

// NewServiceDiscovery creates a new service discovery integration.
// The RouteRegistry is adapted to satisfy discovery.RouteRegistry.
func NewServiceDiscovery(
	config DiscoveryConfig,
	logger forge.Logger,
	rm RouteRegistry,
	discService DiscoveryService,
) *ServiceDiscovery {
	return disc.NewManager(
		config,
		logger,
		newRouteRegistryAdapter(rm),
		discService,
	)
}

// OpenAPIAggregator is a backward-compatible alias for discovery.OpenAPIAggregator.
type OpenAPIAggregator = disc.OpenAPIAggregator

// NewOpenAPIAggregator creates a new OpenAPI aggregator.
func NewOpenAPIAggregator(config OpenAPIConfig, logger forge.Logger, rm RouteRegistry, sd *ServiceDiscovery) *OpenAPIAggregator {
	return disc.NewAggregator(
		config,
		logger,
		newRouteRegistryAdapter(rm),
		sd,
	)
}

// AsyncAPIAggregator is a backward-compatible alias for discovery.AsyncAPIAggregator.
type AsyncAPIAggregator = disc.AsyncAPIAggregator

// NewAsyncAPIAggregator creates a new AsyncAPI aggregator.
func NewAsyncAPIAggregator(config AsyncAPIConfig, logger forge.Logger, sd *ServiceDiscovery) *AsyncAPIAggregator {
	return disc.NewAsyncAPIAggregator(config, logger, sd)
}

// --- RouteRegistry adapter ---

// routeRegistryAdapter adapts RouteRegistry to discovery.RouteRegistry.
// The root Route/Target types have more fields than the discovery versions,
// so we convert between them to bridge the two packages.
type routeRegistryAdapter struct {
	rm RouteRegistry
}

func newRouteRegistryAdapter(rm RouteRegistry) *routeRegistryAdapter {
	return &routeRegistryAdapter{rm: rm}
}

// AddRoute converts a discovery.Route to root Route and adds it.
func (a *routeRegistryAdapter) AddRoute(route *disc.Route) error {
	return a.rm.AddRoute(discRouteToRoot(route))
}

// UpdateRoute merges discovery-managed fields onto the existing bastion.Route
// to preserve fields that the discovery package doesn't manage (e.g., Version,
// AddPrefix, RewritePath, Headers, Auth, RateLimit, CircuitBreaker, Cache,
// TrafficPolicy, Transform, Metadata, CreatedAt). Without this merge,
// every discovery poll would zero out these fields because discRouteToRoot
// can't copy what disc.Route doesn't have.
func (a *routeRegistryAdapter) UpdateRoute(route *disc.Route) error {
	existing, ok := a.rm.GetRoute(route.ID)
	if !ok {
		// Route doesn't exist yet — create fresh.
		return a.rm.UpdateRoute(discRouteToRoot(route))
	}

	// Shallow copy preserves all bastion-specific fields.
	updated := *existing
	updated.Path = route.Path
	updated.Methods = route.Methods
	updated.StripPrefix = route.StripPrefix
	updated.Protocol = RouteProtocol(route.Protocol)
	updated.Source = RouteSource(route.Source)
	updated.ServiceName = route.ServiceName
	updated.Priority = route.Priority
	updated.Enabled = route.Enabled
	updated.UpdatedAt = route.UpdatedAt

	targets := make([]*Target, len(route.Targets))
	for i, t := range route.Targets {
		targets[i] = &Target{
			ID:       t.ID,
			URL:      t.URL,
			Weight:   t.Weight,
			Healthy:  t.Healthy,
			Tags:     t.Tags,
			Metadata: t.Metadata,
		}
	}
	updated.Targets = targets

	// Merge per-route overrides from discovery layer.
	if route.Timeout != nil {
		updated.Timeout = &TimeoutConfig{Read: route.Timeout.Read, Write: route.Timeout.Write}
	}
	// A discovery override carrying no usable rate OR no burst is not a rate
	// limit, it is
	// a zero-valued struct that nobody filled in. Honouring it anyway built
	// newTokenBucket(0, 0): no tokens to begin with and no refill, since the
	// bucket only ever gains `elapsed * rate`. Burst alone is enough to do it:
	// it sets maxTokens, and every refill is clamped to maxTokens, so a burst of
	// zero pins the bucket at zero no matter how generous the rate. The route
	// then answered 429 to
	// every request for the life of the process, survived restarts because
	// re-registration restores the same override, and never recovered with
	// time. Skipping it leaves the route unlimited, which is what it was
	// before the empty override arrived.
	if route.RateLimit != nil && route.RateLimit.RequestsPerSec > 0 &&
		route.RateLimit.Burst > 0 {
		updated.RateLimit = &middleware.RateLimitConfig{
			Enabled:        true,
			RequestsPerSec: route.RateLimit.RequestsPerSec,
			Burst:          route.RateLimit.Burst,
			PerClient:      route.RateLimit.PerClient,
		}
	}
	if route.Cache != nil {
		updated.Cache = &middleware.RouteCacheConfig{
			Enabled: route.Cache.Enabled,
			TTL:     route.Cache.TTL,
			VaryBy:  route.Cache.VaryBy,
		}
	}
	if route.Auth != nil {
		updated.Auth = &security.RouteAuthConfig{
			SkipAuth: route.Auth.SkipAuth,
			Scopes:   route.Auth.Scopes,
		}
		if !route.Auth.SkipAuth && len(route.Auth.Scopes) > 0 {
			updated.Auth.Enabled = true
		}
	}
	if route.Metadata != nil {
		updated.Metadata = route.Metadata
	}

	return a.rm.UpdateRoute(&updated)
}

// GetRoute returns a route by ID, converting from root to discovery type.
func (a *routeRegistryAdapter) GetRoute(id string) (*disc.Route, bool) {
	route, ok := a.rm.GetRoute(id)
	if !ok {
		return nil, false
	}

	return rootRouteToDisc(route), true
}

// RemoveRoute removes a single route by ID.
func (a *routeRegistryAdapter) RemoveRoute(id string) error {
	return a.rm.RemoveRoute(id)
}

// RemoveByServiceName removes all routes for a service.
func (a *routeRegistryAdapter) RemoveByServiceName(serviceName string) {
	a.rm.RemoveByServiceName(serviceName)
}

// ListRoutes returns all routes, converting from root to discovery types.
func (a *routeRegistryAdapter) ListRoutes() []*disc.Route {
	routes := a.rm.ListRoutes()
	result := make([]*disc.Route, len(routes))

	for i, r := range routes {
		result[i] = rootRouteToDisc(r)
	}

	return result
}

// --- Type conversion helpers ---

// discRouteToRoot converts a discovery.Route to the root Route type.
func discRouteToRoot(r *disc.Route) *Route {
	targets := make([]*Target, len(r.Targets))
	for i, t := range r.Targets {
		targets[i] = &Target{
			ID:       t.ID,
			URL:      t.URL,
			Weight:   t.Weight,
			Healthy:  t.Healthy,
			Tags:     t.Tags,
			Metadata: t.Metadata,
		}
	}

	route := &Route{
		ID:          r.ID,
		Path:        r.Path,
		Methods:     r.Methods,
		Targets:     targets,
		StripPrefix: r.StripPrefix,
		Protocol:    RouteProtocol(r.Protocol),
		Source:      RouteSource(r.Source),
		ServiceName: r.ServiceName,
		Priority:    r.Priority,
		Enabled:     r.Enabled,
		UpdatedAt:   r.UpdatedAt,
		Metadata:    r.Metadata,
	}

	// Map discovery per-route overrides to bastion root types.
	if r.Timeout != nil {
		route.Timeout = &TimeoutConfig{Read: r.Timeout.Read, Write: r.Timeout.Write}
	}
	// A discovery override carrying no usable rate OR no burst is not a rate
	// limit, it is
	// a zero-valued struct that nobody filled in. Honouring it anyway built
	// newTokenBucket(0, 0): no tokens to begin with and no refill, since the
	// bucket only ever gains `elapsed * rate`. Burst alone is enough to do it:
	// it sets maxTokens, and every refill is clamped to maxTokens, so a burst of
	// zero pins the bucket at zero no matter how generous the rate. The route
	// then answered 429 to
	// every request for the life of the process, survived restarts because
	// re-registration restores the same override, and never recovered with
	// time. Skipping it leaves the route unlimited, which is what it was
	// before the empty override arrived.
	if r.RateLimit != nil && r.RateLimit.RequestsPerSec > 0 &&
		r.RateLimit.Burst > 0 {
		route.RateLimit = &middleware.RateLimitConfig{
			Enabled:        true,
			RequestsPerSec: r.RateLimit.RequestsPerSec,
			Burst:          r.RateLimit.Burst,
			PerClient:      r.RateLimit.PerClient,
		}
	}
	if r.Cache != nil {
		route.Cache = &middleware.RouteCacheConfig{
			Enabled: r.Cache.Enabled,
			TTL:     r.Cache.TTL,
			VaryBy:  r.Cache.VaryBy,
		}
	}
	if r.Auth != nil {
		route.Auth = &security.RouteAuthConfig{
			SkipAuth: r.Auth.SkipAuth,
			Scopes:   r.Auth.Scopes,
		}
		if !r.Auth.SkipAuth && len(r.Auth.Scopes) > 0 {
			route.Auth.Enabled = true
		}
	}

	return route
}

// rootRouteToDisc converts a root Route to discovery.Route.
func rootRouteToDisc(r *Route) *disc.Route {
	targets := make([]*disc.Target, len(r.Targets))
	for i, t := range r.Targets {
		targets[i] = &disc.Target{
			ID:       t.ID,
			URL:      t.URL,
			Weight:   t.Weight,
			Healthy:  t.Healthy,
			Tags:     t.Tags,
			Metadata: t.Metadata,
		}
	}

	route := &disc.Route{
		ID:          r.ID,
		Path:        r.Path,
		Methods:     r.Methods,
		Targets:     targets,
		StripPrefix: r.StripPrefix,
		Protocol:    disc.RouteProtocol(r.Protocol),
		Source:      disc.RouteSource(r.Source),
		ServiceName: r.ServiceName,
		Priority:    r.Priority,
		Enabled:     r.Enabled,
		UpdatedAt:   r.UpdatedAt,
		Metadata:    r.Metadata,
	}

	// Round-trip per-route overrides back to discovery types.
	if r.Timeout != nil {
		route.Timeout = &disc.TimeoutOverride{Read: r.Timeout.Read, Write: r.Timeout.Write}
	}
	if r.RateLimit != nil {
		route.RateLimit = &disc.RateLimitOverride{
			RequestsPerSec: r.RateLimit.RequestsPerSec,
			Burst:          r.RateLimit.Burst,
			PerClient:      r.RateLimit.PerClient,
		}
	}
	if r.Cache != nil {
		route.Cache = &disc.CacheOverride{
			Enabled: r.Cache.Enabled,
			TTL:     r.Cache.TTL,
			VaryBy:  r.Cache.VaryBy,
		}
	}
	if r.Auth != nil {
		route.Auth = &disc.AuthOverride{
			SkipAuth: r.Auth.SkipAuth,
			Scopes:   r.Auth.Scopes,
		}
	}

	return route
}
