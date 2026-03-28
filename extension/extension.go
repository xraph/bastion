package extension

import (
	"context"
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard"
	"github.com/xraph/forge/extensions/dashboard/contributor"
	"github.com/xraph/forge/extensions/discovery"
	"github.com/xraph/grove"
	"github.com/xraph/vessel"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/api"
	bastionDash "github.com/xraph/bastion/dashboard"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
	"github.com/xraph/bastion/store"
	mongostore "github.com/xraph/bastion/store/mongo"
	pgstore "github.com/xraph/bastion/store/postgres"
	sqlitestore "github.com/xraph/bastion/store/sqlite"
)

// Ensure Extension implements forge.Extension and dashboard.DashboardAware.
var (
	_ forge.Extension          = (*Extension)(nil)
	_ dashboard.DashboardAware = (*Extension)(nil)
)

// Extension wraps the Bastion gateway as a Forge extension, providing
// DashboardAware integration, auto-wiring of the discovery service, and
// grove-based persistent store support.
type Extension struct {
	gw   *bastion.Gateway
	opts []bastion.ConfigOption

	// Grove store integration
	useGrove       bool
	groveDBName    string
	storeInst      store.Store
	disableMigrate bool
}

// New creates a Bastion Forge extension.
func New(opts ...bastion.ConfigOption) *Extension {
	return &Extension{opts: opts}
}

// WithGroveDatabase configures the extension to use a named grove database
// from the DI container for persistent storage.
func WithGroveDatabase(name string) func(*Extension) {
	return func(e *Extension) {
		e.useGrove = true
		e.groveDBName = name
	}
}

// WithStore provides a pre-built store instance directly.
func WithStore(s store.Store) func(*Extension) {
	return func(e *Extension) {
		e.storeInst = s
	}
}

// WithDisableMigrate disables auto-migration on startup.
func WithDisableMigrate() func(*Extension) {
	return func(e *Extension) {
		e.disableMigrate = true
	}
}

// Configure applies extension-level options.
func (e *Extension) Configure(opts ...func(*Extension)) *Extension {
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// --- forge.Extension interface delegation ---

func (e *Extension) Name() string           { return e.gateway().Name() }
func (e *Extension) Description() string    { return e.gateway().Description() }
func (e *Extension) Version() string        { return e.gateway().Version() }
func (e *Extension) Dependencies() []string { return e.gateway().Dependencies() }

func (e *Extension) Register(app forge.App) error {
	gw := e.gateway()
	if err := gw.Register(app); err != nil {
		return err
	}

	if !gw.Config().Enabled {
		return nil
	}

	cfg := gw.Config()

	// Wire concrete subsystems from subpackages
	rm := routing.NewManager()
	gw.SetRouteRegistry(rm)

	cbm := resilience.NewCBManager(cfg.CircuitBreaker)
	gw.SetCircuitControl(cbm)

	re := resilience.NewRetryExecutor(cfg.Retry)
	gw.SetRetryPolicy(re)

	ts := routing.NewTrafficSplitter(cfg.TrafficSplit.Enabled)
	gw.SetTrafficRouter(ts)

	stats := proxy.NewStatsCollector()
	gw.SetStatsRecorder(stats)

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
		adminBase := cfg.Dashboard.BasePath + "/api"
		h := api.NewHandlers(gw, hub)

		mustReg := func(err error) {
			if err != nil {
				gw.Logger().Error("failed to register admin route", forge.F("error", err))
			}
		}

		mustReg(router.GET(adminBase+"/routes", h.HandleListRoutes))
		mustReg(router.GET(adminBase+"/routes/:id", h.HandleGetRoute))
		mustReg(router.POST(adminBase+"/routes", h.HandleCreateRoute))
		mustReg(router.PUT(adminBase+"/routes/:id", h.HandleUpdateRoute))
		mustReg(router.DELETE(adminBase+"/routes/:id", h.HandleDeleteRoute))
		mustReg(router.POST(adminBase+"/routes/:id/enable", h.HandleEnableRoute))
		mustReg(router.POST(adminBase+"/routes/:id/disable", h.HandleDisableRoute))
		mustReg(router.GET(adminBase+"/upstreams", h.HandleListUpstreams))
		mustReg(router.GET(adminBase+"/stats", h.HandleGetStats))
		mustReg(router.GET(adminBase+"/stats/routes", h.HandleGetRouteStats))
		mustReg(router.GET(adminBase+"/config", h.HandleGetConfig))
		mustReg(router.GET(adminBase+"/discovery/services", h.HandleListDiscoveredServices))
		mustReg(router.POST(adminBase+"/discovery/refresh", h.HandleRefreshDiscovery))
		mustReg(router.GET(adminBase+"/discovery/debug", h.HandleDebugDiscovery))
		mustReg(router.POST(adminBase+"/discovery/register", h.HandleRegisterService))
		mustReg(router.DELETE(adminBase+"/discovery/services/:name", h.HandleDeregisterService))

		if hub != nil {
			mustReg(router.GET(cfg.Dashboard.BasePath+"/ws", h.HandleWebSocket))
		}
	})

	return nil
}

