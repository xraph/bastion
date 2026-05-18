package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xraph/farp"
	"github.com/xraph/farp/merger"
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
	config        OpenAPIConfig
	logger        forge.Logger
	rm            RouteRegistry
	disc          *Manager
	httpClient    *http.Client
	schemaFetcher *SchemaFetcher

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
	httpClient := &http.Client{
		Timeout: config.FetchTimeout,
	}

	return &OpenAPIAggregator{
		config:        config,
		logger:        logger,
		rm:            rm,
		disc:          disc,
		httpClient:    httpClient,
		schemaFetcher: NewSchemaFetcher(httpClient, logger),
		serviceSpecs:  make(map[string]*ServiceOpenAPISpec),
		refreshCh:     make(chan struct{}, 1),
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

	// Fetch specs in parallel with concurrency limit and cycle timeout.
	const maxConcurrentSpecFetches = 10

	refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
	defer refreshCancel()

	// Collect schema sources: FARP manifests (preferred) + fallback metadata-based services.
	type schemaSource struct {
		ServiceName string
		Version     string
		Manifest    *farp.SchemaManifest // nil for non-FARP services
		Descriptor  *farp.SchemaDescriptor
		SpecURL     string // fallback URL for non-FARP services
	}

	var sources []schemaSource
	currentServiceSet := make(map[string]bool)

	// 1. Get schemas from cached FARP manifests (preferred path).
	if oa.disc != nil {
		manifests := oa.disc.ServiceManifests()
		for name, manifest := range manifests {
			if oa.isExcluded(name) {
				continue
			}
			currentServiceSet[name] = true

			// Find OpenAPI schema descriptor in manifest.
			for i := range manifest.Schemas {
				desc := &manifest.Schemas[i]
				if desc.Type == farp.SchemaTypeOpenAPI || desc.Type == farp.SchemaTypeORPC {
					sources = append(sources, schemaSource{
						ServiceName: name,
						Version:     manifest.ServiceVersion,
						Manifest:    manifest,
						Descriptor:  desc,
					})
					break // one OpenAPI descriptor per service
				}
			}

			// If manifest has no OpenAPI schema descriptor but has an OpenAPI endpoint,
			// create a source from the endpoint URL.
			if manifest.Endpoints.OpenAPI != "" {
				hasOpenAPISource := false
				for _, s := range sources {
					if s.ServiceName == name {
						hasOpenAPISource = true
						break
					}
				}
				if !hasOpenAPISource {
					// Build URL from first discovered service instance.
					svcs := oa.disc.DiscoveredServices()
					for _, svc := range svcs {
						if svc.Name == name {
							specURL := fmt.Sprintf("http://%s:%d%s", svc.Address, svc.Port, manifest.Endpoints.OpenAPI)
							sources = append(sources, schemaSource{
								ServiceName: name,
								Version:     manifest.ServiceVersion,
								Manifest:    manifest,
								SpecURL:     specURL,
							})
							break
						}
					}
				}
			}
		}
	}

	// Track which services actually got a source from their manifest.
	hasSource := make(map[string]bool)
	for _, s := range sources {
		hasSource[s.ServiceName] = true
	}

	// 2. Fallback: discover services with OpenAPI endpoints from flat metadata.
	//    This covers services that:
	//    - Don't have a cached manifest (manifest fetch failed)
	//    - Have a cached manifest but no OpenAPI schema descriptor or endpoint
	//    - Were never FARP-enabled (only flat metadata)
	legacyServices := oa.discoverOpenAPIServices()
	for _, svc := range legacyServices {
		if hasSource[svc.Name] {
			continue // already has a source from manifest
		}
		currentServiceSet[svc.Name] = true
		sources = append(sources, schemaSource{
			ServiceName: svc.Name,
			Version:     svc.Version,
			SpecURL:     svc.SpecURL,
		})
	}

	if len(sources) == 0 {
		oa.logger.Debug("OpenAPI: no schema sources found, skipping refresh")
		return
	}

	oa.logger.Debug("OpenAPI: refreshing schemas",
		forge.F("sources", len(sources)),
	)

	// Fetch all schemas in parallel.
	var wg sync.WaitGroup
	type fetchResult struct {
		source schemaSource
		spec   *ServiceOpenAPISpec
		schema map[string]any
	}
	resultCh := make(chan fetchResult, len(sources))
	sem := make(chan struct{}, maxConcurrentSpecFetches)

	for _, src := range sources {
		wg.Add(1)
		sem <- struct{}{} // acquire

		go func(s schemaSource) {
			defer wg.Done()
			defer func() { <-sem }() // release

			var fetched *FetchedSchema
			if s.Descriptor != nil {
				// Use schema descriptor (supports inline + HTTP).
				fetched = oa.schemaFetcher.FetchSchema(refreshCtx, *s.Descriptor, s.ServiceName)
			} else if s.SpecURL != "" {
				// Fallback to direct URL fetch.
				fetched = oa.schemaFetcher.FetchFromURL(refreshCtx, s.SpecURL, s.ServiceName)
			}

			if fetched != nil && !fetched.Healthy {
				// Single retry after a short backoff for transient failures.
				time.Sleep(500 * time.Millisecond)
				var retry *FetchedSchema
				if s.Descriptor != nil {
					retry = oa.schemaFetcher.FetchSchema(refreshCtx, *s.Descriptor, s.ServiceName)
				} else if s.SpecURL != "" {
					retry = oa.schemaFetcher.FetchFromURL(refreshCtx, s.SpecURL, s.ServiceName)
				}
				if retry != nil && retry.Healthy {
					fetched = retry
				}
			}

			if fetched == nil {
				return
			}

			specURL := s.SpecURL
			if s.Descriptor != nil && s.Descriptor.Location.URL != "" {
				specURL = s.Descriptor.Location.URL
			}

			resultCh <- fetchResult{
				source: s,
				spec: &ServiceOpenAPISpec{
					ServiceName: s.ServiceName,
					Version:     s.Version,
					SpecURL:     specURL,
					Spec:        fetched.Schema,
					FetchedAt:   fetched.FetchedAt,
					Error:       fetched.Error,
					Healthy:     fetched.Healthy,
					PathCount:   fetched.PathCount,
				},
				schema: fetched.Schema,
			}
		}(src)
	}

	wg.Wait()
	close(resultCh)

	// Collect results.
	newSpecs := make(map[string]*ServiceOpenAPISpec)
	var mergerInputs []fetchResult
	for r := range resultCh {
		newSpecs[r.source.ServiceName] = r.spec
		if r.spec.Healthy && r.schema != nil {
			mergerInputs = append(mergerInputs, r)
		}
	}

	// Retain previously cached specs when a fresh fetch returns fewer paths.
	oa.mu.RLock()
	prevSpecs := oa.serviceSpecs
	oa.mu.RUnlock()

	if prevSpecs != nil {
		for name, newSpec := range newSpecs {
			prev, ok := prevSpecs[name]
			if !ok || !prev.Healthy {
				continue
			}
			if !newSpec.Healthy {
				oa.logger.Debug("OpenAPI: keeping cached spec (new fetch failed)",
					forge.F("service", name),
					forge.F("error", newSpec.Error),
				)
				newSpecs[name] = prev
				continue
			}
			if newSpec.PathCount < prev.PathCount {
				oa.logger.Debug("OpenAPI: keeping cached spec (new has fewer paths)",
					forge.F("service", name),
					forge.F("prev_paths", prev.PathCount),
					forge.F("new_paths", newSpec.PathCount),
				)
				newSpecs[name] = prev
			}
		}

		// Evict cached specs for services that are no longer discovered.
		for name := range prevSpecs {
			if !currentServiceSet[name] {
				oa.logger.Debug("OpenAPI: evicting spec for removed service",
					forge.F("service", name),
				)
				delete(newSpecs, name)
			}
		}
	}

	// Build merged spec using FARP merger.
	merged := oa.buildMergedSpec(newSpecs)

	// Serialize to JSON.
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		oa.logger.Error("failed to serialize merged OpenAPI spec", forge.F("error", err))
		return
	}

	// Atomically update.
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

