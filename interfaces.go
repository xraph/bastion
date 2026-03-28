package bastion

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"

	"github.com/xraph/forge"
)

// --- Subsystem interfaces ---
// These interfaces decouple the Gateway from concrete subsystem implementations,
// enabling them to live in separate subpackages without circular dependencies.
// Root types (Route, Target, Config, etc.) stay here; implementations move to subpackages.
// The extension/ package wires concrete implementations into the Gateway via setters.

// RouteRegistry manages gateway route configuration and matching.
type RouteRegistry interface {
	AddRoute(route *Route) error
	RemoveRoute(id string) error
	UpdateRoute(route *Route) error
	GetRoute(id string) (*Route, bool)
	ListRoutes() []*Route
	MatchRoute(path, method string) *Route
	RouteCount() int
	RemoveBySource(source RouteSource)
	RemoveByServiceName(serviceName string)
	OnRouteChange(fn func(RouteEvent))
}

// HealthChecker manages health monitoring for upstream targets.
type HealthChecker interface {
	Register(routeID string, target *Target)
	Deregister(targetID string)
	RecordPassiveFailure(targetID string)
	RecordPassiveSuccess(targetID string)
	Start(ctx context.Context)
	Stop()
	Health(ctx context.Context) error
	SetOnHealthChange(fn func(event UpstreamHealthEvent))
}

// Breaker represents a single circuit breaker for an upstream target.
type Breaker interface {
	Allow() bool
	RecordSuccess()
	RecordFailure()
	State() CircuitState
	Reset()
}

// CircuitControl manages circuit breakers across all upstream targets.
type CircuitControl interface {
	Get(targetID string) Breaker
	GetWithConfig(targetID string, cfg *CBConfig) Breaker
	Remove(targetID string)
	SetOnStateChange(fn func(targetID string, from, to CircuitState))
}

// RequestLimiter handles request rate limiting.
type RequestLimiter interface {
	Allow(r *http.Request) bool
	AllowWithConfig(r *http.Request, config *RateLimitConfig) bool
	Cleanup(maxAge time.Duration)
}

// StatsRecorder collects and reports gateway traffic statistics.
type StatsRecorder interface {
	Snapshot(routes []*Route) *GatewayStats
	RecordRequest(routeID, path string)
	RecordError(routeID string)
	RecordRateLimited()
	RecordCircuitBreak()
	RecordRetryAttempt()
	RecordCacheHit()
	RecordCacheMiss()
}

// MetricsReporter handles observability metrics for the gateway.
type MetricsReporter interface {
	RecordRequest(routeID, method, status, upstream string)
	RecordLatency(routeID, method, upstream string, seconds float64)
	SetActiveConnections(protocol string, count float64)
	SetUpstreamHealth(targetID string, healthy bool)
	SetCircuitBreakerState(targetID string, state CircuitState)
	RecordRetry(routeID string, attempt int)
	RecordCacheHit()
	RecordCacheMiss()
	RecordRateLimited()
	SetDiscoveryRoutes(source string, count float64)
}

// AccessLogWriter logs HTTP request/response access entries.
type AccessLogWriter interface {
	Log(r *http.Request, statusCode int, latency time.Duration, route *Route, target *Target)
	LogAdminAction(action, resource, result string, r *http.Request)
}

// RetryPolicy determines retry behavior for failed requests.
type RetryPolicy interface {
	ShouldRetry(method string, statusCode int, attempt int, routeConfig *RetryConfig) bool
	Delay(attempt int, routeConfig *RetryConfig) time.Duration
}

// TrafficRouter handles traffic splitting and mirroring.
type TrafficRouter interface {
	FilterTargets(r *http.Request, route *Route) []*Target
	ShouldMirror(route *Route) string
}

// ServiceDiscoverer manages automatic service discovery integration.
type ServiceDiscoverer interface {
	Start(ctx context.Context) error
	Stop()
	Refresh(ctx context.Context) error
	DiscoveredServices() []*DiscoveredService
}

// Authenticator handles gateway-level authentication.
type Authenticator interface {
	SetForgeAuth(registry AuthRegistry)
	RegisterProvider(provider AuthProvider)
	Authenticate(r *http.Request, route *Route) (*GatewayAuthContext, error)
	ForwardAuthHeaders(r *http.Request, authCtx *GatewayAuthContext)
}

// ResponseCacher handles HTTP response caching.
type ResponseCacher interface {
	Get(r *http.Request, route *Route) *CachedResponse
	Set(r *http.Request, route *Route, statusCode int, headers http.Header, body []byte)
	WriteCachedResponse(w http.ResponseWriter, cached *CachedResponse)
	Invalidate(ctx context.Context, method, path string) error
	Stats() CacheStatsReader
}

// CacheStatsReader provides read access to cache statistics.
type CacheStatsReader interface {
	Hits() int64
	Misses() int64
}

// TLSProvider manages TLS certificates and transport configuration.
type TLSProvider interface {
	GlobalTLSConfig() *tls.Config
	TargetTLSConfig(target *Target) *tls.Config
	TransportForTarget(baseTransport *http.Transport, target *Target) *http.Transport
	Start()
	Stop()
	Reload() error
	LastReload() time.Time
}

// OpenAPIService handles OpenAPI spec aggregation and serving.
type OpenAPIService interface {
	Start(ctx context.Context)
	Refresh(ctx context.Context)
	MergedSpec() []byte
	MergedSpecMap() map[string]any
	ServiceSpec(serviceName string) *ServiceOpenAPISpec
	ServiceSpecs() map[string]*ServiceOpenAPISpec
	LastRefresh() time.Time
	HandleMergedSpec(ctx forge.Context) error
	HandleSwaggerUI(ctx forge.Context) error
	HandleServiceList(ctx forge.Context) error
	HandleServiceSpec(ctx forge.Context) error
	HandleRefresh(ctx forge.Context) error
}

// WSBroadcaster manages WebSocket connections for real-time updates.
type WSBroadcaster interface {
	Run()
	Broadcast(data any) error
	ClientCount() int
}
