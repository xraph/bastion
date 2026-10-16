package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xraph/farp"
	"github.com/xraph/farp/merger"
	"github.com/xraph/forge"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/bastion/internal/telemetry"
	"github.com/xraph/bastion/observability"
)

// AsyncAPIAggregator fetches, caches, and merges AsyncAPI specs from all
// discovered upstream services that expose AsyncAPI schemas via FARP manifests.
// It exposes a unified merged AsyncAPI spec combining all service channels.
type AsyncAPIAggregator struct {
	config        AsyncAPIConfig
	logger        forge.Logger
	disc          *Manager
	httpClient    *http.Client
	schemaFetcher *SchemaFetcher
	metrics       *observability.Metrics

	mu             sync.RWMutex
	mergedSpec     map[string]any // cached merged spec
	mergedSpecJSON []byte         // pre-serialized JSON
	lastRefresh    time.Time
	refreshing     bool

	refreshCh chan struct{} // signal channel for debounced reactive refresh
}

// NewAsyncAPIAggregator creates a new AsyncAPI aggregator.
func NewAsyncAPIAggregator(config AsyncAPIConfig, logger forge.Logger, disc *Manager) *AsyncAPIAggregator {
	httpClient := &http.Client{
		Timeout:   config.FetchTimeout,
		Transport: telemetry.NewTransport(nil, "fetch asyncapi spec"),
	}

	return &AsyncAPIAggregator{
		config:        config,
		logger:        logger,
		disc:          disc,
		httpClient:    httpClient,
		schemaFetcher: NewSchemaFetcher(httpClient, logger),
		refreshCh:     make(chan struct{}, 1),
	}
}

// SetMetrics sets where the aggregator records the duration of each
// refresh. Nil records none. Call it before Start.
func (aa *AsyncAPIAggregator) SetMetrics(m *observability.Metrics) { aa.metrics = m }

// Start begins the periodic spec refresh loop and subscribes to service
// change events for reactive refresh. The refreshes it runs are each the
// root of their own trace.
func (aa *AsyncAPIAggregator) Start(ctx context.Context) {
	if !aa.config.Enabled {
		return
	}

	// The loops outlive Start, so they drop the boot span ctx carries.
	ctx = telemetry.Detach(ctx)

	// Subscribe to service changes for reactive refresh.
	if aa.disc != nil {
		aa.disc.OnServiceChange(func(serviceName string, registered bool) {
			aa.logger.Debug("AsyncAPI: service change detected, scheduling refresh",
				forge.F("service", serviceName),
				forge.F("registered", registered),
			)
			select {
			case aa.refreshCh <- struct{}{}:
			default:
			}
		})
	}

	// Start debounce goroutine.
	go aa.debounceLoop(ctx)

	// Delayed initial refresh.
	go func() {
		select {
		case <-time.After(3 * time.Second):
			aa.refresh(ctx, true)
		case <-ctx.Done():
		}
	}()

	// Periodic refresh.
	go func() {
		ticker := time.NewTicker(aa.config.RefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				aa.refresh(ctx, true)
			}
		}
	}()

	aa.logger.Info("AsyncAPI aggregator started",
		forge.F("path", aa.config.Path),
		forge.F("refresh_interval", aa.config.RefreshInterval),
	)
}

// debounceLoop coalesces rapid service-change signals into a single Refresh.
func (aa *AsyncAPIAggregator) debounceLoop(ctx context.Context) {
	const debounceDelay = 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return
		case <-aa.refreshCh:
			timer := time.NewTimer(debounceDelay)
		drain:
			for {
				select {
				case <-aa.refreshCh:
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

			aa.refresh(ctx, true)
		}
	}
}

// Refresh fetches all upstream AsyncAPI specs and rebuilds the merged spec.
// Its span is a child of whatever span ctx carries.
func (aa *AsyncAPIAggregator) Refresh(ctx context.Context) {
	aa.refresh(ctx, false)
}

// RefreshDetached is Refresh for a caller that doesn't wait for it. The
// refresh starts a trace of its own, linked to ctx's span.
func (aa *AsyncAPIAggregator) RefreshDetached(ctx context.Context) {
	aa.refresh(telemetry.Detach(ctx), true, trace.LinkFromContext(ctx))
}

