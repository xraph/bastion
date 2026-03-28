package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xraph/farp"
	"github.com/xraph/forge"
)

// errManifestNotFound is returned when the remote FARP endpoint returns 404,
// meaning no manifest has been published (normal for consumer-only services).
var errManifestNotFound = errors.New("manifest not found")

// Manager integrates FARP and service discovery for automatic route generation.
type Manager struct {
	config     DiscoveryConfig
	logger     forge.Logger
	rm         RouteRegistry
	service    DiscoveryService
	httpClient *http.Client

	mu              sync.RWMutex
	discoveredSvcs  map[string]*DiscoveredService
	servicePrefixes map[string]string    // FARP-resolved prefix per service name
	serviceLastSeen map[string]time.Time // tracks when each service was last seen in ListServices
	stopCh          chan struct{}
	stopOnce        sync.Once
	started         bool // whether Start() successfully launched the loop goroutine
	wg              sync.WaitGroup

	serviceListenersMu sync.RWMutex
	serviceListeners   []ServiceChangeHook
}

// NewManager creates a new service discovery integration.
func NewManager(
	config DiscoveryConfig,
	logger forge.Logger,
	rm RouteRegistry,
	discService DiscoveryService,
) *Manager {
	fetchTimeout := config.FetchTimeout
	if fetchTimeout == 0 {
		fetchTimeout = 10 * time.Second
	}

	return &Manager{
		config:          config,
		logger:          logger,
		rm:              rm,
		service:         discService,
		httpClient:      &http.Client{Timeout: fetchTimeout},
		discoveredSvcs:  make(map[string]*DiscoveredService),
		servicePrefixes: make(map[string]string),
		serviceLastSeen: make(map[string]time.Time),
		stopCh:          make(chan struct{}),
	}
}

// Start begins service discovery. It is safe to call multiple times;
// only the first call starts the polling loop.
func (sd *Manager) Start(ctx context.Context) error {
	if !sd.config.Enabled || sd.service == nil {
		sd.logger.Info("service discovery disabled or no discovery service available")

		return nil
	}

	// Guard against double-start.
	if sd.started {
		return nil
	}

	sd.logger.Info("starting gateway service discovery",
		forge.F("poll_interval", sd.config.PollInterval),
		forge.F("watch_mode", sd.config.WatchMode),
	)

	// Initial discovery
	if err := sd.refresh(ctx); err != nil {
		sd.logger.Warn("initial service discovery failed", forge.F("error", err))
	}

	// Start polling or watching
	sd.wg.Add(1)
	sd.started = true

	go sd.loop(ctx)

	return nil
}

// Stop stops service discovery. It is safe to call multiple times
// and safe to call when Start() was never invoked.
func (sd *Manager) Stop() {
	if !sd.started {
		return
	}

	sd.stopOnce.Do(func() {
		close(sd.stopCh)
	})
	sd.wg.Wait()
}

// Refresh forces a re-scan of services.
func (sd *Manager) Refresh(ctx context.Context) error {
	return sd.refresh(ctx)
}

// RegisterService accepts a pushed service registration and immediately
// processes it to generate routes. This is called by the gateway's HTTP
// handler when a service pushes its registration info.
func (sd *Manager) RegisterService(ctx context.Context, info *ServiceInstanceInfo) error {
	if info == nil {
		return errors.New("service instance info is required")
	}

	if info.Name == "" {
		return errors.New("service name is required")
	}

	sd.logger.Info("processing pushed service registration",
		forge.F("service_name", info.Name),
		forge.F("service_id", info.ID),
		forge.F("address", info.Address),
		forge.F("port", info.Port),
	)

	sd.processService(info.Name, []*ServiceInstanceInfo{info})
	sd.emitServiceChange(info.Name, true)

	return nil
}

// DeregisterService removes a previously pushed service and all its routes.
func (sd *Manager) DeregisterService(ctx context.Context, serviceName string) {
	sd.logger.Info("deregistering pushed service",
		forge.F("service_name", serviceName),
	)

	sd.mu.Lock()
	delete(sd.discoveredSvcs, serviceName)
	sd.mu.Unlock()

	sd.rm.RemoveByServiceName(serviceName)
	sd.emitServiceChange(serviceName, false)
}

// DiscoveryDebugInfo contains diagnostic data from a discovery probe.
type DiscoveryDebugInfo struct {
	ServiceNames []string                          `json:"service_names"`
	Instances    map[string][]*ServiceInstanceInfo `json:"instances"`
	Config       DiscoveryConfig                   `json:"config"`
	Error        string                            `json:"error,omitempty"`
}

