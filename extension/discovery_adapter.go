package extension

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/xraph/farp"
	farpdiscovery "github.com/xraph/farp/discovery"
	farpgw "github.com/xraph/farp/gateway"
	"github.com/xraph/forge/extensions/discovery"

	"github.com/xraph/bastion"
	bastiondisc "github.com/xraph/bastion/discovery"
	"github.com/xraph/bastion/gateway"
)

// NewDiscoveryAdapter creates a bastion.DiscoveryService from a discovery.Service.
// It is gateway.NewDiscoveryAdapter, kept here for existing callers.
func NewDiscoveryAdapter(svc *discovery.Service) bastion.DiscoveryService {
	return gateway.NewDiscoveryAdapter(svc)
}

// FARPDiscoveryAdapter wraps a FARP ServiceDiscovery to satisfy bastion's
// DiscoveryService interface. This enables bastion to use FARP's native
// discovery backends directly.
type FARPDiscoveryAdapter struct {
	disc farpdiscovery.ServiceDiscovery
}

// NewFARPDiscoveryAdapter creates a bastion.DiscoveryService from a FARP ServiceDiscovery.
func NewFARPDiscoveryAdapter(disc farpdiscovery.ServiceDiscovery) bastion.DiscoveryService {
	return &FARPDiscoveryAdapter{disc: disc}
}

// ListServices lists all services via FARP's ListableDiscovery interface.
func (a *FARPDiscoveryAdapter) ListServices(ctx context.Context) ([]string, error) {
	if listable, ok := a.disc.(farpdiscovery.ListableDiscovery); ok {
		return listable.ListServices(ctx)
	}

	// Fallback: discover all and extract unique names
	instances, err := a.disc.Discover(ctx, "")
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var names []string
	for _, inst := range instances {
		if _, ok := seen[inst.ServiceName]; !ok {
			seen[inst.ServiceName] = struct{}{}
			names = append(names, inst.ServiceName)
		}
	}

	return names, nil
}

// DiscoverHealthy returns healthy FARP instances as bastion ServiceInstanceInfo.
func (a *FARPDiscoveryAdapter) DiscoverHealthy(ctx context.Context, serviceName string) ([]*bastion.ServiceInstanceInfo, error) {
	instances, err := a.disc.Discover(ctx, serviceName)
	if err != nil {
		return nil, err
	}

	result := make([]*bastion.ServiceInstanceInfo, 0, len(instances))
	for _, inst := range instances {
		if inst.Status == farp.InstanceStatusHealthy || inst.Status == farp.InstanceStatusDegraded {
			result = append(result, bastiondisc.FARPInstanceToInfo(inst))
		}
	}

	return result, nil
}

// ConvertFARPRoutes converts FARP gateway client ServiceRoutes into bastion Routes.
func ConvertFARPRoutes(serviceName string, farpRoutes []farpgw.ServiceRoute, prefix string, stripPrefix bool) []*bastion.Route {
	var routes []*bastion.Route

	for _, fr := range farpRoutes {
		protocol := bastion.ProtocolHTTP

		// Detect protocol from metadata
		if schemaType, ok := fr.Metadata["schema_type"]; ok {
			switch schemaType {
			case "asyncapi":
				protocol = bastion.ProtocolWebSocket
			case "graphql":
				protocol = bastion.ProtocolGraphQL
			}
		}

		// Detect WebSocket from methods
		for _, m := range fr.Methods {
			if strings.EqualFold(m, "WEBSOCKET") {
				protocol = bastion.ProtocolWebSocket

				break
			}
		}

		methods := fr.Methods
		if protocol == bastion.ProtocolWebSocket {
			methods = nil // WebSocket doesn't filter by method
		}

		path := prefix + fr.Path

		route := &bastion.Route{
			ID:          uuid.New().String(),
			Path:        path,
			Methods:     methods,
			StripPrefix: stripPrefix,
			Protocol:    protocol,
			Source:      bastion.SourceFARP,
			ServiceName: serviceName,
			Priority:    20,
			Enabled:     true,
			Targets: []*bastion.Target{
				{
					ID:      uuid.New().String(),
					URL:     fr.TargetURL,
					Weight:  1,
					Healthy: true,
				},
			},
			Metadata: fr.Metadata,
		}

		routes = append(routes, route)
	}

	return routes
}
