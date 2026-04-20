package bastion

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/bastion/health"
)

// Gateway implements the Bastion API gateway.
type Gateway struct {
	*forge.BaseExtension

	config Config
	app    forge.App

	// Core components (interfaces — concrete implementations injected by extension/)
	routeManager RouteRegistry
	cbManager    CircuitControl
	proxyHandler http.Handler
	stats        StatsRecorder
	retryExec    RetryPolicy
	trafficSplit TrafficRouter

	// WebSocket hub for dashboard real-time updates (interface)
	hub WSBroadcaster

	// Concrete components (kept as-is — not duplicated in subpackages)
	healthMon   *health.Monitor
	rateLimiter *RateLimiter
	hooks       *HookEngine
	accessLog   *AccessLogger
	gwMetrics   *GatewayMetrics
	disc        *ServiceDiscovery

	// Security / caching / TLS / OpenAPI / AsyncAPI
	gwAuth     *GatewayAuth
	respCache  *ResponseCache
	tlsManager *TLSManager
	openAPI    *OpenAPIAggregator
	asyncAPI   *AsyncAPIAggregator

	// Admin handler registration (set by extension/ to avoid circular import)
	adminHandlerSetup func(gw *Gateway, router forge.Router)

	// Persistent store (optional — nil means pure in-memory)
	routeStore  RouteStore
	cbStore     CircuitBreakerStore
	healthStore HealthStore
	auditSink   AuditSink

	// Lifecycle
	draining         atomic.Bool
	routesRegistered bool
}

// NewExtension creates a new bastion gateway.
func New(opts ...ConfigOption) forge.Extension {
	config := DefaultConfig()
	for _, opt := range opts {
		opt(&config)
	}

	base := forge.NewBaseExtension(
		"bastion",
		"1.0.0",
		"Production-grade API gateway (Bastion) with FARP auto-discovery, multi-protocol proxying, and admin dashboard",
	)
	base.SetDependencies([]string{"discovery"})

	return &Gateway{
		BaseExtension: base,
		config:        config,
	}
}

// Register registers the bastion gateway.
func (e *Gateway) Register(app forge.App) error {
	if err := e.BaseExtension.Register(app); err != nil {
		return err
	}

	e.app = app

	// Load config from ConfigManager
	programmaticConfig := e.config
	finalConfig := DefaultConfig()

	if err := e.LoadConfig("bastion", &finalConfig, programmaticConfig, DefaultConfig(), false); err != nil {
		e.Logger().Warn("bastion: using programmatic config",
			forge.F("error", err.Error()),
		)
	}

	e.config = finalConfig

	if !e.config.Enabled {
		e.Logger().Info("bastion gateway disabled")

		return nil
	}

	// Initialize non-duplicate components (concrete types that stay in root)
	e.hooks = NewHookEngine()
	e.rateLimiter = NewRateLimiter(e.config.RateLimiting)
	e.healthMon = health.NewMonitor(e.config.HealthCheck, e.Logger())
	e.accessLog = NewAccessLogger(e.config.AccessLog, e.Logger())
	e.gwAuth = NewGatewayAuth(e.config.Auth, e.Logger())
	e.tlsManager = NewTLSManager(e.config.TLS, e.Logger())
	e.respCache = NewResponseCache(e.config.Caching, e.Logger(), nil)
	e.gwMetrics = NewGatewayMetrics(app.Metrics(), e.config.Metrics)

	// NOTE: Interface-typed fields (routeManager, cbManager, proxyHandler,
	// stats, retryExec, trafficSplit, hub) are injected by the extension/
	// package after Register() returns. Callbacks are wired in Start().

	// Register the gateway service with DI
	if err := forge.ProvideValue(app.Container(), e); err != nil {
		return fmt.Errorf("failed to register gateway service: %w", err)
	}

	// Health check is provided via the extension's Health() method

	e.Logger().Info("bastion gateway registered",
		forge.F("base_path", e.config.BasePath),
		forge.F("routes", len(e.config.Routes)),
		forge.F("discovery_enabled", e.config.Discovery.Enabled),
		forge.F("dashboard_enabled", e.config.Dashboard.Enabled),
	)

	return nil
}

