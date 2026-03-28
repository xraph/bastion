package security

import (
	"context"
	"net/http"
	"time"
)

// --- Auth Types ---

// AuthConfig holds gateway-level auth settings.
type AuthConfig struct {
	Enabled        bool     `json:"enabled" yaml:"enabled"`
	DefaultPolicy  string   `json:"defaultPolicy,omitempty" yaml:"default_policy"`
	Providers      []string `json:"providers,omitempty" yaml:"providers"`
	ForwardHeaders bool     `json:"forwardHeaders" yaml:"forward_headers"`
}

// AuthProvider is the gateway's interface for authentication providers.
// This decouples the gateway from the concrete discovery.Service type.
type AuthProvider interface {
	// Name returns the provider name.
	Name() string

	// Authenticate validates a request and returns an auth context or error.
	Authenticate(ctx context.Context, r *http.Request) (*AuthContext, error)
}

// AuthContext holds authenticated subject information for gateway requests.
type AuthContext struct {
	// Subject is the authenticated entity (user ID, service ID, etc.)
	Subject string `json:"subject"`

	// Claims holds additional authentication claims (roles, permissions, etc.)
	Claims map[string]any `json:"claims,omitempty"`

	// Scopes holds OAuth2 scopes or permission strings
	Scopes []string `json:"scopes,omitempty"`

	// ProviderName identifies which auth provider authenticated this request
	ProviderName string `json:"providerName"`
}

// HasScope checks if the auth context has a specific scope.
func (a *AuthContext) HasScope(scope string) bool {
	for _, s := range a.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// HasScopes checks if the auth context has all specified scopes.
func (a *AuthContext) HasScopes(scopes ...string) bool {
	for _, scope := range scopes {
		if !a.HasScope(scope) {
			return false
		}
	}
	return true
}

// AuthRegistry manages authentication providers for the gateway.
type AuthRegistry interface {
	// Get returns a provider by name.
	Get(name string) (AuthProvider, bool)

	// Has checks if a provider exists.
	Has(name string) bool

	// List returns all registered provider names.
	List() []string

	// Register adds a custom auth provider.
	Register(provider AuthProvider) error
}

// AuthError represents an authentication failure.
type AuthError struct {
	Code    int
	Message string
}

func (e *AuthError) Error() string {
	return e.Message
}

// RouteAuthConfig defines per-route auth requirements.
type RouteAuthConfig struct {
	Enabled     bool     `json:"enabled"`
	Providers   []string `json:"providers,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	SkipAuth    bool     `json:"skipAuth,omitempty"`
	ForwardAuth bool     `json:"forwardAuth,omitempty"`
}

// Route is a minimal interface for gateway routes used by the security package.
type Route interface {
	GetID() string
	GetAuth() *RouteAuthConfig
}

// --- TLS Types ---

// TLSConfig holds upstream TLS settings.
type TLSConfig struct {
	Enabled            bool          `json:"enabled" yaml:"enabled"`
	CACertFile         string        `json:"caCertFile,omitempty" yaml:"ca_cert_file"`
	ClientCertFile     string        `json:"clientCertFile,omitempty" yaml:"client_cert_file"`
	ClientKeyFile      string        `json:"clientKeyFile,omitempty" yaml:"client_key_file"`
	InsecureSkipVerify bool          `json:"insecureSkipVerify,omitempty" yaml:"insecure_skip_verify"`
	MinVersion         string        `json:"minVersion,omitempty" yaml:"min_version"`
	CipherSuites       []string      `json:"cipherSuites,omitempty" yaml:"cipher_suites"`
	ReloadInterval     time.Duration `json:"reloadInterval,omitempty" yaml:"reload_interval"`
}

// TargetTLSConfig holds per-target TLS configuration.
type TargetTLSConfig struct {
	Enabled            bool   `json:"enabled"`
	CACertFile         string `json:"caCertFile,omitempty"`
	ClientCertFile     string `json:"clientCertFile,omitempty"`
	ClientKeyFile      string `json:"clientKeyFile,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	ServerName         string `json:"serverName,omitempty"`
}

// Target is a minimal interface for upstream targets used by the TLS manager.
type Target interface {
	GetID() string
	GetTLS() *TargetTLSConfig
}

// --- Plugin Types ---

// BasePlugin provides no-op defaults for gateway plugins,
// allowing plugins to override only the methods they care about.
type BasePlugin struct {
	PluginName string
}

func (bp *BasePlugin) Name() string { return bp.PluginName }
