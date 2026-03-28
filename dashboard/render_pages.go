package dashboard

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/dashboard/shared"
)

// renderOverviewPage renders the gateway overview page.
func renderOverviewPage(_ context.Context, w io.Writer, overview shared.GatewayOverview, stats *bastion.GatewayStats, routes []*bastion.Route) error {
	_, err := fmt.Fprintf(w, `<div class="space-y-8">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">Overview</h1>
		<p class="text-muted-foreground mt-1">Bastion API Gateway dashboard</p>
	</div>
	<div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
		%s %s %s %s
	</div>
	<div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
		<div class="rounded-lg border bg-card p-6">
			<h3 class="text-lg font-semibold mb-4">Route Summary</h3>
			<div class="space-y-3 text-sm">
				<div class="flex justify-between"><span class="text-muted-foreground">Total Routes</span><span class="font-medium">%d</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Healthy Upstreams</span><span class="font-medium">%d / %d</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Cache Hit Rate</span><span class="font-medium">%.1f%%</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Uptime</span><span class="font-medium">%ds</span></div>
			</div>
		</div>
		<div class="rounded-lg border bg-card p-6">
			<h3 class="text-lg font-semibold mb-4">Recent Routes</h3>
			%s
		</div>
	</div>
</div>`,
		statCard("route", "Routes", fmt.Sprintf("%d", overview.TotalRoutes), "Active routes"),
		statCard("server", "Upstreams", fmt.Sprintf("%d/%d", overview.HealthyUpstreams, overview.TotalUpstreams), "Healthy/Total"),
		statCard("activity", "Requests", formatCount(overview.TotalRequests), "Total requests"),
		statCard("alert-circle", "Error Rate", fmt.Sprintf("%.1f%%", overview.ErrorRate), "Current error rate"),
		overview.TotalRoutes, overview.HealthyUpstreams, overview.TotalUpstreams,
		overview.CacheHitRate, stats.Uptime,
		renderRecentRoutes(routes),
	)

	return err
}

// renderRoutesPage renders the routes list page.
func renderRoutesPage(_ context.Context, w io.Writer, routes []*bastion.Route) error {
	_, err := fmt.Fprintf(w, `<div class="space-y-6">
	<div class="flex items-center justify-between">
		<div>
			<h1 class="text-3xl font-bold tracking-tight">Routes</h1>
			<p class="text-muted-foreground mt-1">%d configured routes</p>
		</div>
	</div>
	<div class="rounded-lg border">
		<table class="w-full">
			<thead><tr class="border-b bg-muted/50">
				<th class="px-4 py-3 text-left text-sm font-medium">Path</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Methods</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Protocol</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Targets</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Source</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Status</th>
			</tr></thead>
			<tbody>%s</tbody>
		</table>
	</div>
</div>`, len(routes), renderRouteRows(routes))

	return err
}

// renderUpstreamsPage renders the upstreams page.
func renderUpstreamsPage(_ context.Context, w io.Writer, routes []*bastion.Route) error {
	var totalTargets int
	var healthyTargets int

	for _, r := range routes {
		for _, t := range r.Targets {
			totalTargets++
			if t.Healthy {
				healthyTargets++
			}
		}
	}

	_, err := fmt.Fprintf(w, `<div class="space-y-6">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">Upstreams</h1>
		<p class="text-muted-foreground mt-1">%d targets (%d healthy)</p>
	</div>
	<div class="rounded-lg border">
		<table class="w-full">
			<thead><tr class="border-b bg-muted/50">
				<th class="px-4 py-3 text-left text-sm font-medium">Target URL</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Route</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Weight</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Health</th>
			</tr></thead>
			<tbody>%s</tbody>
		</table>
	</div>
</div>`, totalTargets, healthyTargets, renderUpstreamRows(routes))

	return err
}

// renderServicesPage renders the discovered services page.
func renderServicesPage(_ context.Context, w io.Writer, services []*bastion.DiscoveredService) error {
	if len(services) == 0 {
		_, err := fmt.Fprint(w, `<div class="space-y-6">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">Services</h1>
		<p class="text-muted-foreground mt-1">Discovered services via FARP</p>
	</div>
	<div class="flex flex-col items-center justify-center py-16 text-center">
		<p class="text-muted-foreground">No services discovered yet</p>
	</div>
</div>`)

		return err
	}

	_, err := fmt.Fprintf(w, `<div class="space-y-6">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">Services</h1>
		<p class="text-muted-foreground mt-1">%d discovered services</p>
	</div>
	<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">%s</div>
</div>`, len(services), renderServiceCards(services))

	return err
}