// buildMergedSpec builds a unified OpenAPI 3.1.0 spec from all service specs
// using the FARP merger package for proper conflict resolution, component
// prefixing, and routing strategy application.
func (oa *OpenAPIAggregator) buildMergedSpec(specs map[string]*ServiceOpenAPISpec) map[string]any {
	// Map bastion config to FARP merger config.
	defaultStrategy := farp.ConflictStrategyPrefix
	if oa.config.MergeStrategy == "flat" {
		defaultStrategy = farp.ConflictStrategyOverwrite
	}

	// Disable the FARP merger's tag handling — bastion applies its own tag logic
	// in post-processing (service tags, ServiceTagOnly, DisableServiceTags).
	// Letting the merger handle tags causes tag prefixing that conflicts with
	// bastion's conventions.
	mergerConfig := merger.MergerConfig{
		DefaultConflictStrategy: defaultStrategy,
		MergedTitle:             oa.config.Title,
		MergedDescription:       oa.config.Description,
		MergedVersion:           oa.config.Version,
		IncludeServiceTags:      false,
		CollapseServiceTags:     false,
		SortOutput:              true,
	}

	// Build merger inputs from cached specs + manifests.
	var serviceSchemas []merger.ServiceSchema
	serviceNames := make([]string, 0, len(specs))
	for name := range specs {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)

	for _, name := range serviceNames {
		svcSpec := specs[name]
		if svcSpec.Spec == nil || svcSpec.Error != "" {
			continue
		}

		// Get the cached FARP manifest for routing strategy, or build a synthetic one.
		manifest := oa.getOrBuildManifest(name, svcSpec)

		serviceSchemas = append(serviceSchemas, merger.ServiceSchema{
			Manifest: manifest,
			Schema:   svcSpec.Spec,
		})
	}

	if len(serviceSchemas) == 0 {
		return oa.buildEmptySpec()
	}

	// Run the FARP merger.
	m := merger.NewMerger(mergerConfig)
	result, err := m.Merge(serviceSchemas)
	if err != nil {
		oa.logger.Error("FARP merger failed, falling back to empty spec",
			forge.F("error", err),
		)
		return oa.buildEmptySpec()
	}

	// Log any conflicts.
	if len(result.Conflicts) > 0 {
		oa.logger.Debug("OpenAPI merge conflicts",
			forge.F("conflicts", len(result.Conflicts)),
		)
	}

	// Convert the typed merger result to map[string]any for JSON serialization.
	merged := oa.mergerResultToMap(result)

	// Post-processing: apply bastion-specific extensions.

	// 0. Tag handling — the FARP merger always prefixes tags with the service name,
	//    but bastion has its own tag conventions (DisableServiceTags, ServiceTagOnly).
	//    We restore original tags from the source specs, then apply bastion's logic.
	if paths, ok := merged["paths"].(map[string]any); ok {
		for path, pathItem := range paths {
			// Determine which service owns this path and restore original tags.
			ownerService := ""
			originalPath := ""
			for _, name := range serviceNames {
				svc := specs[name]
				if svc == nil || svc.Spec == nil {
					continue
				}

				// Check if the merged path corresponds to a path in this service's spec.
				prefix := ""
				if oa.disc != nil {
					prefix = oa.disc.GetServicePrefix(name)
				}
				if prefix == "" {
					prefix = "/" + normalizeServiceName(name)
				}

				upstreamPath := path
				if prefix != "" && strings.HasPrefix(path, prefix) {
					upstreamPath = strings.TrimPrefix(path, prefix)
					if upstreamPath == "" {
						upstreamPath = "/"
					}
				}

				svcPaths, _ := svc.Spec["paths"].(map[string]any)
				if _, ok := svcPaths[upstreamPath]; ok {
					ownerService = name
					originalPath = upstreamPath
					break
				}
				// Also try the full path for root-mounted services.
				if _, ok := svcPaths[path]; ok {
					ownerService = name
					originalPath = path
					break
				}
			}

			if ownerService == "" {
				continue
			}

			// Restore original tags from source spec, then apply bastion's tag policy.
			svc := specs[ownerService]
			svcPaths, _ := svc.Spec["paths"].(map[string]any)
			if origPathItem, ok := svcPaths[originalPath]; ok {
				restoreOriginalTags(pathItem, origPathItem)
			}

			if !oa.config.DisableServiceTags {
				if oa.config.ServiceTagOnly {
					paths[path] = replaceOperationTags(pathItem, ownerService)
				} else {
					paths[path] = tagOperations(pathItem, ownerService)
				}
			}
		}
	}

	// Add top-level service tags.
	if !oa.config.DisableServiceTags {
		tags, _ := merged["tags"].([]any)
		tagSet := make(map[string]bool)
		for _, t := range tags {
			if tm, ok := t.(map[string]any); ok {
				if name, ok := tm["name"].(string); ok {
					tagSet[name] = true
				}
			}
		}

		for _, name := range serviceNames {
			svcSpec := specs[name]
			if svcSpec == nil || svcSpec.Spec == nil || svcSpec.Error != "" {
				continue
			}

			if !tagSet[name] {
				tagDesc := fmt.Sprintf("Operations from %s", name)
				if svcSpec.Version != "" {
					tagDesc += " v" + svcSpec.Version
				}
				tags = append(tags, map[string]any{
					"name":        name,
					"description": tagDesc,
				})
				tagSet[name] = true
			}
		}
		merged["tags"] = tags
	}

	// 1. Extension path filtering — remove excluded extension paths.
	//    Extension filters work on upstream (unprefixed) paths, so we need to
	//    strip the service prefix before checking.
	if paths, ok := merged["paths"].(map[string]any); ok {
		for path := range paths {
			for _, name := range serviceNames {
				// Determine the service prefix to strip.
				prefix := ""
				if oa.disc != nil {
					prefix = oa.disc.GetServicePrefix(name)
				}
				if prefix == "" {
					// Fallback: try the default service name prefix.
					prefix = "/" + normalizeServiceName(name)
				}

				// Strip the service prefix to get the upstream path.
				upstreamPath := path
				if prefix != "" && strings.HasPrefix(path, prefix) {
					upstreamPath = strings.TrimPrefix(path, prefix)
					if upstreamPath == "" {
						upstreamPath = "/"
					}
				}
				if oa.isExtensionPathExcluded(name, upstreamPath) {
					delete(paths, path)
					break
				}
			}
		}
	}

	// 2. Sanitize operations (strip requestBody from GET/HEAD/DELETE).
	if paths, ok := merged["paths"].(map[string]any); ok {
		for path, pathItem := range paths {
			paths[path] = sanitizeOperations(pathItem)
		}
	}

	// 3. Add contact info.
	if oa.config.ContactName != "" || oa.config.ContactEmail != "" {
		if info, ok := merged["info"].(map[string]any); ok {
			contact := make(map[string]any)
			if oa.config.ContactName != "" {
				contact["name"] = oa.config.ContactName
			}
			if oa.config.ContactEmail != "" {
				contact["email"] = oa.config.ContactEmail
			}
			info["contact"] = contact
		}
	}

	// 4. Add x-gateway metadata.
	pathCount := 0
	if paths, ok := merged["paths"].(map[string]any); ok {
		pathCount = len(paths)
	}

	merged["x-gateway"] = map[string]any{
		"generatedAt":     time.Now().UTC().Format(time.RFC3339),
		"serviceCount":    len(specs),
		"pathCount":       pathCount,
		"refreshInterval": oa.config.RefreshInterval.String(),
	}

	// 5. Add gateway admin routes if configured.
	if oa.config.IncludeGatewayRoutes {
		paths, _ := merged["paths"].(map[string]any)
		if paths == nil {
			paths = make(map[string]any)
			merged["paths"] = paths
		}
		tags, _ := merged["tags"].([]any)
		tagSet := make(map[string]bool)
		for _, t := range tags {
			if tm, ok := t.(map[string]any); ok {
				if name, ok := tm["name"].(string); ok {
					tagSet[name] = true
				}
			}
		}
		oa.addGatewayRoutes(paths, &tags, tagSet)
		merged["tags"] = tags
	}

	return merged
}

