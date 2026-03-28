package extension

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/xraph/bastion"
	"github.com/xraph/forge/extensions/discovery"
	"github.com/xraph/forge/extensions/discovery/backends"
	farpgw "github.com/xraph/forge/farp/gateway"
)

// discoveryAdapter wraps the discovery extension's Service to satisfy
// bastion's DiscoveryService interface.
type discoveryAdapter struct {
	svc *discovery.Service
}

// ListServices returns the names of all discoverable services.
func (a *discoveryAdapter) ListServices(ctx context.Context) ([]string, error) {
	return a.svc.ListServices(ctx)
}

// DiscoverHealthy returns healthy instances, converting discovery.ServiceInstance
// to bastion.ServiceInstanceInfo.
func (a *discoveryAdapter) DiscoverHealthy(ctx context.Context, serviceName string) ([]*bastion.ServiceInstanceInfo, error) {
	instances, err := a.svc.DiscoverHealthy(ctx, serviceName)
	if err != nil {
		return nil, err
	}

	result := make([]*bastion.ServiceInstanceInfo, 0, len(instances))
	for _, inst := range instances {
		result = append(result, convertServiceInstance(inst))
	}

	return result, nil
}

// NewDiscoveryAdapter creates a bastion.DiscoveryService from a discovery.Service.
func NewDiscoveryAdapter(svc *discovery.Service) bastion.DiscoveryService {
	return &discoveryAdapter{svc: svc}
}

// convertServiceInstance converts a backends.ServiceInstance to a bastion.ServiceInstanceInfo.
func convertServiceInstance(inst *backends.ServiceInstance) *bastion.ServiceInstanceInfo {
	return &bastion.ServiceInstanceInfo{
		ID:       inst.ID,
		Name:     inst.Name,
		Version:  inst.Version,
		Address:  inst.Address,
		Port:     inst.Port,
		Tags:     inst.Tags,
		Metadata: inst.Metadata,
		Healthy:  inst.Status == backends.HealthStatusPassing,
	}
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