// DebugDiscovery performs a one-shot discovery probe and returns raw results
// without modifying any routes. Useful for diagnosing mDNS issues.
func (sd *Manager) DebugDiscovery(ctx context.Context) *DiscoveryDebugInfo {
	info := &DiscoveryDebugInfo{
		Instances: make(map[string][]*ServiceInstanceInfo),
		Config:    sd.config,
	}

	if sd.service == nil {
		info.Error = "no discovery service configured"
		return info
	}

	names, err := sd.service.ListServices(ctx)
	if err != nil {
		info.Error = fmt.Sprintf("ListServices failed: %v", err)
		return info
	}
	info.ServiceNames = names

	for _, name := range names {
		instances, discErr := sd.service.DiscoverHealthy(ctx, name)
		if discErr != nil {
			info.Instances[name] = nil
			continue
		}
		info.Instances[name] = instances
	}

	return info
}

// DiscoveredServices returns all discovered services.
func (sd *Manager) DiscoveredServices() []*DiscoveredService {
	sd.mu.RLock()
	defer sd.mu.RUnlock()

	services := make([]*DiscoveredService, 0, len(sd.discoveredSvcs))

	for _, svc := range sd.discoveredSvcs {
		services = append(services, svc)
	}

	return services
}

// OnServiceChange registers a callback that fires when a service is
// registered or deregistered. This allows dependent components (e.g.,
// the OpenAPI aggregator) to react immediately to topology changes.
func (sd *Manager) OnServiceChange(fn ServiceChangeHook) {
	sd.serviceListenersMu.Lock()
	defer sd.serviceListenersMu.Unlock()
	sd.serviceListeners = append(sd.serviceListeners, fn)
}

func (sd *Manager) emitServiceChange(serviceName string, registered bool) {
	sd.serviceListenersMu.RLock()
	listeners := make([]ServiceChangeHook, len(sd.serviceListeners))
	copy(listeners, sd.serviceListeners)
	sd.serviceListenersMu.RUnlock()

	for _, fn := range listeners {
		go func(hook ServiceChangeHook) {
			defer func() {
				if r := recover(); r != nil {
					sd.logger.Error("panic in service change hook",
						forge.F("service", serviceName),
						forge.F("panic", fmt.Sprint(r)),
					)
				}
			}()
			hook(serviceName, registered)
		}(fn)
	}
}

func (sd *Manager) loop(ctx context.Context) {
	defer sd.wg.Done()

	ticker := time.NewTicker(sd.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sd.stopCh:
			return
		case <-ticker.C:
			if err := sd.refresh(ctx); err != nil {
				sd.logger.Warn("service discovery refresh failed", forge.F("error", err))
			}
		}
	}
}