// getOrBuildManifest returns the cached FARP manifest for a service, or builds
// a synthetic one for non-FARP services so the merger can apply routing strategies.
func (oa *OpenAPIAggregator) getOrBuildManifest(serviceName string, spec *ServiceOpenAPISpec) *farp.SchemaManifest {
	// Try cached manifest from discovery.
	if oa.disc != nil {
		if manifest := oa.disc.ServiceManifest(serviceName); manifest != nil {
			return manifest
		}
	}

	// Build a synthetic manifest for non-FARP services.
	strategy := farp.MountStrategyService
	if oa.config.MergeStrategy == "flat" {
		strategy = farp.MountStrategyRoot
	}

	// Check for prefix overrides via discovery.
	basePath := ""
	if oa.disc != nil {
		prefix := oa.disc.GetServicePrefix(serviceName)
		if prefix != "" {
			strategy = farp.MountStrategyCustom
			basePath = prefix
		}
	}

	manifest := &farp.SchemaManifest{
		Version:        "1.0.0",
		ServiceName:    serviceName,
		ServiceVersion: spec.Version,
		InstanceID:     serviceName + "-synthetic",
		Schemas: []farp.SchemaDescriptor{
			{
				Type:        farp.SchemaTypeOpenAPI,
				SpecVersion: "3.1.0",
				ContentType: "application/json",
			},
		},
		Routing: farp.RoutingConfig{
			Strategy: strategy,
			BasePath: basePath,
		},
	}

	return manifest
}

