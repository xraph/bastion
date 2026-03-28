package bastion

// This file consolidates backward-compatible type aliases and lightweight
// interface definitions that re-export types from domain subpackages.
// New code should import the subpackage directly; these aliases exist
// so that existing consumers can continue using the bastion.* names.

import (
	"context"
	"net/http"

	disc "github.com/xraph/bastion/discovery"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/middleware"
	"github.com/xraph/bastion/observability"
	"github.com/xraph/bastion/security"
)

// --- Auth aliases (security/) ---

// AuthProvider is the gateway's interface for authentication providers.
//
// Canonical definition: security.AuthProvider
type AuthProvider = security.AuthProvider

// GatewayAuthContext holds authenticated subject information for gateway requests.
//
// Canonical definition: security.AuthContext
type GatewayAuthContext = security.AuthContext

// AuthRegistry manages authentication providers for the gateway.
//
// Canonical definition: security.AuthRegistry
type AuthRegistry = security.AuthRegistry

// AuthError represents an authentication failure.
//
// Canonical definition: security.AuthError
type AuthError = security.AuthError

// --- Cache aliases (middleware/) ---

// CacheStore is the gateway's interface for a cache backend.
//
// Canonical definition: middleware.CacheStore
type CacheStore = middleware.CacheStore

// CachedResponse represents a cached HTTP response.
//
// Canonical definition: middleware.CachedResponse
type CachedResponse = middleware.CachedResponse

// --- Health aliases (health/) ---

// HealthCheckResult records a single health check outcome.
//
// Canonical definition: health.CheckResult
type HealthCheckResult = health.CheckResult

// HealthSummary provides a summary of health check history for a target.
//
// Canonical definition: health.Summary
type HealthSummary = health.Summary

// --- Audit aliases (observability/) ---

// AuditConfig configures audit logging.
//
// Canonical definition: observability.AuditConfig
type AuditConfig = observability.AuditConfig

// AuditAction represents the type of admin action taken.
//
// Canonical definition: observability.AuditAction
type AuditAction = observability.AuditAction

const (
	AuditRouteCreated    AuditAction = observability.AuditRouteCreated
	AuditRouteUpdated    AuditAction = observability.AuditRouteUpdated
	AuditRouteDeleted    AuditAction = observability.AuditRouteDeleted
	AuditRouteToggled    AuditAction = observability.AuditRouteToggled
	AuditConfigChanged   AuditAction = observability.AuditConfigChanged
	AuditDiscoveryForced AuditAction = observability.AuditDiscoveryForced
	AuditCircuitReset    AuditAction = observability.AuditCircuitReset
	AuditCacheCleared    AuditAction = observability.AuditCacheCleared
)

// AuditEvent represents an auditable action in the gateway.
//
// Canonical definition: observability.AuditEvent
type AuditEvent = observability.AuditEvent

// AuditSink is the interface for consuming audit events.
//
// Canonical definition: observability.AuditSink
type AuditSink = observability.AuditSink

// --- Rate limit aliases (middleware/) ---

// RateLimitStore is the interface for distributed rate limiting backends.
// Implementations must be safe for concurrent use.
//
// Canonical definition: middleware.RateLimitStore
type RateLimitStore = middleware.RateLimitStore

// --- Discovery types ---

// DiscoveryService is the interface that the gateway requires from a discovery provider.
// This decouples the gateway from the concrete discovery.Service type.
type DiscoveryService interface {
	// ListServices lists all registered service names.
	ListServices(ctx context.Context) ([]string, error)

	// DiscoverHealthy returns healthy instances for a service.
	DiscoverHealthy(ctx context.Context, serviceName string) ([]*ServiceInstanceInfo, error)
}

// ServiceInstanceInfo represents a discovered service instance.
//
// Canonical definition: discovery.ServiceInstanceInfo
type ServiceInstanceInfo = disc.ServiceInstanceInfo

// --- OpenAPI aliases (discovery/) ---

// OpenAPIConfig holds configuration for the OpenAPI aggregation feature.
//
// Canonical definition: discovery.OpenAPIConfig
type OpenAPIConfig = disc.OpenAPIConfig

// DefaultOpenAPIConfig returns defaults for OpenAPI aggregation.
func DefaultOpenAPIConfig() OpenAPIConfig {
	return disc.DefaultOpenAPIConfig()
}

// ServiceOpenAPISpec holds a cached OpenAPI spec for a single upstream service.
//
// Canonical definition: discovery.ServiceOpenAPISpec
type ServiceOpenAPISpec = disc.ServiceOpenAPISpec

// ExtensionPathFilter defines per-service extension path filtering.
//
// Canonical definition: discovery.ExtensionPathFilter
type ExtensionPathFilter = disc.ExtensionPathFilter

// --- Plugin types ---

// GatewayPlugin is a composable extension point for the gateway.
// Plugins are invoked in registration order at the request, response,
// and error stages of the proxy pipeline.
type GatewayPlugin interface {
	// Name returns a unique identifier for the plugin.
	Name() string

	// OnRequest is called before a request is forwarded to the upstream.
	// Return a non-nil error to reject the request (short-circuits the pipeline).
	OnRequest(r *http.Request, route *Route) error

	// OnResponse is called after the upstream response is received.
	OnResponse(resp *http.Response, route *Route)

	// OnError is called when the upstream returns an error.
	OnError(err error, route *Route, w http.ResponseWriter)
}

// BasePlugin provides no-op defaults for the GatewayPlugin interface,
// allowing plugins to override only the methods they care about.
type BasePlugin struct {
	PluginName string
}

func (bp *BasePlugin) Name() string { return bp.PluginName }

func (bp *BasePlugin) OnRequest(_ *http.Request, _ *Route) error { return nil }

func (bp *BasePlugin) OnResponse(_ *http.Response, _ *Route) {}

func (bp *BasePlugin) OnError(_ error, _ *Route, _ http.ResponseWriter) {}

// --- Load balancer interface ---

// LoadBalancer selects a target from a list of healthy targets.
type LoadBalancer interface {
	// Select picks a target from the provided list.
	// The key is used for consistent hashing (may be empty for other strategies).
	Select(targets []*Target, key string) *Target
}

// --- Gateway accessor ---

// AccessLog returns the access logger.
func (e *Gateway) AccessLog() *AccessLogger { return e.accessLog }