// Start starts the bastion gateway. It is idempotent — calling Start()
// multiple times (e.g. when forge re-starts extensions after middleware
// application) is safe and only performs initialization once.
func (e *Gateway) Start(ctx context.Context) error {
	if !e.config.Enabled {
		return nil
	}

	// Guard against double-start. Forge may call Start() again after
	// applying middleware from later extensions (e.g. authsome).
	if e.IsStarted() {
		e.Logger().Debug("bastion gateway already started, skipping duplicate Start()")
		return nil
	}

	e.Logger().Info("starting bastion gateway")

	// Wire callbacks now that interfaces have been injected
	e.wireCallbacks()

	// Load manual routes from config
	e.loadManualRoutes()

	// Load persisted routes from store (if available)
	e.loadPersistedRoutes()

	// Wire store-backed persistence hooks
	e.wireStorePersistence()

	// Start service discovery if a DiscoveryService was injected via SetDiscoveryService.
	if e.config.Discovery.Enabled {
		if e.disc != nil {
			e.Logger().Info("starting service discovery manager")
			if startErr := e.disc.Start(ctx); startErr != nil {
				e.Logger().Warn("failed to start service discovery", forge.F("error", startErr))
			}
		} else {
			e.Logger().Warn("service discovery enabled but no DiscoveryService was injected — check that the discovery extension is loaded")
		}
	}

	// Start TLS certificate reload
	if e.config.TLS.Enabled {
		e.tlsManager.Start()
	}

	// Initialize and start OpenAPI aggregator
	if e.config.OpenAPI.Enabled {
		e.openAPI = NewOpenAPIAggregator(e.config.OpenAPI, e.Logger(), e.routeManager, e.disc)
		e.openAPI.Start(ctx)
	}

	// Initialize and start AsyncAPI aggregator
	if e.config.AsyncAPI.Enabled {
		e.asyncAPI = NewAsyncAPIAggregator(e.config.AsyncAPI, e.Logger(), e.disc)
		e.asyncAPI.Start(ctx)
	}

	// Start health monitor
	e.healthMon.Start(ctx)

	// Register routes (only once)
	if !e.routesRegistered {
		e.registerRoutes()
		e.routesRegistered = true
	}

	// Start WebSocket hub for dashboard
	if e.hub != nil {
		go e.hub.Run()
		go e.broadcastUpdates(ctx)
	}

	// Start rate limiter cleanup
	go e.rateLimiterCleanup(ctx)

	e.MarkStarted()
	routeCount := 0
	if e.routeManager != nil {
		routeCount = e.routeManager.RouteCount()
	}
	e.Logger().Info("bastion gateway started",
		forge.F("routes_count", routeCount),
	)

	return nil
}

// Stop stops the bastion gateway.
func (e *Gateway) Stop(_ context.Context) error {
	if !e.config.Enabled {
		return nil
	}

	e.Logger().Info("stopping bastion gateway")

	// Set draining
	e.draining.Store(true)

	// Stop discovery
	if e.disc != nil {
		e.disc.Stop()
	}

	// Stop health monitor
	if e.healthMon != nil {
		e.healthMon.Stop()
	}

	// Stop TLS manager
	if e.tlsManager != nil {
		e.tlsManager.Stop()
	}

	e.MarkStopped()
	e.Logger().Info("bastion gateway stopped")

	return nil
}

// Health checks if the gateway is healthy.
func (e *Gateway) Health(ctx context.Context) error {
	if !e.config.Enabled {
		return nil
	}

	if e.routeManager == nil || e.routeManager.RouteCount() == 0 {
		return fmt.Errorf("no routes configured")
	}

	return e.healthMon.Health(ctx)
}

// Dependencies returns extension dependencies.
func (e *Gateway) Dependencies() []string {
	return []string{"discovery"}
}

// RouteManager returns the route registry.
func (e *Gateway) RouteManager() RouteRegistry { return e.routeManager }

// HealthMonitor returns the health monitor.
func (e *Gateway) HealthMonitor() *health.Monitor { return e.healthMon }

// Stats returns the stats recorder.
func (e *Gateway) Stats() StatsRecorder { return e.stats }

// Hooks returns the hook engine.
func (e *Gateway) Hooks() *HookEngine { return e.hooks }

// Auth returns the gateway auth handler.
func (e *Gateway) Auth() *GatewayAuth { return e.gwAuth }

// Cache returns the response cache.
func (e *Gateway) Cache() *ResponseCache { return e.respCache }

// TLS returns the TLS manager.
func (e *Gateway) TLS() *TLSManager { return e.tlsManager }