func (e *Extension) Start(ctx context.Context) error {
	app := e.gateway().App()

	// Auto-wire discovery service if available
	if app != nil {
		discSvc, err := forge.Inject[*discovery.Service](app.Container())
		if err != nil {
			e.gw.Logger().Warn("bastion: discovery service not found in DI container",
				forge.F("error", err),
			)
		} else if discSvc != nil {
			e.gw.Logger().Info("bastion: discovery service wired from DI container")
			e.gw.SetDiscoveryService(NewDiscoveryAdapter(discSvc))
		} else {
			e.gw.Logger().Warn("bastion: discovery service resolved as nil from DI container")
		}
	}

	// Wire grove store
	if app != nil {
		if err := e.wireStore(app); err != nil {
			return fmt.Errorf("bastion: %w", err)
		}
	}

	return e.gateway().Start(ctx)
}

func (e *Extension) Stop(ctx context.Context) error {
	return e.gateway().Stop(ctx)
}

func (e *Extension) Health(ctx context.Context) error {
	return e.gateway().Health(ctx)
}

// --- dashboard.DashboardAware ---

// DashboardContributor returns a LocalContributor that renders bastion
// pages, widgets, and settings in the Forge dashboard.
func (e *Extension) DashboardContributor() contributor.LocalContributor {
	manifest := bastionDash.NewManifest()
	return bastionDash.New(manifest, e.gateway())
}

// --- Accessor ---

// Gateway returns the underlying bastion.Gateway instance.
func (e *Extension) Gateway() *bastion.Gateway {
	return e.gateway()
}

func (e *Extension) gateway() *bastion.Gateway {
	if e.gw == nil {
		ext := bastion.New(e.opts...)
		e.gw = ext.(*bastion.Gateway)
	}

	return e.gw
}

// --- Grove integration ---

// wireStore resolves or builds the store and wires it into the gateway.
func (e *Extension) wireStore(fapp forge.App) error {
	var s store.Store

	if e.storeInst != nil {
		// Use pre-built store
		s = e.storeInst
	} else if e.useGrove {
		// Explicitly configured grove database
		db, err := e.resolveGroveDB(fapp)
		if err != nil {
			return err
		}
		built, err := buildStoreFromGroveDB(db)
		if err != nil {
			return err
		}
		s = built
	} else if db, err := forge.Inject[*grove.DB](fapp.Container()); err == nil {
		// Auto-discover default grove.DB from container
		built, err := buildStoreFromGroveDB(db)
		if err != nil {
			return err
		}
		s = built
		e.gateway().Logger().Info("bastion: auto-discovered grove.DB from container",
			forge.F("driver", db.Driver().Name()),
		)
	}

	if s == nil {
		return nil
	}

	// Run migrations unless disabled
	if !e.disableMigrate {
		if err := s.Migrate(context.Background()); err != nil {
			return fmt.Errorf("store migration failed: %w", err)
		}
	}

	// Wire store into gateway
	e.gw.SetStore(s)

	return nil
}

// resolveGroveDB resolves a grove.DB from the DI container.
func (e *Extension) resolveGroveDB(fapp forge.App) (*grove.DB, error) {
	if e.groveDBName != "" {
		db, err := vessel.InjectNamed[*grove.DB](fapp.Container(), e.groveDBName)
		if err != nil {
			return nil, fmt.Errorf("grove database %q not found in container: %w", e.groveDBName, err)
		}
		return db, nil
	}

	db, err := forge.Inject[*grove.DB](fapp.Container())
	if err != nil {
		return nil, fmt.Errorf("default grove database not found in container: %w", err)
	}

	return db, nil
}

// buildStoreFromGroveDB creates the appropriate store for the grove driver.
func buildStoreFromGroveDB(db *grove.DB) (store.Store, error) {
	driverName := db.Driver().Name()
	switch driverName {
	case "pg":
		return pgstore.New(db), nil
	case "sqlite":
		return sqlitestore.New(db), nil
	case "mongo":
		return mongostore.New(db), nil
	default:
		return nil, fmt.Errorf("bastion: unsupported grove driver %q", driverName)
	}
}