// renderTrafficPage renders the traffic page.
func renderTrafficPage(_ context.Context, w io.Writer, stats *bastion.GatewayStats) error {
	_, err := fmt.Fprintf(w, `<div class="space-y-6">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">Traffic</h1>
		<p class="text-muted-foreground mt-1">Real-time traffic metrics</p>
	</div>
	<div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
		%s %s %s %s
	</div>
	<div class="rounded-lg border bg-card p-6">
		<h3 class="text-lg font-semibold mb-4">Per-Route Statistics</h3>
		<div class="rounded-lg border">
			<table class="w-full">
				<thead><tr class="border-b bg-muted/50">
					<th class="px-4 py-3 text-left text-sm font-medium">Route</th>
					<th class="px-4 py-3 text-left text-sm font-medium">Requests</th>
					<th class="px-4 py-3 text-left text-sm font-medium">Errors</th>
					<th class="px-4 py-3 text-left text-sm font-medium">Avg Latency</th>
				</tr></thead>
				<tbody>%s</tbody>
			</table>
		</div>
	</div>
</div>`,
		statCard("activity", "Total Requests", formatCount(stats.TotalRequests), ""),
		statCard("alert-triangle", "Total Errors", formatCount(stats.TotalErrors), ""),
		statCard("zap", "Rate Limited", formatCount(stats.RateLimited), ""),
		statCard("shield-off", "Circuit Breaks", formatCount(stats.CircuitBreaks), ""),
		renderRouteStatsRows(stats.RouteStats),
	)

	return err
}

// renderHealthPage renders the health page.
func renderHealthPage(_ context.Context, w io.Writer, routes []*bastion.Route) error {
	_, err := fmt.Fprintf(w, `<div class="space-y-6">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">Health</h1>
		<p class="text-muted-foreground mt-1">Upstream health status</p>
	</div>
	<div class="rounded-lg border">
		<table class="w-full">
			<thead><tr class="border-b bg-muted/50">
				<th class="px-4 py-3 text-left text-sm font-medium">Target</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Route</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Status</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Requests</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Errors</th>
			</tr></thead>
			<tbody>%s</tbody>
		</table>
	</div>
</div>`, renderHealthRows(routes))

	return err
}

// renderCircuitsPage renders the circuit breakers page.
func renderCircuitsPage(_ context.Context, w io.Writer, routes []*bastion.Route) error {
	_, err := fmt.Fprintf(w, `<div class="space-y-6">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">Circuit Breakers</h1>
		<p class="text-muted-foreground mt-1">Per-target circuit breaker states</p>
	</div>
	<div class="rounded-lg border">
		<table class="w-full">
			<thead><tr class="border-b bg-muted/50">
				<th class="px-4 py-3 text-left text-sm font-medium">Target</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Route</th>
				<th class="px-4 py-3 text-left text-sm font-medium">State</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Active Conns</th>
			</tr></thead>
			<tbody>%s</tbody>
		</table>
	</div>
</div>`, renderCircuitRows(routes))

	return err
}