// RateLimiter returns the rate limiter.
func (e *Gateway) RateLimiter() *RateLimiter { return e.rateLimiter }

// OpenAPI returns the OpenAPI aggregator.
func (e *Gateway) OpenAPI() *OpenAPIAggregator { return e.openAPI }

// AsyncAPI returns the AsyncAPI aggregator.
func (e *Gateway) AsyncAPI() *AsyncAPIAggregator { return e.asyncAPI }

// Discovery returns the service discovery integration.
func (e *Gateway) Discovery() *ServiceDiscovery { return e.disc }

// Config returns the gateway configuration.
func (e *Gateway) Config() Config { return e.config }

// Hub returns the WebSocket hub for real-time updates.
func (e *Gateway) Hub() WSBroadcaster { return e.hub }

// App returns the Forge app instance.
func (e *Gateway) App() forge.App { return e.app }

// Snapshot returns current gateway statistics.
func (e *Gateway) Snapshot() *GatewayStats {
	if e.stats == nil || e.routeManager == nil {
		return &GatewayStats{}
	}
	return e.stats.Snapshot(e.routeManager.ListRoutes())
}

// SetDiscoveryService sets the discovery service adapter and initializes
// the ServiceDiscovery integration. This is called by the extension adapter
// to inject the concrete discovery.Service dependency.
func (e *Gateway) SetDiscoveryService(svc DiscoveryService) {
	if svc == nil {
		return
	}
	e.disc = NewServiceDiscovery(
		e.config.Discovery,
		e.Logger(),
		e.routeManager,
		svc,
	)
}

// SetCacheStore sets an external cache store for response caching.
func (e *Gateway) SetCacheStore(store CacheStore) {
	if store == nil {
		return
	}
	e.respCache = NewResponseCache(e.config.Caching, e.Logger(), store)
}

// SetStore sets a composite store that provides persistence for all gateway
// subsystems (routes, circuit breakers, health checks, cache, rate limits, audit).
// The store is decomposed into subsystem-specific interfaces.
func (e *Gateway) SetStore(s interface{}) {
	if s == nil {
		return
	}
	if rs, ok := s.(RouteStore); ok {
		e.routeStore = rs
	}
	if cs, ok := s.(CircuitBreakerStore); ok {
		e.cbStore = cs
	}
	if hs, ok := s.(HealthStore); ok {
		e.healthStore = hs
	}
	if cs, ok := s.(CacheStore); ok {
		e.SetCacheStore(cs)
	}
	if rs, ok := s.(RateLimitStore); ok {
		e.rateLimiter = NewDistributedRateLimiter(e.config.RateLimiting, rs).RateLimiter
	}
	if as, ok := s.(AuditSink); ok {
		e.auditSink = as
	}
}

// --- Interface injection setters (called by extension/ after Register) ---

// SetRouteRegistry sets the route manager implementation.
func (e *Gateway) SetRouteRegistry(rm RouteRegistry) { e.routeManager = rm }

// SetCircuitControl sets the circuit breaker manager.
func (e *Gateway) SetCircuitControl(cb CircuitControl) { e.cbManager = cb }

// SetProxyHandler sets the HTTP handler for proxying.
func (e *Gateway) SetProxyHandler(h http.Handler) { e.proxyHandler = h }

// SetStatsRecorder sets the stats collector.
func (e *Gateway) SetStatsRecorder(s StatsRecorder) { e.stats = s }

// SetRetryPolicy sets the retry executor.
func (e *Gateway) SetRetryPolicy(r RetryPolicy) { e.retryExec = r }

// SetTrafficRouter sets the traffic splitter.
func (e *Gateway) SetTrafficRouter(t TrafficRouter) { e.trafficSplit = t }

// SetWSBroadcaster sets the WebSocket hub for dashboard.
func (e *Gateway) SetWSBroadcaster(h WSBroadcaster) { e.hub = h }

// SetAdminHandlerSetup sets the callback for registering admin dashboard routes.
func (e *Gateway) SetAdminHandlerSetup(fn func(gw *Gateway, router forge.Router)) {
	e.adminHandlerSetup = fn
}