// mergerResultToMap converts a typed merger.MergeResult to map[string]any.
func (oa *OpenAPIAggregator) mergerResultToMap(result *merger.MergeResult) map[string]any {
	if result == nil || result.Spec == nil {
		return oa.buildEmptySpec()
	}

	spec := result.Spec

	// Convert paths.
	paths := make(map[string]any)
	for path, pathItem := range spec.Paths {
		paths[path] = pathItemToMap(pathItem)
	}

	// Convert tags.
	tags := make([]any, 0, len(spec.Tags))
	for _, tag := range spec.Tags {
		tagMap := map[string]any{"name": tag.Name}
		if tag.Description != "" {
			tagMap["description"] = tag.Description
		}
		tags = append(tags, tagMap)
	}

	merged := map[string]any{
		"openapi": spec.OpenAPI,
		"info": map[string]any{
			"title":       spec.Info.Title,
			"description": spec.Info.Description,
			"version":     spec.Info.Version,
		},
		"paths": paths,
		"tags":  tags,
	}

	// Convert servers.
	if len(spec.Servers) > 0 {
		servers := make([]any, 0, len(spec.Servers))
		for _, s := range spec.Servers {
			serverMap := map[string]any{"url": s.URL}
			if s.Description != "" {
				serverMap["description"] = s.Description
			}
			servers = append(servers, serverMap)
		}
		merged["servers"] = servers
	}

	// Convert components.
	if spec.Components != nil {
		components := make(map[string]any)

		if len(spec.Components.Schemas) > 0 {
			schemas := make(map[string]any)
			for name, schema := range spec.Components.Schemas {
				schemas[name] = schema
			}
			components["schemas"] = schemas
		}

		if len(spec.Components.Responses) > 0 {
			// Convert via JSON round-trip for simplicity.
			data, err := json.Marshal(spec.Components.Responses)
			if err == nil {
				var responses map[string]any
				if json.Unmarshal(data, &responses) == nil {
					components["responses"] = responses
				}
			}
		}

		if len(spec.Components.Parameters) > 0 {
			data, err := json.Marshal(spec.Components.Parameters)
			if err == nil {
				var params map[string]any
				if json.Unmarshal(data, &params) == nil {
					components["parameters"] = params
				}
			}
		}

		if len(spec.Components.RequestBodies) > 0 {
			data, err := json.Marshal(spec.Components.RequestBodies)
			if err == nil {
				var bodies map[string]any
				if json.Unmarshal(data, &bodies) == nil {
					components["requestBodies"] = bodies
				}
			}
		}

		if len(spec.Components.SecuritySchemes) > 0 {
			data, err := json.Marshal(spec.Components.SecuritySchemes)
			if err == nil {
				var schemes map[string]any
				if json.Unmarshal(data, &schemes) == nil {
					components["securitySchemes"] = schemes
				}
			}
		}

		if len(components) > 0 {
			merged["components"] = components
		}
	}

	return merged
}