// renderAPIExplorerPage renders the API Explorer page with an embedded Swagger UI
// and service summary information.
func renderAPIExplorerPage(_ context.Context, w io.Writer, openAPI *bastion.OpenAPIAggregator, basePath string) error {
	hasSpec := openAPI != nil && openAPI.MergedSpec() != nil

	if !hasSpec {
		_, err := fmt.Fprint(w, `<div class="space-y-6">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">API Explorer</h1>
		<p class="text-muted-foreground mt-1">Aggregated OpenAPI specification</p>
	</div>
	<div class="flex flex-col items-center justify-center py-16 text-center">
		<p class="text-muted-foreground">OpenAPI spec not available yet. Enable OpenAPI aggregation or wait for the first refresh.</p>
	</div>
</div>`)

		return err
	}

	specPath := basePath + openAPI.SpecPath()
	swaggerUIPath := basePath + openAPI.UIPath()
	refreshPath := basePath + "/api/openapi/refresh"

	// Build service summary rows
	serviceRows := ""
	specs := openAPI.ServiceSpecs()
	serviceCount := len(specs)
	healthyCount := 0

	for _, spec := range specs {
		if spec.Healthy {
			healthyCount++
		}

		statusBadge := `<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300">Healthy</span>`
		errorInfo := ""

		if !spec.Healthy {
			statusBadge = `<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-300">Error</span>`
			if spec.Error != "" {
				errorInfo = fmt.Sprintf(`<span class="text-xs text-muted-foreground ml-2">%s</span>`, spec.Error)
			}
		}

		serviceRows += fmt.Sprintf(`<tr class="border-b">
			<td class="px-4 py-3 text-sm font-medium">%s</td>
			<td class="px-4 py-3 text-sm text-muted-foreground">%s</td>
			<td class="px-4 py-3 text-sm">%d</td>
			<td class="px-4 py-3 text-sm">%s%s</td>
			<td class="px-4 py-3 text-sm"><a href="%s/api/openapi/services/%s" target="_blank" class="text-primary hover:underline">View Spec</a></td>
		</tr>`, spec.ServiceName, spec.Version, spec.PathCount, statusBadge, errorInfo, basePath, spec.ServiceName)
	}

	if serviceRows == "" {
		serviceRows = `<tr><td colspan="5" class="px-4 py-8 text-center text-muted-foreground">No upstream services discovered yet</td></tr>`
	}

	_, err := fmt.Fprintf(w, `<div class="space-y-6">
	<div class="flex items-center justify-between">
		<div>
			<h1 class="text-3xl font-bold tracking-tight">API Explorer</h1>
			<p class="text-muted-foreground mt-1">Aggregated OpenAPI specification from all upstream services</p>
		</div>
		<div class="flex items-center gap-3">
			<a href="%s" target="_blank" class="inline-flex items-center gap-2 rounded-md border px-3 py-2 text-sm font-medium hover:bg-accent">
				<svg class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"/></svg>
				OpenAPI JSON
			</a>
			<button onclick="fetch('%s',{method:'POST'}).then(()=>window.location.reload())" class="inline-flex items-center gap-2 rounded-md bg-primary text-primary-foreground px-3 py-2 text-sm font-medium hover:bg-primary/90">
				<svg class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/></svg>
				Refresh Specs
			</button>
		</div>
	</div>

	<div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
		%s %s %s %s
	</div>

	<div class="rounded-lg border bg-card">
		<div class="p-4 border-b">
			<h3 class="text-lg font-semibold">Discovered Services</h3>
			<p class="text-sm text-muted-foreground mt-1">OpenAPI specs from upstream services</p>
		</div>
		<table class="w-full">
			<thead><tr class="border-b bg-muted/50">
				<th class="px-4 py-3 text-left text-sm font-medium">Service</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Version</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Paths</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Status</th>
				<th class="px-4 py-3 text-left text-sm font-medium">Spec</th>
			</tr></thead>
			<tbody>%s</tbody>
		</table>
	</div>

	<div class="rounded-lg border bg-card">
		<div class="p-4 border-b flex items-center justify-between">
			<div>
				<h3 class="text-lg font-semibold">Swagger UI</h3>
				<p class="text-sm text-muted-foreground mt-1">Interactive API documentation</p>
			</div>
			<a href="%s" target="_blank" class="text-sm text-primary hover:underline">Open in new tab</a>
		</div>
		<div class="w-full" style="min-height: 800px;">
			<iframe src="%s" style="width:100%%;height:800px;border:none;" title="Swagger UI"></iframe>
		</div>
	</div>
</div>`,
		specPath, refreshPath,
		statCard("server", "Services", fmt.Sprintf("%d", serviceCount), "Discovered"),
		statCard("check-circle", "Healthy", fmt.Sprintf("%d/%d", healthyCount, serviceCount), "Specs available"),
		statCard("file-text", "Total Paths", fmt.Sprintf("%d", countMergedPaths(openAPI)), "Across all services"),
		statCard("clock", "Last Refresh", formatRefreshTime(openAPI.LastRefresh()), ""),
		serviceRows,
		swaggerUIPath, swaggerUIPath,
	)

	return err
}

// countMergedPaths returns the total number of paths in the merged spec.
func countMergedPaths(oa *bastion.OpenAPIAggregator) int {
	spec := oa.MergedSpecMap()
	if spec == nil {
		return 0
	}

	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		return 0
	}

	return len(paths)
}

// formatRefreshTime formats the last refresh time for display.
func formatRefreshTime(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}

	return t.Format("15:04:05")
}

