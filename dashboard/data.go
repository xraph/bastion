package dashboard

import (
	"github.com/xraph/bastion"
	"github.com/xraph/bastion/dashboard/shared"
)

// fetchOverview builds the gateway overview from the gateway instance.
func fetchOverview(gw *bastion.Gateway) shared.GatewayOverview {
	stats := gw.Snapshot()

	overview := shared.GatewayOverview{
		TotalRoutes:      stats.TotalRoutes,
		HealthyUpstreams: stats.HealthyUpstreams,
		TotalUpstreams:   stats.TotalUpstreams,
		TotalRequests:    stats.TotalRequests,
		AvgLatencyMs:     stats.AvgLatencyMs,
	}

	if stats.TotalRequests > 0 {
		overview.ErrorRate = float64(stats.TotalErrors) / float64(stats.TotalRequests) * 100
	}

	totalCacheOps := stats.CacheHits + stats.CacheMisses
	if totalCacheOps > 0 {
		overview.CacheHitRate = float64(stats.CacheHits) / float64(totalCacheOps) * 100
	}

	return overview
}

// fetchRoutes returns all routes from the route manager.
func fetchRoutes(gw *bastion.Gateway) []*bastion.Route {
	return gw.RouteManager().ListRoutes()
}

// fetchStats returns the current gateway stats.
func fetchStats(gw *bastion.Gateway) *bastion.GatewayStats {
	return gw.Snapshot()
}