// wireCallbacks wires observability callbacks now that interfaces are injected.
func (e *Gateway) wireCallbacks() {
	if e.cbManager != nil {
		e.cbManager.SetOnStateChange(func(targetID string, from, to CircuitState) {
			e.gwMetrics.SetCircuitBreakerState(targetID, to)
			e.hooks.RunOnCircuitBreak(targetID, from, to)
		})
	}

	e.healthMon.SetOnHealthChange(func(event health.Event) {
		e.gwMetrics.SetUpstreamHealth(event.TargetID, event.Healthy)
		e.hooks.RunOnUpstreamHealth(healthEventToUpstream(event))
	})

	if e.routeManager != nil {
		e.routeManager.OnRouteChange(func(event RouteEvent) {
			e.hooks.RunOnRouteChange(event)

			// Auto-register/deregister targets with health monitor so that
			// ALL routes (manual, persisted, discovery, FARP) get health
			// checking without each caller having to register explicitly.
			if event.Route != nil {
				switch event.Type {
				case RouteEventAdded, RouteEventUpdated:
					for _, target := range event.Route.Targets {
						e.healthMon.Register(event.Route.ID, target)
					}
				case RouteEventRemoved:
					for _, target := range event.Route.Targets {
						e.healthMon.Deregister(target.GetID())
					}
				}
			}
		})
	}
}

// loadManualRoutes adds manually configured routes.
func (e *Gateway) loadManualRoutes() {
	if e.routeManager == nil {
		return
	}

	for _, rc := range e.config.Routes {
		route := configToRoute(rc, e.config.BasePath)

		if err := e.routeManager.AddRoute(route); err != nil {
			e.Logger().Warn("failed to add manual route",
				forge.F("path", rc.Path),
				forge.F("error", err),
			)
		}

		// Register targets with health monitor
		for _, target := range route.Targets {
			e.healthMon.Register(route.ID, target)
		}
	}
}

// registerRoutes registers the gateway catch-all handler and admin routes.
func (e *Gateway) registerRoutes() {
	router := e.app.Router()

	mustRegister := func(err error) {
		if err != nil {
			e.Logger().Error("failed to register gateway route", forge.F("error", err))
		}
	}

	// Register admin API handlers via callback (set by extension/)
	if e.config.Dashboard.Enabled && e.adminHandlerSetup != nil {
		e.adminHandlerSetup(e, router)
	}

	// Register OpenAPI aggregation endpoints
	if e.openAPI != nil {
		specBase := e.config.Dashboard.BasePath
		fullSpecPath := specBase + e.config.OpenAPI.Path

		// === Aggregated upstream service spec ===
		mustRegister(router.GET(fullSpecPath, e.openAPI.HandleMergedSpec, forge.WithSchemaExclude()))

		// Per-service spec and refresh endpoints
		mustRegister(router.GET(specBase+"/api/openapi/services", e.openAPI.HandleServiceList, forge.WithSchemaExclude()))
		mustRegister(router.GET(specBase+"/api/openapi/services/:service", e.openAPI.HandleServiceSpec, forge.WithSchemaExclude()))
		mustRegister(router.POST(specBase+"/api/openapi/refresh", e.openAPI.HandleRefresh, forge.WithSchemaExclude()))

		// Root-level docs (/docs, /openapi.json) — aggregated upstream services only
		if e.config.OpenAPI.EnableRootDocs {
			rootSpecPath := e.config.OpenAPI.Path // "/openapi.json"
			rootUIPath := e.config.OpenAPI.RootUIPath
			if rootUIPath == "" {
				rootUIPath = "/docs"
			}

			mustRegister(router.GET(rootSpecPath, e.openAPI.HandleMergedSpec, forge.WithSchemaExclude()))
			mustRegister(router.GET(rootUIPath,
				e.openAPI.SwaggerUIHandler(rootSpecPath), forge.WithSchemaExclude()))
		}

		// === Gateway admin API docs (disabled by default) ===
		// Serves the gateway's own admin routes (route management, stats, discovery)
		// separately from the aggregated upstream service docs.
		if e.config.OpenAPI.EnableGatewayDocs {
			gwSpecPath := specBase + "/admin/openapi.json"
			mustRegister(router.GET(gwSpecPath, e.openAPI.HandleGatewaySpec, forge.WithSchemaExclude()))

			if e.config.OpenAPI.UIPath != "" {
				mustRegister(router.GET(specBase+e.config.OpenAPI.UIPath,
					e.openAPI.SwaggerUIHandler(gwSpecPath), forge.WithSchemaExclude()))
			}
		}
	}

	// Register AsyncAPI aggregation endpoints
	if e.asyncAPI != nil {
		specBase := e.config.Dashboard.BasePath
		fullAsyncSpecPath := specBase + e.config.AsyncAPI.Path

		mustRegister(router.GET(fullAsyncSpecPath, e.asyncAPI.HandleMergedSpec, forge.WithSchemaExclude()))

		// Root-level AsyncAPI endpoint
		if e.config.OpenAPI.EnableRootDocs {
			rootAsyncSpecPath := e.config.AsyncAPI.Path // "/asyncapi.json"
			mustRegister(router.GET(rootAsyncSpecPath, e.asyncAPI.HandleMergedSpec, forge.WithSchemaExclude()))
		}
	}

	// Register the catch-all proxy handler
	// This must be registered last to not interfere with admin routes
	if e.proxyHandler == nil {
		e.Logger().Warn("proxy handler not set, skipping catch-all route registration")
		return
	}

	basePath := strings.TrimRight(e.config.BasePath, "/")

	// Use a custom handler that wraps the proxy handler with middleware
	proxyHandler := GatewayMiddleware(e.config, e.proxyHandler)

	// Register catch-all for all methods
	catchAll := func(ctx forge.Context) error {
		if e.draining.Load() {
			return ctx.JSON(http.StatusServiceUnavailable, map[string]string{"error": "gateway is draining"})
		}

		proxyHandler.ServeHTTP(ctx.Response(), ctx.Request())

		return nil
	}

	// Register for common HTTP methods
	for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		var regErr error

		switch method {
		case "GET":
			regErr = router.GET(basePath+"/*path", catchAll)
		case "POST":
			regErr = router.POST(basePath+"/*path", catchAll)
		case "PUT":
			regErr = router.PUT(basePath+"/*path", catchAll)
		case "DELETE":
			regErr = router.DELETE(basePath+"/*path", catchAll)
		case "PATCH":
			regErr = router.PATCH(basePath+"/*path", catchAll)
		case "HEAD":
			regErr = router.HEAD(basePath+"/*path", catchAll)
		case "OPTIONS":
			regErr = router.OPTIONS(basePath+"/*path", catchAll)
		}

		if regErr != nil {
			e.Logger().Warn("failed to register catch-all route",
				forge.F("method", method),
				forge.F("error", regErr),
			)
		}
	}

	e.Logger().Info("gateway routes registered",
		forge.F("base_path", basePath),
		forge.F("dashboard", e.config.Dashboard.Enabled),
	)
}