func (sd *Manager) refresh(ctx context.Context) error {
	if sd.service == nil {
		return nil
	}

	// List all services
	serviceNames, err := sd.service.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	sd.logger.Debug("discovery refresh: listed services",
		forge.F("count", len(serviceNames)),
		forge.F("names", serviceNames),
	)

	currentServices := make(map[string]bool)

	// Track which services were already known so we only emit change events
	// for genuinely new services, not on every poll cycle.
	sd.mu.RLock()
	alreadyKnown := make(map[string]bool, len(sd.discoveredSvcs))
	for k := range sd.discoveredSvcs {
		alreadyKnown[k] = true
	}
	sd.mu.RUnlock()

	now := time.Now()

	for _, name := range serviceNames {
		if !sd.matchesFilter(name) {
			sd.logger.Debug("discovery refresh: service filtered out",
				forge.F("service", name),
			)
			continue
		}

		// Track that we saw this service in ListServices
		sd.mu.Lock()
		sd.serviceLastSeen[name] = now
		sd.mu.Unlock()

		instances, discErr := sd.service.DiscoverHealthy(ctx, name)
		if discErr != nil {
			sd.logger.Warn("failed to discover service",
				forge.F("service", name),
				forge.F("error", discErr),
			)

			// Keep routes alive — the service is still listed in the
			// discovery backend; we just can't reach its instances right now.
			currentServices[name] = true

			if alreadyKnown[name] {
				sd.mu.Lock()
				if svc, ok := sd.discoveredSvcs[name]; ok {
					svc.Healthy = false
				}
				sd.mu.Unlock()
			}

			continue
		}

		sd.logger.Debug("discovery refresh: discovered instances",
			forge.F("service", name),
			forge.F("instances", len(instances)),
		)

		// Apply tag and metadata filters to instances
		instances = sd.filterInstances(instances)

		if len(instances) == 0 {
			// Service has 0 healthy instances but is still registered.
			// Keep routes alive with unhealthy targets so the proxy returns
			// 503 ("no healthy upstream") instead of 404 ("no matching route").
			currentServices[name] = true

			if alreadyKnown[name] {
				sd.markTargetsUnhealthy(name)
			}

			continue
		}

		currentServices[name] = true

		sd.processService(name, instances)

		// Only emit for genuinely new services, not on every poll tick
		if !alreadyKnown[name] {
			sd.emitServiceChange(name, true)
		}
	}

	// Remove routes for services that are no longer listed at all.
	// Apply a grace period to handle transient discovery backend issues.
	sd.mu.Lock()

	gracePeriod := sd.config.RemovalGracePeriod
	if gracePeriod == 0 {
		gracePeriod = 60 * time.Second
	}

	removedServices := make([]string, 0)

	for name := range sd.discoveredSvcs {
		if !currentServices[name] {
			// Check grace period: only remove if the service has been absent
			// for longer than the configured grace period.
			lastSeen, ok := sd.serviceLastSeen[name]
			if ok && now.Sub(lastSeen) < gracePeriod {
				sd.logger.Debug("service not in current list but within grace period",
					forge.F("service", name),
					forge.F("last_seen", lastSeen),
					forge.F("grace_period", gracePeriod),
				)
				continue
			}

			sd.logger.Info("service deregistered, removing routes",
				forge.F("service", name),
			)

			sd.rm.RemoveByServiceName(name)
			removedServices = append(removedServices, name)
			delete(sd.discoveredSvcs, name)
			delete(sd.serviceLastSeen, name)
		}
	}

	sd.mu.Unlock()

	// Emit service change events outside the lock
	for _, name := range removedServices {
		sd.emitServiceChange(name, false)
	}

	return nil
}

// markTargetsUnhealthy marks all targets as unhealthy for routes belonging to
// the given service. This keeps routes in the route table (so they still
// match requests) but ensures the load balancer returns nil (triggering a 503
// "no healthy upstream" instead of a 404 "no matching route").
func (sd *Manager) markTargetsUnhealthy(serviceName string) {
	routes := sd.rm.ListRoutes()
	for _, route := range routes {
		if route.ServiceName != serviceName {
			continue
		}

		modified := false
		for _, t := range route.Targets {
			if t.Healthy {
				t.Healthy = false
				modified = true
			}
		}

		if modified {
			route.UpdatedAt = time.Now()
			if err := sd.rm.UpdateRoute(route); err != nil {
				sd.logger.Warn("failed to mark route targets unhealthy",
					forge.F("route_id", route.ID),
					forge.F("error", err),
				)
			}
		}
	}

	sd.mu.Lock()
	if svc, ok := sd.discoveredSvcs[serviceName]; ok {
		svc.Healthy = false
	}
	sd.mu.Unlock()

	sd.logger.Info("marked all targets unhealthy for service",
		forge.F("service", serviceName),
	)
}