// pathItemToMap converts a typed merger.PathItem to map[string]any.
func pathItemToMap(item merger.PathItem) map[string]any {
	result := make(map[string]any)

	addOp := func(method string, op *merger.Operation) {
		if op == nil {
			return
		}
		opMap := make(map[string]any)
		if op.OperationID != "" {
			opMap["operationId"] = op.OperationID
		}
		if op.Summary != "" {
			opMap["summary"] = op.Summary
		}
		if op.Description != "" {
			opMap["description"] = op.Description
		}
		if len(op.Tags) > 0 {
			// Convert to []any for compatibility with bastion's tag helpers.
			anyTags := make([]any, len(op.Tags))
			for i, t := range op.Tags {
				anyTags[i] = t
			}
			opMap["tags"] = anyTags
		}
		if len(op.Parameters) > 0 {
			opMap["parameters"] = op.Parameters
		}
		if op.RequestBody != nil {
			opMap["requestBody"] = op.RequestBody
		}
		if len(op.Responses) > 0 {
			opMap["responses"] = op.Responses
		}
		if len(op.Security) > 0 {
			opMap["security"] = op.Security
		}
		if op.Deprecated {
			opMap["deprecated"] = true
		}
		// Copy extensions.
		for k, v := range op.Extensions {
			opMap[k] = v
		}
		result[method] = opMap
	}

	addOp("get", item.Get)
	addOp("put", item.Put)
	addOp("post", item.Post)
	addOp("delete", item.Delete)
	addOp("patch", item.Patch)
	addOp("options", item.Options)
	addOp("head", item.Head)
	addOp("trace", item.Trace)

	// Copy path-level parameters.
	if len(item.Parameters) > 0 {
		result["parameters"] = item.Parameters
	}

	return result
}

