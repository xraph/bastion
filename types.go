package bastion

import (
	"sync/atomic"
	"time"

	disc "github.com/xraph/bastion/discovery"
	"github.com/xraph/bastion/middleware"
	"github.com/xraph/bastion/observability"
	"github.com/xraph/bastion/security"
)

// RouteProtocol defines the protocol type for a gateway route.
type RouteProtocol string

const (
	// ProtocolHTTP is standard HTTP/HTTPS proxying.
	ProtocolHTTP RouteProtocol = "http"

	// ProtocolWebSocket is WebSocket proxying.
	ProtocolWebSocket RouteProtocol = "websocket"

	// ProtocolSSE is Server-Sent Events proxying.
	ProtocolSSE RouteProtocol = "sse"

	// ProtocolGRPC is gRPC proxying.
	ProtocolGRPC RouteProtocol = "grpc"

	// ProtocolGraphQL is GraphQL proxying (HTTP-based with special handling).
	ProtocolGraphQL RouteProtocol = "graphql"
)

// RouteSource indicates how a route was created.
type RouteSource string

const (
	// SourceManual indicates a manually configured route.
	SourceManual RouteSource = "manual"

	// SourceFARP indicates a route auto-generated from FARP schemas.
	SourceFARP RouteSource = "farp"

	// SourceDiscovery indicates a route from service discovery.
	SourceDiscovery RouteSource = "discovery"
)

// CircuitState represents the circuit breaker state.
// The canonical definition lives in the observability subpackage.
type CircuitState = observability.CircuitState

const (
	// CircuitClosed allows requests through (normal operation).
	CircuitClosed CircuitState = observability.CircuitClosed

	// CircuitOpen rejects requests (fail-fast).
	CircuitOpen CircuitState = observability.CircuitOpen

	// CircuitHalfOpen allows limited probe requests.
	CircuitHalfOpen CircuitState = observability.CircuitHalfOpen
)

// TrafficSplitType defines the traffic splitting strategy.
type TrafficSplitType string

const (
	// TrafficCanary routes a percentage of traffic to canary targets.
	TrafficCanary TrafficSplitType = "canary"

	// TrafficBlueGreen switches between two target groups.
	TrafficBlueGreen TrafficSplitType = "blueGreen"

	// TrafficABTest routes based on header/cookie matching.
	TrafficABTest TrafficSplitType = "abTest"

	// TrafficMirror duplicates requests to a mirror target.
	TrafficMirror TrafficSplitType = "mirror"

	// TrafficWeighted uses explicit weight distribution.
	TrafficWeighted TrafficSplitType = "weighted"
)

// TrafficMatchType defines how traffic rules are matched.
type TrafficMatchType string

const (
	// MatchHeader matches on request header value.
	MatchHeader TrafficMatchType = "header"

	// MatchCookie matches on cookie value.
	MatchCookie TrafficMatchType = "cookie"

	// MatchWeight matches by random weight percentage.
	MatchWeight TrafficMatchType = "weight"

	// MatchIPRange matches by client IP range.
	MatchIPRange TrafficMatchType = "ipRange"
)

// LoadBalanceStrategy defines load balancing algorithms.
type LoadBalanceStrategy string

const (
	// LBRoundRobin selects targets in round-robin order.
	LBRoundRobin LoadBalanceStrategy = "roundRobin"

	// LBWeightedRoundRobin selects targets using smooth weighted round-robin.
	LBWeightedRoundRobin LoadBalanceStrategy = "weightedRoundRobin"

	// LBRandom selects a random target.
	LBRandom LoadBalanceStrategy = "random"

	// LBLeastConnections selects the target with fewest active connections.
	LBLeastConnections LoadBalanceStrategy = "leastConnections"

	// LBConsistentHash uses consistent hashing for sticky routing.
	LBConsistentHash LoadBalanceStrategy = "consistentHash"
)

// BackoffStrategy defines retry backoff algorithms.
type BackoffStrategy string

