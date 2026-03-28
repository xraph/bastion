package dashboard

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/a-h/templ"

	"github.com/xraph/bastion"
	"github.com/xraph/forge/extensions/dashboard/contributor"
)

// Contributor implements the LocalContributor interface for Bastion.
type Contributor struct {
	manifest *contributor.Manifest
	gw       *bastion.Gateway
}

// New creates a new Bastion dashboard contributor.
func New(manifest *contributor.Manifest, gw *bastion.Gateway) *Contributor {
	return &Contributor{
		manifest: manifest,
		gw:       gw,
	}
}

// Manifest returns the dashboard manifest.
func (c *Contributor) Manifest() *contributor.Manifest {
	return c.manifest
}

// RenderPage renders a dashboard page for the given route.
func (c *Contributor) RenderPage(_ context.Context, route string, _ contributor.Params) (templ.Component, error) {
	pageRoute := normalizeRoute(route)

	switch pageRoute {
	case "/", "":
		return c.renderOverview(), nil
	case "/routes":
		return c.renderRoutes(), nil
	case "/upstreams":
		return c.renderUpstreams(), nil
	case "/services":
		return c.renderServices(), nil
	case "/traffic":
		return c.renderTraffic(), nil
	case "/health":
		return c.renderHealth(), nil
	case "/circuits":
		return c.renderCircuits(), nil
	case "/api-explorer":
		return c.renderAPIExplorer(), nil
	case "/config":
		return c.renderConfig(), nil
	default:
		return emptyState("page-x", "Page Not Found", fmt.Sprintf("No page found for route %q", route)), nil
	}
}

// RenderWidget renders a widget by ID.
func (c *Contributor) RenderWidget(_ context.Context, widgetID string) (templ.Component, error) {
	switch widgetID {
	case "bastion-stats":
		return c.renderStatsWidget(), nil
	case "bastion-health":
		return c.renderHealthWidget(), nil
	case "bastion-errors":
		return c.renderErrorsWidget(), nil
	default:
		return emptyState("puzzle", "Unknown Widget", fmt.Sprintf("Widget %q not found", widgetID)), nil
	}
}

// RenderSettings renders a settings panel by ID.
func (c *Contributor) RenderSettings(_ context.Context, settingID string) (templ.Component, error) {
	switch settingID {
	case "bastion-config":
		return c.renderConfigSettings(), nil
	default:
		return emptyState("settings", "Unknown Setting", fmt.Sprintf("Setting %q not found", settingID)), nil
	}
}

// --- Page renderers ---

func (c *Contributor) renderOverview() templ.Component {
	overview := fetchOverview(c.gw)
	stats := fetchStats(c.gw)
	routes := fetchRoutes(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderOverviewPage(ctx, w, overview, stats, routes)
	})
}

func (c *Contributor) renderRoutes() templ.Component {
	routes := fetchRoutes(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderRoutesPage(ctx, w, routes)
	})
}

func (c *Contributor) renderUpstreams() templ.Component {
	routes := fetchRoutes(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderUpstreamsPage(ctx, w, routes)
	})
}

func (c *Contributor) renderServices() templ.Component {
	var services []*bastion.DiscoveredService
	if disc := c.gw.Discovery(); disc != nil {
		services = disc.DiscoveredServices()
	}

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderServicesPage(ctx, w, services)
	})
}

func (c *Contributor) renderTraffic() templ.Component {
	stats := fetchStats(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderTrafficPage(ctx, w, stats)
	})
}

func (c *Contributor) renderHealth() templ.Component {
	routes := fetchRoutes(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderHealthPage(ctx, w, routes)
	})
}

func (c *Contributor) renderCircuits() templ.Component {
	routes := fetchRoutes(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderCircuitsPage(ctx, w, routes)
	})
}

func (c *Contributor) renderAPIExplorer() templ.Component {
	openAPI := c.gw.OpenAPI()
	basePath := c.gw.Config().Dashboard.BasePath

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderAPIExplorerPage(ctx, w, openAPI, basePath)
	})
}

func (c *Contributor) renderConfig() templ.Component {
	config := c.gw.Config()

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderConfigPage(ctx, w, config)
	})
}

// --- Widget renderers ---

func (c *Contributor) renderStatsWidget() templ.Component {
	stats := fetchStats(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderStatsWidgetHTML(ctx, w, stats)
	})
}

func (c *Contributor) renderHealthWidget() templ.Component {
	stats := fetchStats(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderHealthWidgetHTML(ctx, w, stats)
	})
}

func (c *Contributor) renderErrorsWidget() templ.Component {
	stats := fetchStats(c.gw)

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderErrorsWidgetHTML(ctx, w, stats)
	})
}

// --- Settings renderers ---

func (c *Contributor) renderConfigSettings() templ.Component {
	config := c.gw.Config()

	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		return renderConfigSettingsHTML(ctx, w, config)
	})
}

// --- Helpers ---

func normalizeRoute(route string) string {
	route = strings.TrimRight(route, "/")
	if route == "" {
		return "/"
	}

	return route
}

func emptyState(icon, title, description string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, `<div class="flex flex-col items-center justify-center py-16 text-center">
			<div class="text-muted-foreground mb-4">
				<svg class="h-12 w-12 mx-auto" fill="none" stroke="currentColor" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>
			</div>
			<h3 class="text-lg font-semibold">%s</h3>
			<p class="text-muted-foreground mt-1">%s</p>
		</div>`, title, description)

		return err
	})
}