func (sd *Manager) processService(name string, instances []*ServiceInstanceInfo) {
	if len(instances) == 0 {
		return
	}

	// Build targets from instances
	targets := make([]*Target, 0, len(instances))

	for _, inst := range instances {
		targets = append(targets, &Target{
			ID:       inst.ID,
			URL:      inst.URL("http"),
			Weight:   1,
			Healthy:  inst.IsHealthy(),
			Tags:     inst.Tags,
			Metadata: inst.Metadata,
		})
	}

	// Propagate FARP health path from instance metadata to targets. This is
	// the fallback mechanism when the full manifest is not fetched — the flat
	// metadata key "farp.health" is used instead.
	if healthPath, ok := instances[0].Metadata["farp.health"]; ok && healthPath != "" {
		for _, t := range targets {
			if t.Metadata == nil {
				t.Metadata = make(map[string]string)
			}

			// Only set if not already present (manifest takes precedence).
			if _, exists := t.Metadata["health_check_path"]; !exists {
				t.Metadata["health_check_path"] = healthPath
			}
		}
	}

	// Check if service has FARP manifest
	var protocols []string
	var schemaTypes []string
	capabilities := make([]string, 0)
	var routes []*Route

	firstInstance := instances[0]

	if farpEnabled, ok := firstInstance.Metadata["farp.enabled"]; ok && farpEnabled == "true" {
		// Try to get routes from FARP manifest
		farpRoutes := sd.routesFromFARP(name, firstInstance, targets)
		if len(farpRoutes) > 0 {
			routes = farpRoutes

			for _, r := range routes {
				protocols = append(protocols, string(r.Protocol))

				switch r.Protocol {
				case ProtocolWebSocket, ProtocolSSE:
					schemaTypes = append(schemaTypes, "asyncapi")
				case ProtocolGRPC:
					schemaTypes = append(schemaTypes, "grpc")
				case ProtocolGraphQL:
					schemaTypes = append(schemaTypes, "graphql")
				default:
					schemaTypes = append(schemaTypes, "openapi")
				}
			}
		}

		// Extract capabilities from FARP metadata
		if caps, ok := firstInstance.Metadata["farp.capabilities"]; ok && caps != "" {
			for _, c := range strings.Split(caps, ",") {
				c = strings.TrimSpace(c)
				if c != "" {
					capabilities = append(capabilities, c)
				}
			}
		}
	}

	// Fallback: create a catch-all route for the service
	if len(routes) == 0 {
		prefix := sd.BuildPrefix(name)

		route := &Route{
			ID:          fmt.Sprintf("discovery-%s", name),
			Path:        prefix + "/*",
			Targets:     targets,
			StripPrefix: sd.config.StripPrefix,
			Protocol:    ProtocolHTTP,
			Source:      SourceDiscovery,
			ServiceName: name,
			Priority:    10,
			Enabled:     true,
		}

		routes = []*Route{route}
		protocols = []string{"http"}
	}

	// Build a set of the new route IDs so we can detect stale routes.
	newRouteIDs := make(map[string]bool, len(routes))
	for _, route := range routes {
		newRouteIDs[route.ID] = true
	}

	// Add or update routes FIRST, before removing stale ones. This ensures
	// that at every point in time there is at least one matchable route for
	// the service — no window where all routes are removed but replacements
	// haven't been added yet.
	for _, route := range routes {
		existing, ok := sd.rm.GetRoute(route.ID)
		if ok {
			// Propagate all fields from the freshly generated route so that
			// changes in the FARP manifest (path, methods, protocol, etc.)
			// are reflected without requiring a gateway restart.
			existing.Path = route.Path
			existing.Methods = route.Methods
			existing.Targets = targets
			existing.StripPrefix = route.StripPrefix
			existing.Protocol = route.Protocol
			existing.Priority = route.Priority
			existing.Enabled = route.Enabled
			existing.UpdatedAt = time.Now()

			if err := sd.rm.UpdateRoute(existing); err != nil {
				sd.logger.Warn("failed to update discovered route",
					forge.F("route_id", route.ID),
					forge.F("error", err),
				)
			}
		} else {
			if err := sd.rm.AddRoute(route); err != nil {
				sd.logger.Warn("failed to add discovered route",
					forge.F("route_id", route.ID),
					forge.F("error", err),
				)
			}
		}
	}

	// Remove stale routes AFTER new ones are in place: routes that previously
	// belonged to this service but are no longer in the freshly generated set
	// (e.g., the service dropped a protocol like GraphQL or WebSocket on
	// restart). Doing this after add/update guarantees the new routes are
	// already serving traffic before the old ones are removed.
	for _, existing := range sd.rm.ListRoutes() {
		if existing.ServiceName == name && !newRouteIDs[existing.ID] {
			if err := sd.rm.RemoveRoute(existing.ID); err != nil {
				sd.logger.Warn("failed to remove stale route",
					forge.F("route_id", existing.ID),
					forge.F("service", name),
					forge.F("error", err),
				)
			}
		}
	}

	// Update discovered service info
	sd.mu.Lock()
	sd.discoveredSvcs[name] = &DiscoveredService{
		Name:         name,
		Version:      firstInstance.Version,
		Address:      firstInstance.Address,
		Port:         firstInstance.Port,
		Protocols:    unique(protocols),
		SchemaTypes:  unique(schemaTypes),
		Capabilities: capabilities,
		Healthy:      firstInstance.IsHealthy(),
		Metadata:     firstInstance.Metadata,
		RouteCount:   len(routes),
		DiscoveredAt: time.Now(),
	}
	sd.mu.Unlock()
}

