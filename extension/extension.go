package extension

import (
	"context"
	"fmt"

	"github.com/xraph/forge"
	dashcontract "github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"
	"github.com/xraph/grove"
	"github.com/xraph/vessel"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
	bastioncontract "github.com/xraph/bastion/extension/contract"
	"github.com/xraph/bastion/gateway"
	"github.com/xraph/bastion/store"
	mongostore "github.com/xraph/bastion/store/mongo"
	pgstore "github.com/xraph/bastion/store/postgres"
	sqlitestore "github.com/xraph/bastion/store/sqlite"
)

// Ensure Extension implements forge.Extension.
var _ forge.Extension = (*Extension)(nil)

// Extension wraps the Bastion gateway as a Forge extension, registering the
// bastion contract contributor that the React dashboard shell reads,
// auto-wiring the discovery service, and providing grove-based persistent
// store support.
//
// The proxy and admin wiring is shared with the gateway package. Use that
// package directly when you don't need a grove store: it leaves the Mongo,
// Postgres and SQLite store packages out of your build.
type Extension struct {
	gw    *bastion.Gateway
	admin *admin.Service
	opts  []bastion.ConfigOption

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

	svc, err := gateway.Wire(gw)
	if err != nil {
		return err
	}
	e.admin = svc

	return nil
}

func (e *Extension) Start(ctx context.Context) error {
	app := e.gateway().App()

	// Auto-wire discovery service if available.
	if app != nil {
		gateway.WireDiscovery(e.gw)
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

// RegisterContractContributor implements dashboard.ContractContributorAware.
// It registers the bastion contract contributor, which is what the React
// shell reads.
func (e *Extension) RegisterContractContributor(
	disp *dispatcher.Dispatcher,
	reg dashcontract.Registry,
	wreg dashcontract.WardenRegistry,
) error {
	if e.gw == nil || e.admin == nil {
		// Register never ran, or the gateway is disabled: nothing to answer.
		return nil
	}

	deps := bastioncontract.Deps{Gateway: e.gw, Admin: e.admin}
	// Logger is nil until Register runs; a typed-nil guard keeps Deps.Logger
	// a true nil interface so mapError skips logging.
	if l := e.gw.Logger(); l != nil {
		deps.Logger = l
	}

	if err := bastioncontract.Register(disp, reg, wreg, deps); err != nil {
		return fmt.Errorf("bastion: register contract contributor: %w", err)
	}

	return nil
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