// refresh is Refresh. root starts its span as the root of a new trace, for
// the background loops, linked to links. The span only starts once there
// are specs to fetch.
func (aa *AsyncAPIAggregator) refresh(ctx context.Context, root bool, links ...trace.Link) {
	start := time.Now()

	aa.mu.Lock()
	if aa.refreshing {
		aa.mu.Unlock()
		return
	}
	aa.refreshing = true
	aa.mu.Unlock()

	defer func() {
		aa.mu.Lock()
		aa.refreshing = false
		aa.mu.Unlock()
	}()

	if aa.disc == nil {
		return
	}

	// Collect AsyncAPI schemas from FARP manifests.
	manifests := aa.disc.ServiceManifests()

	type asyncSource struct {
		serviceName string
		manifest    *farp.SchemaManifest
		descriptor  *farp.SchemaDescriptor
		specURL     string // fallback
	}

	var sources []asyncSource

	for name, manifest := range manifests {
		if aa.isExcluded(name) {
			continue
		}

		// Find AsyncAPI schema descriptor.
		for i := range manifest.Schemas {
			desc := &manifest.Schemas[i]
			if desc.Type == farp.SchemaTypeAsyncAPI {
				sources = append(sources, asyncSource{
					serviceName: name,
					manifest:    manifest,
					descriptor:  desc,
				})
				break
			}
		}

		// Fallback: use AsyncAPI endpoint from manifest if no schema descriptor.
		if manifest.Endpoints.AsyncAPI != "" {
			hasSource := false
			for _, s := range sources {
				if s.serviceName == name {
					hasSource = true
					break
				}
			}
			if !hasSource {
				svcs := aa.disc.DiscoveredServices()
				for _, svc := range svcs {
					if svc.Name == name {
						specURL := fmt.Sprintf("http://%s:%d%s", svc.Address, svc.Port, manifest.Endpoints.AsyncAPI)
						sources = append(sources, asyncSource{
							serviceName: name,
							manifest:    manifest,
							specURL:     specURL,
						})
						break
					}
				}
			}
		}
	}

	// Also check for services with farp.asyncapi metadata but no manifest.
	if aa.disc != nil {
		for _, svc := range aa.disc.DiscoveredServices() {
			if aa.isExcluded(svc.Name) {
				continue
			}

			// Skip if already covered by manifest.
			alreadyCovered := false
			for _, s := range sources {
				if s.serviceName == svc.Name {
					alreadyCovered = true
					break
				}
			}
			if alreadyCovered {
				continue
			}

			if specURL, ok := svc.Metadata["farp.asyncapi"]; ok && specURL != "" {
				sources = append(sources, asyncSource{
					serviceName: svc.Name,
					specURL:     specURL,
				})
			}
		}
	}

	if len(sources) == 0 {
		aa.metrics.ObserveJob(observability.JobAsyncAPI, false, time.Since(start))

		return
	}

	startSpan := telemetry.Start
	if root {
		startSpan = telemetry.StartRoot
	}

	ctx, span := startSpan(ctx, "refresh asyncapi specs", trace.WithTimestamp(start), trace.WithLinks(validLinks(links)...))
	defer span.End()

	refreshCtx, refreshCancel := context.WithTimeout(ctx, 30*time.Second)
	defer refreshCancel()

	var (
		failed    atomic.Int64
		mergeFail bool
	)

	defer func() {
		span.SetAttributes(
			attribute.Int("bastion.asyncapi.sources", len(sources)),
			attribute.Int64("bastion.asyncapi.failed", failed.Load()),
		)
		aa.metrics.ObserveJob(observability.JobAsyncAPI, mergeFail || failed.Load() > 0, time.Since(start))
	}()

	// Fetch schemas in parallel.
	const maxConcurrent = 10
	var wg sync.WaitGroup
	type fetchResult struct {
		serviceName string
		manifest    *farp.SchemaManifest
		schema      map[string]any
	}
	resultCh := make(chan fetchResult, len(sources))
	sem := make(chan struct{}, maxConcurrent)

	for _, src := range sources {
		wg.Add(1)
		sem <- struct{}{}

		go func(s asyncSource) {
			defer wg.Done()
			defer func() { <-sem }()

			var fetched *FetchedSchema
			if s.descriptor != nil {
				fetched = aa.schemaFetcher.FetchSchema(refreshCtx, *s.descriptor, s.serviceName)
			} else if s.specURL != "" {
				fetched = aa.schemaFetcher.FetchFromURL(refreshCtx, s.specURL, s.serviceName)
			}

			if fetched != nil && !fetched.Healthy {
				// Retry once.
				time.Sleep(500 * time.Millisecond)
				var retry *FetchedSchema
				if s.descriptor != nil {
					retry = aa.schemaFetcher.FetchSchema(refreshCtx, *s.descriptor, s.serviceName)
				} else if s.specURL != "" {
					retry = aa.schemaFetcher.FetchFromURL(refreshCtx, s.specURL, s.serviceName)
				}
				if retry != nil && retry.Healthy {
					fetched = retry
				}
			}

			if fetched == nil || !fetched.Healthy || fetched.Schema == nil {
				failed.Add(1)

				return
			}

			manifest := s.manifest
			if manifest == nil {
				// Synthetic manifest for non-FARP services.
				manifest = &farp.SchemaManifest{
					Version:     "1.0.0",
					ServiceName: s.serviceName,
					InstanceID:  s.serviceName + "-synthetic",
					Schemas: []farp.SchemaDescriptor{
						{Type: farp.SchemaTypeAsyncAPI},
					},
				}
			}

			resultCh <- fetchResult{
				serviceName: s.serviceName,
				manifest:    manifest,
				schema:      fetched.Schema,
			}
		}(src)
	}

	wg.Wait()
	close(resultCh)

	// Build merger inputs.
	var asyncSchemas []merger.AsyncAPIServiceSchema
	for r := range resultCh {
		asyncSchemas = append(asyncSchemas, merger.AsyncAPIServiceSchema{
			Manifest: r.manifest,
			Schema:   r.schema,
		})
	}

	if len(asyncSchemas) == 0 {
		return
	}

	// Merge using FARP AsyncAPI merger.
	mergerConfig := merger.MergerConfig{
		MergedTitle:        aa.config.Title,
		MergedDescription:  aa.config.Description,
		MergedVersion:      aa.config.Version,
		IncludeServiceTags: true,
		SortOutput:         true,
	}

	_, mergeSpan := telemetry.Start(ctx, "merge asyncapi specs")

	m := merger.NewAsyncAPIMerger(mergerConfig)
	result, err := m.MergeAsyncAPI(asyncSchemas)
	if err != nil {
		mergeFail = true
		telemetry.Fail(mergeSpan, "merge failed", err)
		mergeSpan.End()
		telemetry.Fail(span, "merge failed", nil)
		aa.logger.Error("AsyncAPI merge failed", forge.F("error", err))

		return
	}

	// Convert to map[string]any.
	merged := aa.mergeResultToMap(result)

	// Serialize to JSON.
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		mergeFail = true
		telemetry.Fail(mergeSpan, "serialize merged spec failed", err)
		mergeSpan.End()
		telemetry.Fail(span, "merge failed", nil)
		aa.logger.Error("failed to serialize merged AsyncAPI spec", forge.F("error", err))

		return
	}

	mergeSpan.End()

	// Atomically update.
	aa.mu.Lock()
	aa.mergedSpec = merged
	aa.mergedSpecJSON = mergedJSON
	aa.lastRefresh = time.Now()
	aa.mu.Unlock()

	channelCount := 0
	if channels, ok := merged["channels"].(map[string]any); ok {
		channelCount = len(channels)
	}

	span.SetAttributes(attribute.Int("bastion.asyncapi.channels", channelCount))

	aa.logger.Debug("AsyncAPI specs refreshed",
		forge.F("services", len(asyncSchemas)),
		forge.F("total_channels", channelCount),
	)
}