func (sd *Manager) routesFromFARP(serviceName string, instance *ServiceInstanceInfo, targets []*Target) []*Route {
	// 1. Try fetching the full FARP manifest for rich routing metadata.
	if manifestURL, ok := instance.Metadata["farp.manifest"]; ok && manifestURL != "" && sd.httpClient != nil {
		timeout := sd.httpClient.Timeout
		if timeout == 0 {
			timeout = 10 * time.Second
		}

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		manifest, err := sd.fetchManifest(ctx, manifestURL)
		if err == nil && manifest != nil {
			routes := sd.routesFromManifest(serviceName, manifest, targets)
			if len(routes) > 0 {
				// Enrich instance metadata with manifest endpoint URLs so
				// downstream consumers (e.g. OpenAPI aggregator) can find
				// spec URLs even when the mDNS metadata didn't include them.
				sd.enrichMetadataFromManifest(instance, manifest)

				if sd.logger != nil {
					sd.logger.Debug("bastion: routes from FARP manifest",
						forge.F("service", serviceName),
						forge.F("routes", len(routes)),
					)
				}

				return routes
			}
		} else if err != nil {
			if sd.logger != nil {
				// 404 / "not found" is normal for consumer-only services that
				// don't publish a manifest — log at DEBUG, not WARN.
				if errors.Is(err, errManifestNotFound) {
					sd.logger.Debug("bastion: no FARP manifest published, falling back to metadata",
						forge.F("service", serviceName),
					)
				} else {
					sd.logger.Warn("bastion: failed to fetch FARP manifest, falling back to metadata",
						forge.F("service", serviceName),
						forge.F("url", manifestURL),
						forge.F("error", err),
					)
				}
			}
		}
	}

	// 2. Fallback: use flat FARP metadata keys for route generation.
	return sd.routesFromFARPMetadata(serviceName, instance, targets)
}

// routesFromFARPMetadata creates routes from flat FARP metadata keys
// (farp.openapi, farp.asyncapi, farp.graphql). This is the fallback path
// when the full FARP manifest is unavailable.
func (sd *Manager) routesFromFARPMetadata(serviceName string, instance *ServiceInstanceInfo, targets []*Target) []*Route {
	var routes []*Route
	prefix := sd.BuildPrefix(serviceName)

	if openapiEndpoint, ok := instance.Metadata["farp.openapi"]; ok && openapiEndpoint != "" {
		route := &Route{
			ID:          fmt.Sprintf("farp-%s-http", serviceName),
			Path:        prefix + "/*",
			Targets:     targets,
			StripPrefix: sd.config.StripPrefix,
			Protocol:    ProtocolHTTP,
			Source:      SourceFARP,
			ServiceName: serviceName,
			Priority:    20,
			Enabled:     true,
		}

		routes = append(routes, route)
	}

	if _, ok := instance.Metadata["farp.asyncapi"]; ok {
		route := &Route{
			ID:          fmt.Sprintf("farp-%s-ws", serviceName),
			Path:        prefix + "/ws/*",
			Targets:     targets,
			StripPrefix: sd.config.StripPrefix,
			Protocol:    ProtocolWebSocket,
			Source:      SourceFARP,
			ServiceName: serviceName,
			Priority:    20,
			Enabled:     true,
		}

		routes = append(routes, route)
	}

	if _, ok := instance.Metadata["farp.graphql"]; ok {
		route := &Route{
			ID:          fmt.Sprintf("farp-%s-graphql", serviceName),
			Path:        prefix + "/graphql",
			Methods:     []string{"GET", "POST"},
			Targets:     targets,
			StripPrefix: sd.config.StripPrefix,
			Protocol:    ProtocolGraphQL,
			Source:      SourceFARP,
			ServiceName: serviceName,
			Priority:    20,
			Enabled:     true,
		}

		routes = append(routes, route)
	}

	return routes
}

// fetchManifest fetches and parses a FARP SchemaManifest from the given URL.
func (sd *Manager) fetchManifest(ctx context.Context, manifestURL string) (*farp.SchemaManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := sd.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errManifestNotFound
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var manifest farp.SchemaManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}

	return &manifest, nil
}

