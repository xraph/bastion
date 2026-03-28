package bastion

// This file provides backward-compatible type aliases and constructor wrappers
// for types that moved from the root package to the security/ subpackage.
// Root-level code (proxy.go, extension.go, tests) can continue to use the
// original names; new code should import github.com/xraph/bastion/security directly.

import (
	"context"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/bastion/security"
)

// --- Auth (was GatewayAuth) ---

// GatewayAuth is a backward-compatible alias for security.Auth.
type GatewayAuth = security.Auth

// NewGatewayAuth creates a new gateway auth handler.
// Deprecated: Use security.NewAuth directly.
func NewGatewayAuth(config AuthConfig, logger forge.Logger) *GatewayAuth {
	return security.NewAuth(config, logger)
}

// --- Auth Providers ---

// APIKeyAuthProvider is a backward-compatible alias for security.APIKeyAuthProvider.
type APIKeyAuthProvider = security.APIKeyAuthProvider

// APIKeyEntry is a backward-compatible alias for security.APIKeyEntry.
type APIKeyEntry = security.APIKeyEntry

// NewAPIKeyAuthProvider creates an API key auth provider.
// Deprecated: Use security.NewAPIKeyAuthProvider directly.
func NewAPIKeyAuthProvider(name, header, queryParam string, keys []*APIKeyEntry) *APIKeyAuthProvider {
	return security.NewAPIKeyAuthProvider(name, header, queryParam, keys)
}

// BearerTokenAuthProvider is a backward-compatible alias for security.BearerTokenAuthProvider.
type BearerTokenAuthProvider = security.BearerTokenAuthProvider

// NewBearerTokenAuthProvider creates a bearer token auth provider with a custom validator.
// Deprecated: Use security.NewBearerTokenAuthProvider directly.
func NewBearerTokenAuthProvider(name string, validate func(ctx context.Context, token string) (*GatewayAuthContext, error)) *BearerTokenAuthProvider {
	return security.NewBearerTokenAuthProvider(name, validate)
}

// ForwardAuthProvider is a backward-compatible alias for security.ForwardAuthProvider.
type ForwardAuthProvider = security.ForwardAuthProvider

// NewForwardAuthProvider creates a forward auth provider.
// Deprecated: Use security.NewForwardAuthProvider directly.
func NewForwardAuthProvider(name, endpoint string, headers []string) *ForwardAuthProvider {
	return security.NewForwardAuthProvider(name, endpoint, headers)
}

// --- JWT Auth ---

// JWTAuthConfig is a backward-compatible alias for security.JWTAuthConfig.
type JWTAuthConfig = security.JWTAuthConfig

// JWTAuthProvider is a backward-compatible alias for security.JWTAuthProvider.
type JWTAuthProvider = security.JWTAuthProvider

// NewJWTAuthProvider creates a JWT auth provider.
// Deprecated: Use security.NewJWTAuthProvider directly.
func NewJWTAuthProvider(config JWTAuthConfig) *JWTAuthProvider {
	return security.NewJWTAuthProvider(config)
}

// --- Admin Auth ---

// AdminAuthConfig is a backward-compatible alias for security.AdminAuthConfig.
type AdminAuthConfig = security.AdminAuthConfig

// AdminAuthMiddleware is a backward-compatible alias for security.AdminAuthMiddleware.
type AdminAuthMiddleware = security.AdminAuthMiddleware

// NewAdminAuthMiddleware creates a new admin auth middleware.
// Deprecated: Use security.NewAdminAuthMiddleware directly.
func NewAdminAuthMiddleware(config AdminAuthConfig) *AdminAuthMiddleware {
	return security.NewAdminAuthMiddleware(config)
}

// --- WAF ---

// WAFConfig is a backward-compatible alias for security.WAFConfig.
type WAFConfig = security.WAFConfig

// WAFRule is a backward-compatible alias for security.WAFRule.
type WAFRule = security.WAFRule

// WAFViolation is a backward-compatible alias for security.WAFViolation.
type WAFViolation = security.WAFViolation

// WAF is a backward-compatible alias for security.WAF.
type WAF = security.WAF

// NewWAF creates a WAF with the given configuration.
// Deprecated: Use security.NewWAF directly.
func NewWAF(cfg WAFConfig) *WAF {
	return security.NewWAF(cfg)
}

// WAFPlugin wraps security.WAFPlugin as a root-level GatewayPlugin.
type WAFPlugin struct {
	BasePlugin
	inner *security.WAFPlugin
}

// NewWAFPlugin creates a WAF plugin that implements GatewayPlugin.
// Deprecated: Use security.NewWAFPlugin directly.
func NewWAFPlugin(cfg WAFConfig) *WAFPlugin {
	return &WAFPlugin{
		BasePlugin: BasePlugin{PluginName: "waf"},
		inner:      security.NewWAFPlugin(cfg),
	}
}

// OnRequest checks the request against WAF rules.
func (p *WAFPlugin) OnRequest(r *http.Request, _ *Route) error {
	return p.inner.CheckRequest(r)
}

// --- TLS ---

// TLSManager is a backward-compatible alias for security.TLSManager.
type TLSManager = security.TLSManager

// NewTLSManager creates a new TLS manager.
// Deprecated: Use security.NewTLSManager directly.
func NewTLSManager(config TLSConfig, logger forge.Logger) *TLSManager {
	return security.NewTLSManager(config, logger)
}

// ValidateTLSConfig checks that the TLS configuration is valid.
// Deprecated: Use security.ValidateTLSConfig directly.
func ValidateTLSConfig(config TLSConfig) error {
	return security.ValidateTLSConfig(config)
}

// --- Compile-time interface checks ---
var _ GatewayPlugin = (*WAFPlugin)(nil)
var _ security.Route = (*Route)(nil)
var _ security.Target = (*Target)(nil)
