// Package gateway builds a Bastion gateway that proxies on its own.
//
// bastion.New returns the core gateway. The proxy engine, route manager,
// circuit breakers, retry policy, stats and admin API live in subpackages
// that import the core, so the core can't build them itself, and on its
// own it registers no catch-all route and forwards nothing ("proxy handler
// not set"). New here wires every one of them, and picks up Forge's
// discovery extension when it is registered:
//
//	app.RegisterExtension(gateway.New(
//		bastion.WithRoute(bastion.RouteConfig{
//			Path:    "/docs/*",
//			Targets: []bastion.TargetConfig{{URL: "http://docs:8080"}},
//		}),
//	))
//
// It imports no store package, so you don't pull in the Mongo, Postgres or
// SQLite drivers. Routes created through the admin API then live in memory
// only. If you want them persisted in a grove database, use the extension
// package, which is this plus the store wiring.
package gateway

import (
	"context"
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/discovery"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
	"github.com/xraph/bastion/api"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

// Extension is a Bastion gateway with its runtime subsystems wired. It
// embeds *bastion.Gateway, so everything the core gateway exposes is
// available on it.
type Extension struct {
	*bastion.Gateway

	admin *admin.Service
}

var _ forge.Extension = (*Extension)(nil)

// New creates a gateway that proxies once registered with a Forge app.
func New(opts ...bastion.ConfigOption) *Extension {
	return &Extension{Gateway: bastion.New(opts...).(*bastion.Gateway)}
}

// Register registers the core gateway, then wires the proxy engine and
// the subsystems around it.
func (e *Extension) Register(app forge.App) error {
	if err := e.Gateway.Register(app); err != nil {
		return err
	}

	if !e.Config().Enabled {
		return nil
	}

	svc, err := Wire(e.Gateway)
	if err != nil {
		return err
	}

	e.admin = svc

	return nil
}

// Start wires discovery from the container, then starts the gateway.
func (e *Extension) Start(ctx context.Context) error {
	if e.Config().Enabled && !e.IsStarted() {
		WireDiscovery(e.Gateway)
	}

	return e.Gateway.Start(ctx)
}

// Admin returns the admin service, or nil before Register or when the
// gateway is disabled.
func (e *Extension) Admin() *admin.Service { return e.admin }

// Wire builds the route manager, circuit breakers, retry executor,
// traffic splitter, stats collector, load balancer and proxy engine for
// gw, and sets up its admin API. Call it after gw.Register and before
// gw.Start. It returns the admin service the routes were built on.
//
// New and the extension package both call it; you only need it yourself
// if you embed *bastion.Gateway in your own extension.
func Wire(gw *bastion.Gateway) (*admin.Service, error) {
	cfg := gw.Config()

	rm := routing.NewManager()
	gw.SetRouteRegistry(rm)

	cbm := resilience.NewCBManager(cfg.CircuitBreaker)
	gw.SetCircuitControl(cbm)

	gw.SetRetryPolicy(resilience.NewRetryExecutor(cfg.Retry))
	gw.SetTrafficRouter(routing.NewTrafficSplitter(cfg.TrafficSplit.Enabled))

	stats := proxy.NewStatsCollector()
	gw.SetStatsRecorder(stats)

	svc, err := admin.New(admin.Deps{
		Routes:    rm,
		Health:    gw.HealthMonitor(),
		Breakers:  cbm,
		BasePath:  cfg.BasePath,
		Persisted: gw.RoutesPersisted,
	})
	if err != nil {
		return nil, fmt.Errorf("bastion: build admin service: %w", err)
	}

	lb := routing.NewLoadBalancer(cfg.LoadBalancing.Strategy)

	pe := proxy.NewEngine(cfg, gw.Logger(), rm, gw.HealthMonitor(), cbm, gw.RateLimiter(), stats, gw.Hooks(), lb)
	pe.SetAuth(gw.Auth())
	pe.SetCache(gw.Cache())
	pe.SetTLSManager(gw.TLS())
	gw.SetProxyHandler(pe)

	// Dashboard hub + admin handlers
	var hub *api.Hub
	if cfg.Dashboard.Enabled && cfg.Dashboard.Realtime {
		hub = api.NewHub(gw.Logger())
		gw.SetWSBroadcaster(hub)
	}

	gw.SetAdminHandlerSetup(func(gw *bastion.Gateway, router forge.Router) {
		registerAdminRoutes(gw, router, hub, svc)
	})

	return svc, nil
}

// registerAdminRoutes mounts the admin REST API, the FARP push endpoints
// and the dashboard WebSocket. The REST API and the WebSocket go behind
// the admin auth (see adminRouteOptions). The FARP endpoints don't: the
// services that push-register through them have no admin credentials.
func registerAdminRoutes(gw *bastion.Gateway, router forge.Router, hub *api.Hub, svc *admin.Service) {
	cfg := gw.Config()
	adminBase := cfg.Dashboard.BasePath + "/api"
	h := api.NewHandlers(gw, hub, svc)
	auth := adminRouteOptions(gw)

	mustReg := func(err error) {
		if err != nil {
			gw.Logger().Error("failed to register admin route", forge.F("error", err))
		}
	}

	mustReg(router.GET(adminBase+"/routes", h.HandleListRoutes, auth...))
	mustReg(router.GET(adminBase+"/routes/:id", h.HandleGetRoute, auth...))
	mustReg(router.POST(adminBase+"/routes", h.HandleCreateRoute, auth...))
	mustReg(router.PUT(adminBase+"/routes/:id", h.HandleUpdateRoute, auth...))
	mustReg(router.DELETE(adminBase+"/routes/:id", h.HandleDeleteRoute, auth...))
	mustReg(router.POST(adminBase+"/routes/:id/enable", h.HandleEnableRoute, auth...))
	mustReg(router.POST(adminBase+"/routes/:id/disable", h.HandleDisableRoute, auth...))
	mustReg(router.GET(adminBase+"/upstreams", h.HandleListUpstreams, auth...))
	mustReg(router.GET(adminBase+"/stats", h.HandleGetStats, auth...))
	mustReg(router.GET(adminBase+"/stats/routes", h.HandleGetRouteStats, auth...))
	mustReg(router.GET(adminBase+"/config", h.HandleGetConfig, auth...))
	mustReg(router.GET(adminBase+"/discovery/services", h.HandleListDiscoveredServices, auth...))
	mustReg(router.POST(adminBase+"/discovery/refresh", h.HandleRefreshDiscovery, auth...))
	mustReg(router.GET(adminBase+"/discovery/debug", h.HandleDebugDiscovery, auth...))
	mustReg(router.POST(adminBase+"/discovery/register", h.HandleRegisterService, auth...))
	mustReg(router.DELETE(adminBase+"/discovery/services/:name", h.HandleDeregisterService, auth...))

	// FARP v1 push protocol endpoints (spec section 17.4).
	// Services POST to these endpoints to push-register with the gateway.
	mustReg(router.POST("/_farp/v1/register", h.HandleFARPRegister, forge.WithSchemaExclude()))
	mustReg(router.PUT("/_farp/v1/heartbeat/:id", h.HandleFARPHeartbeat, forge.WithSchemaExclude()))
	mustReg(router.DELETE("/_farp/v1/deregister/:id", h.HandleFARPDeregister, forge.WithSchemaExclude()))

	if hub != nil {
		mustReg(router.GET(cfg.Dashboard.BasePath+"/ws", h.HandleWebSocket, auth...))
	}
}

// WireDiscovery hands gw the discovery service from the app's container,
// if the discovery extension registered one. Call it before gw.Start.
// Without it, a gateway with discovery enabled logs a warning at start
// and serves only its configured and admin-created routes.
func WireDiscovery(gw *bastion.Gateway) {
	app := gw.App()
	if app == nil {
		return
	}

	discSvc, err := forge.Inject[*discovery.Service](app.Container())

	switch {
	case err != nil:
		gw.Logger().Warn("bastion: discovery service not found in DI container",
			forge.F("error", err),
		)
	case discSvc == nil:
		gw.Logger().Warn("bastion: discovery service resolved as nil from DI container")
	default:
		gw.Logger().Info("bastion: discovery service wired from DI container")
		gw.SetDiscoveryService(NewDiscoveryAdapter(discSvc))
	}
}