// routesFromManifest converts a FARP SchemaManifest into bastion routes,
// honouring the manifest's RoutingConfig for prefix strategy, priority,
// strip-prefix, and tags.
func (sd *Manager) routesFromManifest(serviceName string, manifest *farp.SchemaManifest, targets []*Target) []*Route {
	// Determine the path prefix based on the manifest's routing strategy.
	prefix := sd.prefixFromStrategy(serviceName, manifest)

	// Cache the resolved prefix so the OpenAPI aggregator can use it
	// instead of recomputing via BuildPrefix (which ignores the strategy).
	sd.mu.Lock()
	sd.servicePrefixes[serviceName] = prefix
	sd.mu.Unlock()

	// Determine priority (use manifest's if set, otherwise default 20).
	priority := manifest.Routing.Priority
	if priority == 0 {
		priority = 20
	}

	// StripPrefix: use manifest's setting, with fallback to discovery config.
	stripPrefix := sd.config.StripPrefix
	if manifest.Routing.StripPrefix {
		stripPrefix = true
	}

	// Propagate the FARP health endpoint to targets so that the health
	// monitor can use a service-specific health path instead of the global
	// default (e.g. "/_/health").
	if manifest.Endpoints.Health != "" {
		for _, t := range targets {
			if t.Metadata == nil {
				t.Metadata = make(map[string]string)
			}

			t.Metadata["health_check_path"] = manifest.Endpoints.Health
		}
	}

	var routes []*Route
	hasHTTP := false

	// Create routes from schema descriptors in the manifest.
	for _, schema := range manifest.Schemas {
		switch schema.Type {
		case farp.SchemaTypeOpenAPI, farp.SchemaTypeORPC:
			if !hasHTTP {
				route := &Route{
					ID:          fmt.Sprintf("farp-%s-http", serviceName),
					Path:        prefix + "/*",
					Targets:     targets,
					StripPrefix: stripPrefix,
					Protocol:    ProtocolHTTP,
					Source:      SourceFARP,
					ServiceName: serviceName,
					Priority:    priority,
					Enabled:     true,
				}
				routes = append(routes, route)
				hasHTTP = true
			}

		case farp.SchemaTypeAsyncAPI:
			route := &Route{
				ID:          fmt.Sprintf("farp-%s-ws", serviceName),
				Path:        prefix + "/ws/*",
				Targets:     targets,
				StripPrefix: stripPrefix,
				Protocol:    ProtocolWebSocket,
				Source:      SourceFARP,
				ServiceName: serviceName,
				Priority:    priority,
				Enabled:     true,
			}
			routes = append(routes, route)

		case farp.SchemaTypeGraphQL:
			route := &Route{
				ID:          fmt.Sprintf("farp-%s-graphql", serviceName),
				Path:        prefix + "/graphql",
				Methods:     []string{"GET", "POST"},
				Targets:     targets,
				StripPrefix: stripPrefix,
				Protocol:    ProtocolGraphQL,
				Source:      SourceFARP,
				ServiceName: serviceName,
				Priority:    priority,
				Enabled:     true,
			}
			routes = append(routes, route)

		case farp.SchemaTypeGRPC:
			route := &Route{
				ID:          fmt.Sprintf("farp-%s-grpc", serviceName),
				Path:        prefix + "/*",
				Targets:     targets,
				StripPrefix: stripPrefix,
				Protocol:    ProtocolGRPC,
				Source:      SourceFARP,
				ServiceName: serviceName,
				Priority:    priority,
				Enabled:     true,
			}
			routes = append(routes, route)
		}
	}

	// If no schemas but endpoints hint at capabilities, create routes from endpoints.
	if len(routes) == 0 {
		if manifest.Endpoints.OpenAPI != "" {
			route := &Route{
				ID:          fmt.Sprintf("farp-%s-http", serviceName),
				Path:        prefix + "/*",
				Targets:     targets,
				StripPrefix: stripPrefix,
				Protocol:    ProtocolHTTP,
				Source:      SourceFARP,
				ServiceName: serviceName,
				Priority:    priority,
				Enabled:     true,
			}
			routes = append(routes, route)
		}

		if manifest.Endpoints.AsyncAPI != "" {
			route := &Route{
				ID:          fmt.Sprintf("farp-%s-ws", serviceName),
				Path:        prefix + "/ws/*",
				Targets:     targets,
				StripPrefix: stripPrefix,
				Protocol:    ProtocolWebSocket,
				Source:      SourceFARP,
				ServiceName: serviceName,
				Priority:    priority,
				Enabled:     true,
			}
			routes = append(routes, route)
		}

		if manifest.Endpoints.GraphQL != "" {
			route := &Route{
				ID:          fmt.Sprintf("farp-%s-graphql", serviceName),
				Path:        prefix + "/graphql",
				Methods:     []string{"GET", "POST"},
				Targets:     targets,
				StripPrefix: stripPrefix,
				Protocol:    ProtocolGraphQL,
				Source:      SourceFARP,
				ServiceName: serviceName,
				Priority:    priority,
				Enabled:     true,
			}
			routes = append(routes, route)
		}

		if manifest.Endpoints.GRPCReflection {
			route := &Route{
				ID:          fmt.Sprintf("farp-%s-grpc", serviceName),
				Path:        prefix + "/*",
				Targets:     targets,
				StripPrefix: stripPrefix,
				Protocol:    ProtocolGRPC,
				Source:      SourceFARP,
				ServiceName: serviceName,
				Priority:    priority,
				Enabled:     true,
			}
			routes = append(routes, route)
		}
	}

	return routes
}