const (
	// BackoffExponential uses exponential backoff.
	BackoffExponential BackoffStrategy = "exponential"

	// BackoffLinear uses linear backoff.
	BackoffLinear BackoffStrategy = "linear"

	// BackoffFixed uses a fixed delay.
	BackoffFixed BackoffStrategy = "fixed"
)

// Route represents a configured gateway route with full policy support.
type Route struct {
	ID          string        `json:"id"`
	Path        string        `json:"path"`
	Methods     []string      `json:"methods,omitempty"`
	Targets     []*Target     `json:"targets"`
	StripPrefix bool          `json:"stripPrefix"`
	AddPrefix   string        `json:"addPrefix,omitempty"`
	RewritePath string        `json:"rewritePath,omitempty"`
	Headers     HeaderPolicy  `json:"headers"`
	Protocol    RouteProtocol `json:"protocol"`
	Source      RouteSource   `json:"source"`
	ServiceName string        `json:"serviceName,omitempty"`
	Priority    int           `json:"priority"`
	Version     int64         `json:"version"`
	Enabled     bool          `json:"enabled"`

	// Per-route overrides (nil = use global defaults)
	Retry          *RetryConfig      `json:"retry,omitempty"`
	Timeout        *TimeoutConfig    `json:"timeout,omitempty"`
	RateLimit      *RateLimitConfig  `json:"rateLimit,omitempty"`
	Auth           *RouteAuthConfig  `json:"auth,omitempty"`
	CircuitBreaker *CBConfig         `json:"circuitBreaker,omitempty"`
	Cache          *RouteCacheConfig `json:"cache,omitempty"`
	TrafficPolicy  *TrafficPolicy    `json:"trafficPolicy,omitempty"`
	Transform      *TransformConfig  `json:"transform,omitempty"`

	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// GetID returns the route ID (implements security.Route).
func (r *Route) GetID() string { return r.ID }

// GetAuth returns the per-route auth config (implements security.Route).
func (r *Route) GetAuth() *RouteAuthConfig { return r.Auth }

// Target represents an upstream service endpoint.
type Target struct {
	ID           string            `json:"id"`
	URL          string            `json:"url"`
	Weight       int               `json:"weight"`
	Healthy      bool              `json:"healthy"`
	ActiveConns  int64             `json:"activeConns"`
	CircuitState CircuitState      `json:"circuitState"`
	TLS          *TargetTLSConfig  `json:"tls,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Tags         []string          `json:"tags,omitempty"`

	// Stats
	TotalRequests int64   `json:"totalRequests"`
	TotalErrors   int64   `json:"totalErrors"`
	AvgLatencyMs  float64 `json:"avgLatencyMs"`
	P99LatencyMs  float64 `json:"p99LatencyMs"`

	// Internal (not serialized)
	activeConns atomic.Int64 `json:"-"`
	totalReqs   atomic.Int64 `json:"-"`
	totalErrs   atomic.Int64 `json:"-"`
	latencySum  atomic.Int64 `json:"-"` // nanoseconds total
	draining    atomic.Bool  `json:"-"`
}

// GetID returns the target ID (implements health.Target).
func (t *Target) GetID() string { return t.ID }

// GetURL returns the target URL (implements health.Target).
func (t *Target) GetURL() string { return t.URL }

// GetTLS returns the per-target TLS config (implements security.Target).
func (t *Target) GetTLS() *TargetTLSConfig { return t.TLS }

// IsHealthy returns whether the target is healthy (implements health.Target).
func (t *Target) IsHealthy() bool { return t.Healthy }

// SetHealthy sets the target health status (implements health.Target).
func (t *Target) SetHealthy(healthy bool) { t.Healthy = healthy }

// HealthPath returns a custom health check path for this target, if one is
// set in its metadata (key "health_check_path"). Returns empty string to
// use the monitor's default path. Implements health.HealthPathProvider.
func (t *Target) HealthPath() string {
	if t.Metadata != nil {
		if p, ok := t.Metadata["health_check_path"]; ok {
			return p
		}
	}

	return ""
}

// IncrConns increments active connections.
func (t *Target) IncrConns() { t.activeConns.Add(1) }

// DecrConns decrements active connections.
func (t *Target) DecrConns() { t.activeConns.Add(-1) }

// RecordRequest records a request outcome.
func (t *Target) RecordRequest(latency time.Duration, isError bool) {
	t.totalReqs.Add(1)
	t.latencySum.Add(latency.Nanoseconds())

	if isError {
		t.totalErrs.Add(1)
	}
}

// IsDraining returns true if the target is draining.
func (t *Target) IsDraining() bool { return t.draining.Load() }

// SetDraining sets the draining state.
func (t *Target) SetDraining(v bool) { t.draining.Store(v) }

// Snapshot populates the exported stats fields from atomics.
func (t *Target) Snapshot() {
	t.ActiveConns = t.activeConns.Load()
	t.TotalRequests = t.totalReqs.Load()
	t.TotalErrors = t.totalErrs.Load()

	if t.TotalRequests > 0 {
		t.AvgLatencyMs = float64(t.latencySum.Load()) / float64(t.TotalRequests) / 1e6
	}
}

// HeaderPolicy defines header manipulation rules.
type HeaderPolicy struct {
	Add    map[string]string `json:"add,omitempty"`
	Set    map[string]string `json:"set,omitempty"`
	Remove []string          `json:"remove,omitempty"`
}

// TrafficPolicy defines traffic splitting rules for a route.
type TrafficPolicy struct {
	Type         TrafficSplitType `json:"type"`
	Rules        []TrafficRule    `json:"rules"`
	MirrorTarget string           `json:"mirrorTarget,omitempty"`
}

// TrafficRule defines a single traffic routing rule.
type TrafficRule struct {
	Match      TrafficMatch `json:"match"`
	TargetTags []string     `json:"targetTags"`
	Weight     int          `json:"weight"`
}

// TrafficMatch defines traffic matching criteria.
type TrafficMatch struct {
	Type   TrafficMatchType `json:"type"`
	Key    string           `json:"key,omitempty"`
	Value  string           `json:"value,omitempty"`
	Negate bool             `json:"negate,omitempty"`
}

// TransformConfig defines request/response transformation.
type TransformConfig struct {
	RequestHeaders  HeaderPolicy `json:"requestHeaders"`
	ResponseHeaders HeaderPolicy `json:"responseHeaders"`
}

// TargetTLSConfig holds per-target TLS configuration.
//
// Canonical definition: security.TargetTLSConfig
type TargetTLSConfig = security.TargetTLSConfig

// RouteAuthConfig defines per-route auth requirements.
//
// Canonical definition: security.RouteAuthConfig
type RouteAuthConfig = security.RouteAuthConfig

// RouteCacheConfig defines per-route caching policy.
//
// Canonical definition: middleware.RouteCacheConfig
type RouteCacheConfig = middleware.RouteCacheConfig

// CBConfig is a per-route circuit breaker override.
type CBConfig struct {
	FailureThreshold int           `json:"failureThreshold"`
	ResetTimeout     time.Duration `json:"resetTimeout"`
	HalfOpenMax      int           `json:"halfOpenMax"`
}

// GatewayStats holds aggregated gateway traffic statistics.
type GatewayStats struct {
	TotalRequests    int64                  `json:"totalRequests"`
	TotalErrors      int64                  `json:"totalErrors"`
	ActiveConns      int64                  `json:"activeConns"`
	ActiveWSConns    int64                  `json:"activeWsConns"`
	ActiveSSEConns   int64                  `json:"activeSseConns"`
	AvgLatencyMs     float64                `json:"avgLatencyMs"`
	P99LatencyMs     float64                `json:"p99LatencyMs"`
	RequestsPerSec   float64                `json:"requestsPerSec"`
	CacheHits        int64                  `json:"cacheHits"`
	CacheMisses      int64                  `json:"cacheMisses"`
	RateLimited      int64                  `json:"rateLimited"`
	CircuitBreaks    int64                  `json:"circuitBreaks"`
	RetryAttempts    int64                  `json:"retryAttempts"`
	TotalRoutes      int                    `json:"totalRoutes"`
	HealthyUpstreams int                    `json:"healthyUpstreams"`
	TotalUpstreams   int                    `json:"totalUpstreams"`
	RouteStats       map[string]*RouteStats `json:"routeStats,omitempty"`
	Uptime           int64                  `json:"uptime"`
	StartedAt        time.Time              `json:"startedAt"`
}

// RouteStats holds per-route traffic statistics.
type RouteStats struct {
	RouteID       string  `json:"routeId"`
	Path          string  `json:"path"`
	TotalRequests int64   `json:"totalRequests"`
	TotalErrors   int64   `json:"totalErrors"`
	AvgLatencyMs  float64 `json:"avgLatencyMs"`
	P99LatencyMs  float64 `json:"p99LatencyMs"`
	CacheHits     int64   `json:"cacheHits"`
	CacheMisses   int64   `json:"cacheMisses"`
	RateLimited   int64   `json:"rateLimited"`
}

// RouteEvent represents a change in the route table.
type RouteEvent struct {
	Type      RouteEventType `json:"type"`
	Route     *Route         `json:"route"`
	Timestamp time.Time      `json:"timestamp"`
}

// RouteEventType represents types of route events.
type RouteEventType string

const (
	// RouteEventAdded indicates a new route was added.
	RouteEventAdded RouteEventType = "added"

	// RouteEventUpdated indicates a route was updated.
	RouteEventUpdated RouteEventType = "updated"

	// RouteEventRemoved indicates a route was removed.
	RouteEventRemoved RouteEventType = "removed"
)

// UpstreamHealthEvent represents an upstream health change.
type UpstreamHealthEvent struct {
	TargetID  string    `json:"targetId"`
	TargetURL string    `json:"targetUrl"`
	Healthy   bool      `json:"healthy"`
	Previous  bool      `json:"previous"`
	RouteID   string    `json:"routeId"`
	Timestamp time.Time `json:"timestamp"`
}

// DiscoveredService represents a service found via FARP/discovery.
//
// Canonical definition: discovery.DiscoveredService
type DiscoveredService = disc.DiscoveredService

// RouteDTO is the API request body for creating/updating a route.
type RouteDTO struct {
	Path        string        `json:"path"`
	Methods     []string      `json:"methods,omitempty"`
	Targets     []TargetDTO   `json:"targets"`
	StripPrefix bool          `json:"stripPrefix"`
	AddPrefix   string        `json:"addPrefix,omitempty"`
	RewritePath string        `json:"rewritePath,omitempty"`
	Headers     HeaderPolicy  `json:"headers"`
	Protocol    RouteProtocol `json:"protocol"`
	Priority    int           `json:"priority"`
	Enabled     bool          `json:"enabled"`

	Retry          *RetryConfig      `json:"retry,omitempty"`
	Timeout        *TimeoutConfig    `json:"timeout,omitempty"`
	RateLimit      *RateLimitConfig  `json:"rateLimit,omitempty"`
	Auth           *RouteAuthConfig  `json:"auth,omitempty"`
	CircuitBreaker *CBConfig         `json:"circuitBreaker,omitempty"`
	Cache          *RouteCacheConfig `json:"cache,omitempty"`
	TrafficPolicy  *TrafficPolicy    `json:"trafficPolicy,omitempty"`
	Transform      *TransformConfig  `json:"transform,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
}

// TargetDTO is the API representation of a target.
type TargetDTO struct {
	URL      string            `json:"url"`
	Weight   int               `json:"weight"`
	Tags     []string          `json:"tags,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	TLS      *TargetTLSConfig  `json:"tls,omitempty"`
}