func (e *Gateway) broadcastUpdates(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e.hub == nil || e.hub.ClientCount() == 0 {
				continue
			}

			// Broadcast stats
			stats := e.Snapshot()
			msg := map[string]any{"type": "stats", "data": stats}

			if err := e.hub.Broadcast(msg); err != nil {
				e.Logger().Error("failed to broadcast stats", forge.F("error", err))
			}

			// Broadcast routes
			if e.routeManager != nil {
				routes := e.routeManager.ListRoutes()
				routesMsg := map[string]any{"type": "routes", "data": routes}

				if err := e.hub.Broadcast(routesMsg); err != nil {
					e.Logger().Error("failed to broadcast routes", forge.F("error", err))
				}
			}
		}
	}
}

func (e *Gateway) rateLimiterCleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.rateLimiter.Cleanup(10 * time.Minute)
		}
	}
}

// configToRoute converts a RouteConfig to a Route.
func configToRoute(rc RouteConfig, basePath string) *Route {
	path := strings.TrimRight(basePath, "/") + rc.Path

	targets := make([]*Target, 0, len(rc.Targets))
	for _, tc := range rc.Targets {
		t := &Target{
			ID:       fmt.Sprintf("target-%s-%s", rc.Path, tc.URL),
			URL:      tc.URL,
			Weight:   tc.Weight,
			Healthy:  true,
			Tags:     tc.Tags,
			Metadata: tc.Metadata,
			TLS:      tc.TLS,
		}

		if t.Weight <= 0 {
			t.Weight = 1
		}

		targets = append(targets, t)
	}

	protocol := rc.Protocol
	if protocol == "" {
		protocol = ProtocolHTTP
	}

	// If an OpenAPI spec URL is provided, store it in all target metadata
	// so the OpenAPI aggregator can discover it via route-level inspection.
	if rc.OpenAPISpec != "" {
		for _, t := range targets {
			if t.Metadata == nil {
				t.Metadata = make(map[string]string)
			}
			t.Metadata["openapi"] = rc.OpenAPISpec
		}
	}

	routeID := fmt.Sprintf("manual-%s", rc.Path)
	if rc.ServiceName != "" {
		routeID = fmt.Sprintf("manual-%s-%s", rc.ServiceName, rc.Path)
	}

	return &Route{
		ID:            routeID,
		Path:          path,
		Methods:       rc.Methods,
		Targets:       targets,
		StripPrefix:   rc.StripPrefix,
		AddPrefix:     rc.AddPrefix,
		RewritePath:   rc.RewritePath,
		Headers:       rc.Headers,
		Protocol:      protocol,
		Source:        SourceManual,
		ServiceName:   rc.ServiceName,
		Priority:      rc.Priority + 100, // Manual routes get higher base priority
		Enabled:       rc.Enabled,
		Retry:         rc.Retry,
		Timeout:       rc.Timeout,
		RateLimit:     rc.RateLimit,
		Auth:          rc.Auth,
		Cache:         rc.Cache,
		TrafficPolicy: rc.TrafficPolicy,
		Metadata:      rc.Metadata,
	}
}