// enrichMetadataFromManifest propagates the manifest's endpoint URLs into
// the service instance metadata. This ensures downstream consumers (like
// the OpenAPI aggregator) can locate spec URLs even when the mDNS
// metadata keys were not explicitly configured by the service.
func (sd *Manager) enrichMetadataFromManifest(instance *ServiceInstanceInfo, manifest *farp.SchemaManifest) {
	if instance.Metadata == nil {
		instance.Metadata = make(map[string]string)
	}

	baseURL := instance.URL("http")

	if manifest.Endpoints.OpenAPI != "" {
		if _, exists := instance.Metadata["farp.openapi"]; !exists {
			instance.Metadata["farp.openapi"] = baseURL + manifest.Endpoints.OpenAPI
			instance.Metadata["farp.openapi.path"] = manifest.Endpoints.OpenAPI
		}
	}

	if manifest.Endpoints.AsyncAPI != "" {
		if _, exists := instance.Metadata["farp.asyncapi"]; !exists {
			instance.Metadata["farp.asyncapi"] = baseURL + manifest.Endpoints.AsyncAPI
			instance.Metadata["farp.asyncapi.path"] = manifest.Endpoints.AsyncAPI
		}
	}

	if manifest.Endpoints.GraphQL != "" {
		if _, exists := instance.Metadata["farp.graphql"]; !exists {
			instance.Metadata["farp.graphql"] = baseURL + manifest.Endpoints.GraphQL
			instance.Metadata["farp.graphql.path"] = manifest.Endpoints.GraphQL
		}
	}

	if manifest.Endpoints.Health != "" {
		if _, exists := instance.Metadata["farp.health"]; !exists {
			instance.Metadata["farp.health"] = manifest.Endpoints.Health
		}
	}
}

// prefixFromStrategy determines the route prefix based on the manifest's
// RoutingConfig strategy. PrefixOverrides take highest precedence.
func (sd *Manager) prefixFromStrategy(serviceName string, manifest *farp.SchemaManifest) string {
	// PrefixOverrides take precedence over everything.
	if prefix, ok := sd.lookupPrefixOverride(serviceName); ok {
		return prefix
	}

	switch manifest.Routing.Strategy {
	case farp.MountStrategyRoot:
		// Mount at root or at base path
		if manifest.Routing.BasePath != "" {
			return strings.TrimRight(manifest.Routing.BasePath, "/")
		}

		return ""

	case farp.MountStrategyService:
		return "/" + normalizeServiceName(serviceName)

	case farp.MountStrategyVersioned:
		version := manifest.ServiceVersion
		if version == "" {
			version = "v1"
		}

		return "/" + normalizeServiceName(serviceName) + "/" + version

	case farp.MountStrategyCustom:
		if manifest.Routing.BasePath != "" {
			return strings.TrimRight(manifest.Routing.BasePath, "/")
		}

		return sd.BuildPrefix(serviceName)

	case farp.MountStrategyInstance:
		if manifest.InstanceID != "" {
			return "/" + manifest.InstanceID
		}

		return sd.BuildPrefix(serviceName)

	case farp.MountStrategySubdomain:
		// Subdomain strategy is not directly supported in path-based routing;
		// fall back to the default prefix.
		return sd.BuildPrefix(serviceName)

	default:
		// Unknown or unset strategy: use the discovery config's prefix template.
		return sd.BuildPrefix(serviceName)
	}
}

