// Package contract wires Bastion into the Forge dashboard's contract path. It
// registers the `bastion` contributor and answers its intents from the live
// gateway and the admin service.
package contract

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"

	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"
	"github.com/xraph/forge/extensions/dashboard/contract/loader"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
)

//go:embed manifest.yaml
var manifestYAML []byte

// ContributorName is the join key with packages/plugin-bastion's `extension`
// field. A mismatch hides the React plugin with no error anywhere, because
// that is what an uninstalled extension looks like.
const ContributorName = "bastion"

// Deps bundles what the handlers need.
type Deps struct {
	// Gateway answers stats, circuits, discovery, OpenAPI and config. Required.
	Gateway *bastion.Gateway
	// Admin answers route reads and performs route writes. Required.
	Admin *admin.Service
	// Logger receives an Error entry for every error mapped to CodeInternal.
	// Optional.
	Logger forge.Logger
}

// Register loads the embedded manifest, validates and registers it, and binds
// the handlers.
func Register(d *dispatcher.Dispatcher, reg contract.Registry, wreg contract.WardenRegistry, deps Deps) error {
	if deps.Gateway == nil || deps.Admin == nil {
		return errors.New("bastion/contract: Gateway and Admin are required")
	}

	m, err := loader.Load(bytes.NewReader(manifestYAML), "bastion/contract/manifest.yaml")
	if err != nil {
		return fmt.Errorf("bastion/contract: load manifest: %w", err)
	}

	if err := loader.Validate(m, wreg); err != nil {
		return fmt.Errorf("bastion/contract: validate manifest: %w", err)
	}

	if err := reg.Register(m); err != nil {
		return fmt.Errorf("bastion/contract: register manifest: %w", err)
	}

	c := ContributorName

	for _, bind := range []struct {
		intent string
		fn     func() error
	}{
		{"overview.stats", func() error { return dispatcher.RegisterQuery(d, c, "overview.stats", 1, overviewStatsHandler(deps)) }},
		{"traffic.stats", func() error { return dispatcher.RegisterQuery(d, c, "traffic.stats", 1, trafficStatsHandler(deps)) }},
		{"circuits.list", func() error { return dispatcher.RegisterQuery(d, c, "circuits.list", 1, circuitsListHandler(deps)) }},
		{"routes.list", func() error { return dispatcher.RegisterQuery(d, c, "routes.list", 1, routesListHandler(deps)) }},
		{"routes.detail", func() error { return dispatcher.RegisterQuery(d, c, "routes.detail", 1, routesDetailHandler(deps)) }},
		{"upstreams.list", func() error { return dispatcher.RegisterQuery(d, c, "upstreams.list", 1, upstreamsListHandler(deps)) }},
		{"services.list", func() error { return dispatcher.RegisterQuery(d, c, "services.list", 1, servicesListHandler(deps)) }},
		{"openapi.summary", func() error { return dispatcher.RegisterQuery(d, c, "openapi.summary", 1, openapiSummaryHandler(deps)) }},
		{"config.detail", func() error { return dispatcher.RegisterQuery(d, c, "config.detail", 1, configDetailHandler(deps)) }},
		{"routes.create", func() error { return dispatcher.RegisterCommand(d, c, "routes.create", 1, routesCreateHandler(deps)) }},
		{"routes.update", func() error { return dispatcher.RegisterCommand(d, c, "routes.update", 1, routesUpdateHandler(deps)) }},
		{"routes.delete", func() error { return dispatcher.RegisterCommand(d, c, "routes.delete", 1, routesDeleteHandler(deps)) }},
		{"routes.setEnabled", func() error {
			return dispatcher.RegisterCommand(d, c, "routes.setEnabled", 1, routesSetEnabledHandler(deps))
		}},
		{"discovery.refresh", func() error {
			return dispatcher.RegisterCommand(d, c, "discovery.refresh", 1, discoveryRefreshHandler(deps))
		}},
		{"openapi.refresh", func() error {
			return dispatcher.RegisterCommand(d, c, "openapi.refresh", 1, openapiRefreshHandler(deps))
		}},
		{"circuits.reset", func() error {
			return dispatcher.RegisterCommand(d, c, "circuits.reset", 1, circuitsResetHandler(deps))
		}},
	} {
		if err := bind.fn(); err != nil {
			return fmt.Errorf("bastion/contract: register %s: %w", bind.intent, err)
		}
	}

	return nil
}