// loadPersistedRoutes loads routes from the persistent store into the route manager.
func (e *Gateway) loadPersistedRoutes() {
	if e.routeStore == nil || e.routeManager == nil {
		return
	}

	routes, err := e.routeStore.ListRoutes(context.Background())
	if err != nil {
		e.Logger().Warn("bastion: failed to load persisted routes", forge.F("error", err))
		return
	}

	for _, route := range routes {
		if err := e.routeManager.AddRoute(route); err != nil {
			e.Logger().Warn("bastion: failed to add persisted route",
				forge.F("id", route.ID),
				forge.F("error", err),
			)
			continue
		}

		// Register targets with health monitor
		for _, target := range route.Targets {
			e.healthMon.Register(route.ID, target)
		}
	}

	if len(routes) > 0 {
		e.Logger().Info("bastion: loaded persisted routes", forge.F("count", len(routes)))
	}
}

// wireStorePersistence sets up hooks that persist state changes to the store.
func (e *Gateway) wireStorePersistence() {
	// Persist route changes
	if e.routeStore != nil && e.routeManager != nil {
		e.routeManager.OnRouteChange(func(event RouteEvent) {
			ctx := context.Background()
			switch event.Type {
			case RouteEventAdded, RouteEventUpdated:
				if err := e.routeStore.SaveRoute(ctx, event.Route); err != nil {
					e.Logger().Warn("bastion: failed to persist route",
						forge.F("id", event.Route.ID),
						forge.F("error", err),
					)
				}
			case RouteEventRemoved:
				if err := e.routeStore.DeleteRoute(ctx, event.Route.ID); err != nil {
					e.Logger().Warn("bastion: failed to delete persisted route",
						forge.F("id", event.Route.ID),
						forge.F("error", err),
					)
				}
			}
		})
	}

	// Persist circuit breaker state changes
	if e.cbStore != nil && e.cbManager != nil {
		e.cbManager.SetOnStateChange(func(targetID string, from, to CircuitState) {
			e.gwMetrics.SetCircuitBreakerState(targetID, to)
			e.hooks.RunOnCircuitBreak(targetID, from, to)

			snap := &CircuitBreakerSnapshot{
				TargetID:        targetID,
				State:           to,
				LastStateChange: time.Now(),
				UpdatedAt:       time.Now(),
			}
			if err := e.cbStore.SaveState(context.Background(), snap); err != nil {
				e.Logger().Warn("bastion: failed to persist circuit breaker state",
					forge.F("target", targetID),
					forge.F("error", err),
				)
			}
		})
	}

	// Persist health check results
	if e.healthStore != nil {
		e.healthMon.SetOnHealthChange(func(event health.Event) {
			e.gwMetrics.SetUpstreamHealth(event.TargetID, event.Healthy)
			e.hooks.RunOnUpstreamHealth(healthEventToUpstream(event))

			result := &HealthCheckResult{
				TargetID:  event.TargetID,
				TargetURL: event.TargetURL,
				Healthy:   event.Healthy,
				Timestamp: event.Timestamp,
			}
			if err := e.healthStore.RecordCheck(context.Background(), result); err != nil {
				e.Logger().Warn("bastion: failed to persist health check",
					forge.F("target", event.TargetID),
					forge.F("error", err),
				)
			}
		})
	}
}
