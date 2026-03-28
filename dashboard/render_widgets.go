package dashboard

import (
	"context"
	"fmt"
	"io"

	"github.com/xraph/bastion"
)

// renderStatsWidgetHTML renders the gateway stats widget.
func renderStatsWidgetHTML(_ context.Context, w io.Writer, stats *bastion.GatewayStats) error {
	_, err := fmt.Fprintf(w, `<div class="grid gap-4 md:grid-cols-4">
	%s %s %s %s
</div>`,
		statCard("activity", "Requests", formatCount(stats.TotalRequests), ""),
		statCard("alert-triangle", "Errors", formatCount(stats.TotalErrors), ""),
		statCard("clock", "Avg Latency", fmt.Sprintf("%.1fms", stats.AvgLatencyMs), ""),
		statCard("route", "Routes", fmt.Sprintf("%d", stats.TotalRoutes), ""),
	)

	return err
}

// renderHealthWidgetHTML renders the health widget.
func renderHealthWidgetHTML(_ context.Context, w io.Writer, stats *bastion.GatewayStats) error {
	pct := 0.0
	if stats.TotalUpstreams > 0 {
		pct = float64(stats.HealthyUpstreams) / float64(stats.TotalUpstreams) * 100
	}

	barColor := "bg-green-500"
	if pct < 50 {
		barColor = "bg-red-500"
	} else if pct < 80 {
		barColor = "bg-yellow-500"
	}

	_, err := fmt.Fprintf(w, `<div class="space-y-3">
	<div class="flex items-center justify-between">
		<span class="text-sm font-medium">Healthy Upstreams</span>
		<span class="text-sm text-muted-foreground">%d / %d</span>
	</div>
	<div class="h-2 rounded-full bg-muted">
		<div class="h-2 rounded-full %s" style="width: %.0f%%"></div>
	</div>
	<p class="text-xs text-muted-foreground">%.0f%% of upstreams are healthy</p>
</div>`, stats.HealthyUpstreams, stats.TotalUpstreams, barColor, pct, pct)

	return err
}

// renderErrorsWidgetHTML renders the error rate widget.
func renderErrorsWidgetHTML(_ context.Context, w io.Writer, stats *bastion.GatewayStats) error {
	errorRate := 0.0
	if stats.TotalRequests > 0 {
		errorRate = float64(stats.TotalErrors) / float64(stats.TotalRequests) * 100
	}

	rateColor := "text-green-500"
	if errorRate > 5 {
		rateColor = "text-red-500"
	} else if errorRate > 1 {
		rateColor = "text-yellow-500"
	}

	_, err := fmt.Fprintf(w, `<div class="space-y-3">
	<div class="text-3xl font-bold %s">%.1f%%</div>
	<p class="text-sm text-muted-foreground">Error Rate</p>
	<div class="flex items-center gap-4 text-sm">
		<span>Circuit Breaks: <strong>%d</strong></span>
		<span>Rate Limited: <strong>%d</strong></span>
	</div>
</div>`, rateColor, errorRate, stats.CircuitBreaks, stats.RateLimited)

	return err
}

// renderConfigSettingsHTML renders the config settings panel.
func renderConfigSettingsHTML(_ context.Context, w io.Writer, config bastion.Config) error {
	_, err := fmt.Fprintf(w, `<div class="space-y-4 text-sm">
	<div class="flex justify-between"><span class="text-muted-foreground">Gateway Enabled</span><span class="font-medium">%v</span></div>
	<div class="flex justify-between"><span class="text-muted-foreground">Load Balancing</span><span class="font-medium">%s</span></div>
	<div class="flex justify-between"><span class="text-muted-foreground">Circuit Breaker</span><span class="font-medium">%v</span></div>
	<div class="flex justify-between"><span class="text-muted-foreground">Rate Limiting</span><span class="font-medium">%v</span></div>
	<div class="flex justify-between"><span class="text-muted-foreground">Auth</span><span class="font-medium">%v</span></div>
	<div class="flex justify-between"><span class="text-muted-foreground">Caching</span><span class="font-medium">%v</span></div>
	<div class="flex justify-between"><span class="text-muted-foreground">Discovery</span><span class="font-medium">%v</span></div>
	<div class="flex justify-between"><span class="text-muted-foreground">OpenAPI</span><span class="font-medium">%v</span></div>
</div>`,
		config.Enabled, config.LoadBalancing.Strategy,
		config.CircuitBreaker.Enabled, config.RateLimiting.Enabled,
		config.Auth.Enabled, config.Caching.Enabled,
		config.Discovery.Enabled, config.OpenAPI.Enabled,
	)

	return err
}
