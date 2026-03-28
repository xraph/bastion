package health

import "time"

// Config configures upstream health checking.
type Config struct {
	Enabled              bool          `json:"enabled" yaml:"enabled"`
	Interval             time.Duration `json:"interval" yaml:"interval"`
	Timeout              time.Duration `json:"timeout" yaml:"timeout"`
	Path                 string        `json:"path" yaml:"path"`
	FailureThreshold     int           `json:"failureThreshold" yaml:"failure_threshold"`
	SuccessThreshold     int           `json:"successThreshold" yaml:"success_threshold"`
	EnablePassive        bool          `json:"enablePassive" yaml:"enable_passive"`
	PassiveFailThreshold int           `json:"passiveFailThreshold" yaml:"passive_fail_threshold"`
}

// CheckResult records a single health check outcome.
type CheckResult struct {
	TargetID  string        `json:"targetId"`
	TargetURL string        `json:"targetUrl"`
	Healthy   bool          `json:"healthy"`
	Latency   time.Duration `json:"latency"`
	Error     string        `json:"error,omitempty"`
	Timestamp time.Time     `json:"timestamp"`
}

// Summary provides a summary of health check history for a target.
type Summary struct {
	TargetID      string       `json:"targetId"`
	TotalChecks   int          `json:"totalChecks"`
	HealthyChecks int          `json:"healthyChecks"`
	UptimePercent float64      `json:"uptimePercent"`
	AvgLatency    time.Duration `json:"avgLatency"`
	LastCheck     *CheckResult `json:"lastCheck,omitempty"`
}

// Event represents an upstream health change.
type Event struct {
	TargetID  string    `json:"targetId"`
	TargetURL string    `json:"targetUrl"`
	Healthy   bool      `json:"healthy"`
	Previous  bool      `json:"previous"`
	RouteID   string    `json:"routeId"`
	Timestamp time.Time `json:"timestamp"`
}

// Target is a minimal interface for upstream targets used by the health monitor.
type Target interface {
	GetID() string
	GetURL() string
	IsHealthy() bool
	SetHealthy(healthy bool)
}

// HealthPathProvider is an optional interface that targets can implement
// to specify a custom health check path instead of the global default.
// If a target implements this interface and returns a non-empty string,
// that path is used for health checks instead of the monitor's global
// Config.Path.
type HealthPathProvider interface {
	HealthPath() string
}
