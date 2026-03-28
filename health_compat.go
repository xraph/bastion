package bastion

import (
	"github.com/xraph/forge"

	"github.com/xraph/bastion/health"
)

// HealthMonitor is a backward-compatible alias for health.Monitor.
type HealthMonitor = health.Monitor

// NewHealthMonitor creates a new HealthMonitor.
// Deprecated: Use health.NewMonitor directly.
func NewHealthMonitor(config HealthCheckConfig, logger forge.Logger) *HealthMonitor {
	return health.NewMonitor(config, logger)
}

// HealthHistory is a backward-compatible alias for health.History.
type HealthHistory = health.History

// NewHealthHistory creates a new HealthHistory.
// Deprecated: Use health.NewHistory directly.
func NewHealthHistory(capacity int) *HealthHistory {
	return health.NewHistory(capacity)
}

// healthEventToUpstream converts a health.Event to an UpstreamHealthEvent.
func healthEventToUpstream(e health.Event) UpstreamHealthEvent {
	return UpstreamHealthEvent{
		TargetID:  e.TargetID,
		TargetURL: e.TargetURL,
		Healthy:   e.Healthy,
		Previous:  e.Previous,
		RouteID:   e.RouteID,
		Timestamp: e.Timestamp,
	}
}
