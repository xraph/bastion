package security

import (
	"context"
	"errors"
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

	// cause is the last provider's error when every provider failed, so
	// AuthFailureClass can tell an auth service that was down from a
	// denial.
	cause error
}

func (e *AuthError) Error() string {
	return e.Message
}

// Unwrap returns the provider error behind e, if there is one.
func (e *AuthError) Unwrap() error { return e.cause }

// The classes AuthFailureClass sorts a failed authentication into.
const (
	// AuthFailureUnauthenticated is a request with no credentials, or ones
	// no provider accepted.
	AuthFailureUnauthenticated = "unauthenticated"

	// AuthFailureForbidden is a request whose credentials were accepted
	// but don't carry the route's scopes, or that a provider answered 403.
	AuthFailureForbidden = "forbidden"

	// AuthFailureUnavailable is a request that couldn't be checked: an auth
	// service that was down or answered 5xx.
	AuthFailureUnavailable = "unavailable"
)

// AuthFailureClass sorts an error from Auth.Authenticate into one of the
// AuthFailure classes. It looks through the whole chain, so a forward-auth
// service that was down counts as unavailable even though the gateway
// answers the client 401.
func AuthFailureClass(err error) string {
	forbidden := false

	for e := err; e != nil; e = errors.Unwrap(e) {
		ae, ok := e.(*AuthError)
		if !ok {
			continue
		}

		switch {
		case ae.Code >= http.StatusInternalServerError:
			return AuthFailureUnavailable
		case ae.Code == http.StatusForbidden:
			forbidden = true
		}
	}

	if forbidden {
		return AuthFailureForbidden
	}

	return AuthFailureUnauthenticated
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
