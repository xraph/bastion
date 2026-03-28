package bastion

import (
	"github.com/xraph/forge"

	disc "github.com/xraph/bastion/discovery"
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

// UpdateRoute converts a discovery.Route to root Route and updates it.
func (a *routeRegistryAdapter) UpdateRoute(route *disc.Route) error {
	return a.rm.UpdateRoute(discRouteToRoot(route))
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

	return &Route{
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
	}
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

	return &disc.Route{
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
	}
}