// renderConfigPage renders the config page.
func renderConfigPage(_ context.Context, w io.Writer, config bastion.Config) error {
	_, err := fmt.Fprintf(w, `<div class="space-y-6">
	<div>
		<h1 class="text-3xl font-bold tracking-tight">Configuration</h1>
		<p class="text-muted-foreground mt-1">Current gateway settings (read-only)</p>
	</div>
	<div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
		<div class="rounded-lg border bg-card p-6">
			<h3 class="text-lg font-semibold mb-4">General</h3>
			<div class="space-y-3 text-sm">
				<div class="flex justify-between"><span class="text-muted-foreground">Enabled</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Base Path</span><span class="font-medium">%s</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Load Balancing</span><span class="font-medium">%s</span></div>
			</div>
		</div>
		<div class="rounded-lg border bg-card p-6">
			<h3 class="text-lg font-semibold mb-4">Resilience</h3>
			<div class="space-y-3 text-sm">
				<div class="flex justify-between"><span class="text-muted-foreground">Circuit Breaker</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Rate Limiting</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Retry</span><span class="font-medium">%v (max %d)</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Health Check</span><span class="font-medium">%v</span></div>
			</div>
		</div>
		<div class="rounded-lg border bg-card p-6">
			<h3 class="text-lg font-semibold mb-4">Security</h3>
			<div class="space-y-3 text-sm">
				<div class="flex justify-between"><span class="text-muted-foreground">Auth</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">TLS</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">IP Filter</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">CORS</span><span class="font-medium">%v</span></div>
			</div>
		</div>
		<div class="rounded-lg border bg-card p-6">
			<h3 class="text-lg font-semibold mb-4">Features</h3>
			<div class="space-y-3 text-sm">
				<div class="flex justify-between"><span class="text-muted-foreground">Caching</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Discovery</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">OpenAPI</span><span class="font-medium">%v</span></div>
				<div class="flex justify-between"><span class="text-muted-foreground">Metrics</span><span class="font-medium">%v</span></div>
			</div>
		</div>
	</div>
</div>`,
		config.Enabled, orDefault(config.BasePath, "/"),
		config.LoadBalancing.Strategy,
		config.CircuitBreaker.Enabled, config.RateLimiting.Enabled,
		config.Retry.Enabled, config.Retry.MaxAttempts,
		config.HealthCheck.Enabled,
		config.Auth.Enabled, config.TLS.Enabled,
		config.IPFilter.Enabled, config.CORS.Enabled,
		config.Caching.Enabled, config.Discovery.Enabled,
		config.OpenAPI.Enabled, config.Metrics.Enabled,
	)

	return err
}

// --- Render helpers ---

func statCard(icon, label, value, subtitle string) string {
	subtitleHTML := ""
	if subtitle != "" {
		subtitleHTML = fmt.Sprintf(`<span class="text-xs text-muted-foreground">%s</span>`, subtitle)
	}

	return fmt.Sprintf(`<div class="rounded-lg border bg-card p-6">
	<div class="flex items-center justify-between mb-2">
		<span class="text-sm text-muted-foreground">%s</span>
	</div>
	<div class="text-3xl font-bold">%s</div>
	%s
</div>`, label, value, subtitleHTML)
}

func renderRecentRoutes(routes []*bastion.Route) string {
	if len(routes) == 0 {
		return `<p class="text-sm text-muted-foreground">No routes configured</p>`
	}

	limit := 5
	if len(routes) < limit {
		limit = len(routes)
	}

	html := `<div class="space-y-2">`

	for _, r := range routes[:limit] {
		statusClass := "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300"
		statusText := "Active"
		if !r.Enabled {
			statusClass = "bg-gray-100 text-gray-800 dark:bg-gray-900 dark:text-gray-300"
			statusText = "Disabled"
		}

		html += fmt.Sprintf(`<div class="flex items-center justify-between text-sm">
		<span class="font-mono">%s</span>
		<span class="px-2 py-0.5 rounded-full text-xs %s">%s</span>
	</div>`, r.Path, statusClass, statusText)
	}

	html += `</div>`

	return html
}

func renderRouteRows(routes []*bastion.Route) string {
	if len(routes) == 0 {
		return `<tr><td colspan="6" class="px-4 py-8 text-center text-muted-foreground">No routes configured</td></tr>`
	}

	html := ""

	for _, r := range routes {
		methods := "-"
		if len(r.Methods) > 0 {
			methods = fmt.Sprintf("%v", r.Methods)
		}

		status := `<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300">Active</span>`
		if !r.Enabled {
			status = `<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs bg-gray-100 text-gray-800 dark:bg-gray-900 dark:text-gray-300">Disabled</span>`
		}

		html += fmt.Sprintf(`<tr class="border-b">
		<td class="px-4 py-3 text-sm font-mono">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm">%d</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
	</tr>`, r.Path, methods, r.Protocol, len(r.Targets), r.Source, status)
	}

	return html
}