// MergedSpec returns the pre-serialized merged AsyncAPI spec JSON.
func (aa *AsyncAPIAggregator) MergedSpec() []byte {
	aa.mu.RLock()
	defer aa.mu.RUnlock()
	return aa.mergedSpecJSON
}

// MergedSpecMap returns the merged spec as a map.
func (aa *AsyncAPIAggregator) MergedSpecMap() map[string]any {
	aa.mu.RLock()
	defer aa.mu.RUnlock()
	return aa.mergedSpec
}

// LastRefresh returns the time of the last spec refresh.
func (aa *AsyncAPIAggregator) LastRefresh() time.Time {
	aa.mu.RLock()
	defer aa.mu.RUnlock()
	return aa.lastRefresh
}

// SpecPath returns the configured endpoint path for the aggregated AsyncAPI spec.
func (aa *AsyncAPIAggregator) SpecPath() string {
	return aa.config.Path
}

// HandleMergedSpec serves the aggregated AsyncAPI spec as JSON.
func (aa *AsyncAPIAggregator) HandleMergedSpec(ctx forge.Context) error {
	specJSON := aa.MergedSpec()
	if specJSON == nil {
		return ctx.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "AsyncAPI spec not yet available, try again shortly",
		})
	}

	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().Header().Set("Cache-Control", "public, max-age=30")
	ctx.Response().WriteHeader(http.StatusOK)
	_, err := ctx.Response().Write(specJSON)

	return err
}

// isExcluded checks if a service name is in the exclusion list.
func (aa *AsyncAPIAggregator) isExcluded(name string) bool {
	for _, excluded := range aa.config.ExcludeServices {
		if strings.EqualFold(excluded, name) {
			return true
		}
	}
	return false
}

// mergeResultToMap converts a merger.AsyncAPIMergeResult to map[string]any.
func (aa *AsyncAPIAggregator) mergeResultToMap(result *merger.AsyncAPIMergeResult) map[string]any {
	if result == nil || result.Spec == nil {
		return map[string]any{
			"asyncapi": "2.6.0",
			"info": map[string]any{
				"title":       aa.config.Title,
				"description": aa.config.Description,
				"version":     aa.config.Version,
			},
			"channels": map[string]any{},
		}
	}

	// Convert via JSON round-trip for simplicity and correctness.
	data, err := json.Marshal(result.Spec)
	if err != nil {
		aa.logger.Error("failed to marshal AsyncAPI merge result", forge.F("error", err))
		return map[string]any{}
	}

	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		aa.logger.Error("failed to unmarshal AsyncAPI merge result", forge.F("error", err))
		return map[string]any{}
	}

	return merged
}