// lookupPrefixOverride checks PrefixOverrides for a service-specific prefix.
// Returns the prefix and true if found, or ("", false) if not overridden.
func (sd *Manager) lookupPrefixOverride(serviceName string) (string, bool) {
	if len(sd.config.PrefixOverrides) == 0 {
		return "", false
	}

	// Try exact match first, then case-insensitive.
	if prefix, ok := sd.config.PrefixOverrides[serviceName]; ok {
		return strings.TrimRight(prefix, "/"), true
	}

	lower := strings.ToLower(serviceName)
	for k, v := range sd.config.PrefixOverrides {
		if strings.ToLower(k) == lower {
			return strings.TrimRight(v, "/"), true
		}
	}

	return "", false
}

// BuildPrefix builds the route prefix for a service name.
// The service name is normalized to lowercase kebab-case for URL friendliness.
// PrefixOverrides take precedence over AutoPrefix and PrefixTemplate.
func (sd *Manager) BuildPrefix(serviceName string) string {
	// PrefixOverrides take precedence.
	if prefix, ok := sd.lookupPrefixOverride(serviceName); ok {
		return prefix
	}

	if !sd.config.AutoPrefix {
		return ""
	}

	tmpl := sd.config.PrefixTemplate
	if tmpl == "" {
		tmpl = "/{{.ServiceName}}"
	}

	return strings.ReplaceAll(tmpl, "{{.ServiceName}}", normalizeServiceName(serviceName))
}

// GetServicePrefix returns the resolved route prefix for a discovered service.
// It checks in order: PrefixOverrides → cached FARP prefix → BuildPrefix.
func (sd *Manager) GetServicePrefix(serviceName string) string {
	// PrefixOverrides always win.
	if prefix, ok := sd.lookupPrefixOverride(serviceName); ok {
		return prefix
	}

	// Check FARP-resolved cache.
	sd.mu.RLock()
	if prefix, ok := sd.servicePrefixes[serviceName]; ok {
		sd.mu.RUnlock()
		return prefix
	}
	sd.mu.RUnlock()

	return sd.BuildPrefix(serviceName)
}

// normalizeServiceName converts a service name to a URL-friendly form:
// lowercase, spaces and underscores replaced with hyphens.
func normalizeServiceName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")

	return name
}

func (sd *Manager) matchesFilter(serviceName string) bool {
	if len(sd.config.ServiceFilters) == 0 {
		return true
	}

	for _, filter := range sd.config.ServiceFilters {
		if len(filter.IncludeNames) > 0 {
			found := false

			for _, name := range filter.IncludeNames {
				if matchWildcard(serviceName, name) {
					found = true

					break
				}
			}

			if !found {
				return false
			}
		}

		for _, name := range filter.ExcludeNames {
			if matchWildcard(serviceName, name) {
				return false
			}
		}
	}

	return true
}

func (sd *Manager) filterInstances(instances []*ServiceInstanceInfo) []*ServiceInstanceInfo {
	if len(sd.config.ServiceFilters) == 0 {
		return instances
	}

	filtered := make([]*ServiceInstanceInfo, 0, len(instances))

	for _, inst := range instances {
		if sd.instanceMatchesFilters(inst) {
			filtered = append(filtered, inst)
		}
	}

	return filtered
}

func (sd *Manager) instanceMatchesFilters(inst *ServiceInstanceInfo) bool {
	for _, filter := range sd.config.ServiceFilters {
		if len(filter.IncludeTags) > 0 && !hasAllTags(inst.Tags, filter.IncludeTags) {
			return false
		}

		if len(filter.ExcludeTags) > 0 && hasAnyTag(inst.Tags, filter.ExcludeTags) {
			return false
		}

		if len(filter.RequireMetadata) > 0 && !matchesRequiredMetadata(inst.Metadata, filter.RequireMetadata) {
			return false
		}
	}

	return true
}

func hasAllTags(tags []string, required []string) bool {
	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[t] = true
	}

	for _, r := range required {
		if !tagSet[r] {
			return false
		}
	}

	return true
}

func hasAnyTag(tags []string, check []string) bool {
	tagSet := make(map[string]bool, len(tags))
	for _, t := range tags {
		tagSet[t] = true
	}

	for _, c := range check {
		if tagSet[c] {
			return true
		}
	}

	return false
}

func matchesRequiredMetadata(metadata map[string]string, required map[string]string) bool {
	for k, v := range required {
		if actual, ok := metadata[k]; !ok || actual != v {
			return false
		}
	}

	return true
}

func matchWildcard(s, pattern string) bool {
	if pattern == "*" {
		return true
	}

	if strings.Contains(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")

		return strings.HasPrefix(s, prefix)
	}

	return s == pattern
}

func unique(ss []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(ss))

	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}

	return result
}
