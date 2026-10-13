package gateway

import (
	"context"

	"github.com/xraph/forge/extensions/discovery"
	"github.com/xraph/forge/extensions/discovery/backends"

	"github.com/xraph/bastion"
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