func renderUpstreamRows(routes []*bastion.Route) string {
	html := ""

	for _, r := range routes {
		for _, t := range r.Targets {
			healthBadge := `<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300">Healthy</span>`
			if !t.Healthy {
				healthBadge = `<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-300">Unhealthy</span>`
			}

			html += fmt.Sprintf(`<tr class="border-b">
		<td class="px-4 py-3 text-sm font-mono">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm">%d</td>
		<td class="px-4 py-3 text-sm">%s</td>
	</tr>`, t.URL, r.Path, t.Weight, healthBadge)
		}
	}

	if html == "" {
		return `<tr><td colspan="4" class="px-4 py-8 text-center text-muted-foreground">No upstreams configured</td></tr>`
	}

	return html
}

func renderServiceCards(services []*bastion.DiscoveredService) string {
	html := ""

	for _, svc := range services {
		healthBadge := `<span class="px-2 py-0.5 rounded-full text-xs bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-300">Healthy</span>`
		if !svc.Healthy {
			healthBadge = `<span class="px-2 py-0.5 rounded-full text-xs bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-300">Unhealthy</span>`
		}

		html += fmt.Sprintf(`<div class="rounded-lg border bg-card p-6">
		<div class="flex items-center justify-between mb-3">
			<h4 class="font-semibold">%s</h4>
			%s
		</div>
		<div class="space-y-2 text-sm">
			<div class="flex justify-between"><span class="text-muted-foreground">Version</span><span>%s</span></div>
			<div class="flex justify-between"><span class="text-muted-foreground">Address</span><span class="font-mono">%s:%d</span></div>
			<div class="flex justify-between"><span class="text-muted-foreground">Routes</span><span>%d</span></div>
		</div>
	</div>`, svc.Name, healthBadge, svc.Version, svc.Address, svc.Port, svc.RouteCount)
	}

	return html
}

func renderRouteStatsRows(routeStats map[string]*bastion.RouteStats) string {
	if len(routeStats) == 0 {
		return `<tr><td colspan="4" class="px-4 py-8 text-center text-muted-foreground">No traffic data yet</td></tr>`
	}

	html := ""

	for _, rs := range routeStats {
		html += fmt.Sprintf(`<tr class="border-b">
		<td class="px-4 py-3 text-sm font-mono">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm">%.1fms</td>
	</tr>`, rs.Path, formatCount(rs.TotalRequests), formatCount(rs.TotalErrors), rs.AvgLatencyMs)
	}

	return html
}

func renderHealthRows(routes []*bastion.Route) string {
	html := ""

	for _, r := range routes {
		for _, t := range r.Targets {
			t.Snapshot()

			healthBadge := `<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs bg-green-100 text-green-800">Healthy</span>`
			if !t.Healthy {
				healthBadge = `<span class="inline-flex items-center px-2 py-0.5 rounded-full text-xs bg-red-100 text-red-800">Unhealthy</span>`
			}

			html += fmt.Sprintf(`<tr class="border-b">
		<td class="px-4 py-3 text-sm font-mono">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm">%d</td>
		<td class="px-4 py-3 text-sm">%d</td>
	</tr>`, t.URL, r.Path, healthBadge, t.TotalRequests, t.TotalErrors)
		}
	}

	if html == "" {
		return `<tr><td colspan="5" class="px-4 py-8 text-center text-muted-foreground">No targets configured</td></tr>`
	}

	return html
}

func renderCircuitRows(routes []*bastion.Route) string {
	html := ""

	for _, r := range routes {
		for _, t := range r.Targets {
			t.Snapshot()

			stateClass := "bg-green-100 text-green-800"
			stateText := string(t.CircuitState)
			if stateText == "" {
				stateText = "closed"
			}

			switch bastion.CircuitState(stateText) {
			case bastion.CircuitOpen:
				stateClass = "bg-red-100 text-red-800"
			case bastion.CircuitHalfOpen:
				stateClass = "bg-yellow-100 text-yellow-800"
			}

			html += fmt.Sprintf(`<tr class="border-b">
		<td class="px-4 py-3 text-sm font-mono">%s</td>
		<td class="px-4 py-3 text-sm">%s</td>
		<td class="px-4 py-3 text-sm"><span class="px-2 py-0.5 rounded-full text-xs %s">%s</span></td>
		<td class="px-4 py-3 text-sm">%d</td>
	</tr>`, t.URL, r.Path, stateClass, stateText, t.ActiveConns)
		}
	}

	if html == "" {
		return `<tr><td colspan="4" class="px-4 py-8 text-center text-muted-foreground">No targets configured</td></tr>`
	}

	return html
}

func formatCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}

	if n < 1000000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}

	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}

	return s
}