// buildEmptySpec returns an empty but valid OpenAPI 3.1.0 spec.
func (oa *OpenAPIAggregator) buildEmptySpec() map[string]any {
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       oa.config.Title,
			"description": oa.config.Description,
			"version":     oa.config.Version,
		},
		"paths": map[string]any{},
		"tags":  []any{},
	}
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

// replaceOperationTags replaces all existing tags on operations with only the
// given tag. This is used when ServiceTagOnly is enabled to strip upstream tags.
func replaceOperationTags(pathItem any, tag string) any {
	pathItemMap, ok := pathItem.(map[string]any)
	if !ok {
		return pathItem
	}

	methods := []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
	for _, method := range methods {
		if op, ok := pathItemMap[method]; ok {
			if opMap, ok := op.(map[string]any); ok {
				opMap["tags"] = []any{tag}
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

// restoreOriginalTags replaces the FARP merger's prefixed tags with the original
// tags from the upstream spec. This is necessary because the merger always prefixes
// tags with the service name, but bastion handles tags differently.
func restoreOriginalTags(mergedPathItem any, originalPathItem any) {
	mergedMap, ok := mergedPathItem.(map[string]any)
	if !ok {
		return
	}

	originalMap, ok := originalPathItem.(map[string]any)
	if !ok {
		return
	}

	methods := []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
	for _, method := range methods {
		mergedOp, ok := mergedMap[method]
		if !ok {
			continue
		}
		mergedOpMap, ok := mergedOp.(map[string]any)
		if !ok {
			continue
		}

		// Get original tags from source spec.
		if origOp, ok := originalMap[method]; ok {
			if origOpMap, ok := origOp.(map[string]any); ok {
				if origTags, ok := origOpMap["tags"]; ok {
					mergedOpMap["tags"] = origTags
				} else {
					delete(mergedOpMap, "tags")
				}
			}
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
