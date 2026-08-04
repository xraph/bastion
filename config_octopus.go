package bastion

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xraph/bastion/middleware"
	"github.com/xraph/bastion/security"
)

// OctopusConfig mirrors the octopus gateway YAML configuration format.
// Use FromOctopusConfig to convert to bastion's native Config type.
type OctopusConfig struct {
	Gateway       OctopusGatewayConfig                 `yaml:"gateway" json:"gateway"`
	Upstreams     []OctopusUpstreamConfig              `yaml:"upstreams" json:"upstreams"`
	Routes        []OctopusRouteConfig                 `yaml:"routes" json:"routes"`
	Plugins       []OctopusPluginConfig                `yaml:"plugins" json:"plugins"`
	FARP          OctopusFARPConfig                    `yaml:"farp" json:"farp"`
	Observability OctopusObservabilityConfig           `yaml:"observability" json:"observability"`
	AuthProviders map[string]OctopusAuthProviderConfig `yaml:"auth_providers" json:"auth_providers"`
	Auth          OctopusAuthConfig                    `yaml:"auth" json:"auth"`
	CORS          *OctopusCORSConfig                   `yaml:"cors" json:"cors"`
	Admin         OctopusAdminConfig                   `yaml:"admin" json:"admin"`
	GRPC          OctopusGRPCConfig                    `yaml:"grpc" json:"grpc"`
}

// OctopusGatewayConfig mirrors octopus gateway.* settings.
type OctopusGatewayConfig struct {
	Listen              string                    `yaml:"listen" json:"listen"`
	Workers             int                       `yaml:"workers" json:"workers"`
	RequestTimeout      string                    `yaml:"request_timeout" json:"request_timeout"`
	ShutdownTimeout     string                    `yaml:"shutdown_timeout" json:"shutdown_timeout"`
	MaxBodySize         int                       `yaml:"max_body_size" json:"max_body_size"`
	TLS                 *OctopusTLSConfig         `yaml:"tls" json:"tls"`
	Compression         *OctopusCompressionConfig `yaml:"compression" json:"compression"`
	InternalRoutePrefix *string                   `yaml:"internal_route_prefix" json:"internal_route_prefix"`
}

// OctopusTLSConfig mirrors octopus TLS settings.
type OctopusTLSConfig struct {
	CertFile           string `yaml:"cert_file" json:"cert_file"`
	KeyFile            string `yaml:"key_file" json:"key_file"`
	ClientCAFile       string `yaml:"client_ca_file" json:"client_ca_file"`
	RequireClientCert  bool   `yaml:"require_client_cert" json:"require_client_cert"`
	MinTLSVersion      string `yaml:"min_tls_version" json:"min_tls_version"`
	EnableCertReload   bool   `yaml:"enable_cert_reload" json:"enable_cert_reload"`
	ReloadIntervalSecs int    `yaml:"reload_interval_secs" json:"reload_interval_secs"`
}

// OctopusCompressionConfig mirrors octopus compression settings.
type OctopusCompressionConfig struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	Level      int      `yaml:"level" json:"level"`
	MinSize    int      `yaml:"min_size" json:"min_size"`
	Algorithms []string `yaml:"algorithms" json:"algorithms"`
}

// OctopusUpstreamConfig mirrors octopus upstream definitions.
type OctopusUpstreamConfig struct {
	Name           string                    `yaml:"name" json:"name"`
	Instances      []OctopusInstanceConfig   `yaml:"instances" json:"instances"`
	LBPolicy       string                    `yaml:"lb_policy" json:"lb_policy"`
	HealthCheck    *OctopusHealthCheckConfig `yaml:"health_check" json:"health_check"`
	CircuitBreaker *OctopusCBConfig          `yaml:"circuit_breaker" json:"circuit_breaker"`
}

// OctopusInstanceConfig mirrors octopus instance definitions.
type OctopusInstanceConfig struct {
	ID       string            `yaml:"id" json:"id"`
	Host     string            `yaml:"host" json:"host"`
	Port     int               `yaml:"port" json:"port"`
	Weight   int               `yaml:"weight" json:"weight"`
	Metadata map[string]string `yaml:"metadata" json:"metadata"`
}

// OctopusRouteConfig mirrors octopus route definitions.
type OctopusRouteConfig struct {
	Path          string                       `yaml:"path" json:"path"`
	Methods       []string                     `yaml:"methods" json:"methods"`
	Upstream      string                       `yaml:"upstream" json:"upstream"`
	Priority      int                          `yaml:"priority" json:"priority"`
	StripPrefix   *string                      `yaml:"strip_prefix" json:"strip_prefix"`
	AddPrefix     *string                      `yaml:"add_prefix" json:"add_prefix"`
	Metadata      map[string]string            `yaml:"metadata" json:"metadata"`
	AuthProvider  *string                      `yaml:"auth_provider" json:"auth_provider"`
	SkipAuth      bool                         `yaml:"skip_auth" json:"skip_auth"`
	RequireRoles  []string                     `yaml:"require_roles" json:"require_roles"`
	RequireScopes []string                     `yaml:"require_scopes" json:"require_scopes"`
	AuthzRule     *string                      `yaml:"authz_rule" json:"authz_rule"`
	Timeout       *string                      `yaml:"timeout" json:"timeout"`
	RateLimit     *OctopusRouteRateLimitConfig `yaml:"rate_limit" json:"rate_limit"`
	CORS          *OctopusRouteCORSConfig      `yaml:"cors" json:"cors"`
}

// OctopusRouteRateLimitConfig mirrors octopus per-route rate limiting.
type OctopusRouteRateLimitConfig struct {
	RequestsPerWindow int    `yaml:"requests_per_window" json:"requests_per_window"`
	WindowSize        string `yaml:"window_size" json:"window_size"`
}

// OctopusRouteCORSConfig mirrors octopus per-route CORS override.
type OctopusRouteCORSConfig struct {
	AllowedOrigins   []string `yaml:"allowed_origins" json:"allowed_origins"`
	AllowedMethods   []string `yaml:"allowed_methods" json:"allowed_methods"`
	AllowedHeaders   []string `yaml:"allowed_headers" json:"allowed_headers"`
	AllowCredentials bool     `yaml:"allow_credentials" json:"allow_credentials"`
	MaxAge           int      `yaml:"max_age" json:"max_age"`
}

// OctopusHealthCheckConfig mirrors octopus health check settings.
type OctopusHealthCheckConfig struct {
	Type               string `yaml:"type" json:"type"`
	Path               string `yaml:"path" json:"path"`
	Interval           string `yaml:"interval" json:"interval"`
	Timeout            string `yaml:"timeout" json:"timeout"`
	HealthyThreshold   int    `yaml:"healthy_threshold" json:"healthy_threshold"`
	UnhealthyThreshold int    `yaml:"unhealthy_threshold" json:"unhealthy_threshold"`
}

// OctopusCBConfig mirrors octopus circuit breaker settings.
type OctopusCBConfig struct {
	ErrorThreshold float32 `yaml:"error_threshold" json:"error_threshold"`
	MinRequests    int     `yaml:"min_requests" json:"min_requests"`
	Timeout        string  `yaml:"timeout" json:"timeout"`
}

// OctopusPluginConfig mirrors octopus plugin definitions.
type OctopusPluginConfig struct {
	Name       string         `yaml:"name" json:"name"`
	PluginType string         `yaml:"plugin_type" json:"plugin_type"`
	Enabled    bool           `yaml:"enabled" json:"enabled"`
	Priority   int            `yaml:"priority" json:"priority"`
	Config     map[string]any `yaml:"config" json:"config"`
}

// OctopusFARPConfig mirrors octopus FARP settings.
type OctopusFARPConfig struct {
	Enabled        bool                        `yaml:"enabled" json:"enabled"`
	WatchInterval  string                      `yaml:"watch_interval" json:"watch_interval"`
	SchemaCacheTTL string                      `yaml:"schema_cache_ttl" json:"schema_cache_ttl"`
	Discovery      *OctopusFARPDiscoveryConfig `yaml:"discovery" json:"discovery"`
}

// OctopusFARPDiscoveryConfig mirrors octopus FARP discovery backend settings.
type OctopusFARPDiscoveryConfig struct {
	Backends []OctopusDiscoveryBackend `yaml:"backends" json:"backends"`
}

// OctopusDiscoveryBackend mirrors an octopus discovery backend entry.
type OctopusDiscoveryBackend struct {
	Type    string         `yaml:"type" json:"type"`
	Enabled bool           `yaml:"enabled" json:"enabled"`
	Config  map[string]any `yaml:"config" json:"config"`
}

// OctopusObservabilityConfig mirrors octopus observability settings.
type OctopusObservabilityConfig struct {
	Logging OctopusLoggingConfig `yaml:"logging" json:"logging"`
	Metrics OctopusMetricsConfig `yaml:"metrics" json:"metrics"`
	Tracing OctopusTracingConfig `yaml:"tracing" json:"tracing"`
}

// OctopusLoggingConfig mirrors octopus logging settings.
type OctopusLoggingConfig struct {
	Level  string `yaml:"level" json:"level"`
	Format string `yaml:"format" json:"format"`
}

// OctopusMetricsConfig mirrors octopus metrics settings.
type OctopusMetricsConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Endpoint string `yaml:"endpoint" json:"endpoint"`
}

// OctopusTracingConfig mirrors octopus tracing settings.
type OctopusTracingConfig struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	JaegerEndpoint string `yaml:"jaeger_endpoint" json:"jaeger_endpoint"`
}

// OctopusAuthProviderConfig mirrors octopus auth provider (tagged enum).
type OctopusAuthProviderConfig struct {
	Type string `yaml:"type" json:"type"`

	// JWT fields
	Secret        string `yaml:"secret,omitempty" json:"secret,omitempty"`
	PublicKey     string `yaml:"public_key,omitempty" json:"public_key,omitempty"`
	PublicKeyFile string `yaml:"public_key_file,omitempty" json:"public_key_file,omitempty"`
	Algorithm     string `yaml:"algorithm,omitempty" json:"algorithm,omitempty"`
	Issuer        string `yaml:"issuer,omitempty" json:"issuer,omitempty"`
	Audience      string `yaml:"audience,omitempty" json:"audience,omitempty"`
	HeaderName    string `yaml:"header_name,omitempty" json:"header_name,omitempty"`
	TokenPrefix   string `yaml:"token_prefix,omitempty" json:"token_prefix,omitempty"`

	// OIDC fields
	IssuerURL           string   `yaml:"issuer_url,omitempty" json:"issuer_url,omitempty"`
	JWKSRefreshInterval string   `yaml:"jwks_refresh_interval,omitempty" json:"jwks_refresh_interval,omitempty"`
	RequiredScopes      []string `yaml:"required_scopes,omitempty" json:"required_scopes,omitempty"`
	FallbackProvider    string   `yaml:"fallback_provider,omitempty" json:"fallback_provider,omitempty"`

	// API Key fields
	QueryParam        string               `yaml:"query_param,omitempty" json:"query_param,omitempty"`
	Keys              []OctopusAPIKeyEntry `yaml:"keys,omitempty" json:"keys,omitempty"`
	ExternalValidator string               `yaml:"external_validator,omitempty" json:"external_validator,omitempty"`

	// Forward Auth fields
	Endpoint        string   `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	ForwardHeaders  []string `yaml:"forward_headers,omitempty" json:"forward_headers,omitempty"`
	ResponseHeaders []string `yaml:"response_headers,omitempty" json:"response_headers,omitempty"`
	CacheTTL        string   `yaml:"cache_ttl,omitempty" json:"cache_ttl,omitempty"`

	// mTLS fields
	ClientCAFile         string              `yaml:"client_ca_file,omitempty" json:"client_ca_file,omitempty"`
	RequireClientCert    bool                `yaml:"require_client_cert,omitempty" json:"require_client_cert,omitempty"`
	ExtractCNAsPrincipal bool                `yaml:"extract_cn_as_principal,omitempty" json:"extract_cn_as_principal,omitempty"`
	CNToRoles            map[string][]string `yaml:"cn_to_roles,omitempty" json:"cn_to_roles,omitempty"`

	// Timeout (shared by forward_auth)
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// OctopusAPIKeyEntry mirrors octopus API key entries.
type OctopusAPIKeyEntry struct {
	Key       string   `yaml:"key" json:"key"`
	Name      string   `yaml:"name" json:"name"`
	Scopes    []string `yaml:"scopes" json:"scopes"`
	RateLimit *int     `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
}

// OctopusAuthConfig mirrors octopus global auth settings.
type OctopusAuthConfig struct {
	DefaultProvider string   `yaml:"default_provider" json:"default_provider"`
	GlobalEnforce   bool     `yaml:"global_enforce" json:"global_enforce"`
	SkipPaths       []string `yaml:"skip_paths" json:"skip_paths"`
	PrincipalHeader string   `yaml:"principal_header" json:"principal_header"`
	RolesHeader     string   `yaml:"roles_header" json:"roles_header"`
	ScopesHeader    string   `yaml:"scopes_header" json:"scopes_header"`
	TokenCacheTTL   string   `yaml:"token_cache_ttl" json:"token_cache_ttl"`
	ErrorFormat     string   `yaml:"error_format" json:"error_format"`
}

// OctopusCORSConfig mirrors octopus global CORS settings.
type OctopusCORSConfig struct {
	AllowedOrigins   []string `yaml:"allowed_origins" json:"allowed_origins"`
	AllowedMethods   []string `yaml:"allowed_methods" json:"allowed_methods"`
	AllowedHeaders   []string `yaml:"allowed_headers" json:"allowed_headers"`
	ExposedHeaders   []string `yaml:"exposed_headers" json:"exposed_headers"`
	MaxAge           int      `yaml:"max_age" json:"max_age"`
	AllowCredentials bool     `yaml:"allow_credentials" json:"allow_credentials"`
}

// OctopusAdminConfig mirrors octopus admin settings.
type OctopusAdminConfig struct {
	AuthProvider string   `yaml:"auth_provider" json:"auth_provider"`
	AllowedIPs   []string `yaml:"allowed_ips" json:"allowed_ips"`
}

// OctopusGRPCConfig mirrors octopus gRPC settings.
type OctopusGRPCConfig struct {
	Enabled             bool              `yaml:"enabled" json:"enabled"`
	MaxMessageSize      int               `yaml:"max_message_size" json:"max_message_size"`
	EnableReflection    bool              `yaml:"enable_reflection" json:"enable_reflection"`
	EnableGRPCWeb       bool              `yaml:"enable_grpc_web" json:"enable_grpc_web"`
	DeadlinePropagation bool              `yaml:"deadline_propagation" json:"deadline_propagation"`
	Services            map[string]string `yaml:"services" json:"services"`
}

// LoadOctopusConfig reads and parses an octopus-format YAML config file.
func LoadOctopusConfig(path string) (OctopusConfig, error) {
	// #nosec G304 -- path is the operator-supplied config file location.
	data, err := os.ReadFile(path)
	if err != nil {
		return OctopusConfig{}, fmt.Errorf("read octopus config: %w", err)
	}

	var oc OctopusConfig
	if err := yaml.Unmarshal(data, &oc); err != nil {
		return OctopusConfig{}, fmt.Errorf("parse octopus config: %w", err)
	}

	return oc, nil
}

// FromOctopusConfig converts an octopus-format config to bastion's native Config.
func FromOctopusConfig(oc OctopusConfig) Config {
	cfg := DefaultConfig()

	// --- Gateway ---
	if oc.Gateway.RequestTimeout != "" {
		if d, err := time.ParseDuration(oc.Gateway.RequestTimeout); err == nil {
			cfg.Timeouts.Read = d
			cfg.Timeouts.Write = d
		}
	}
	if oc.Gateway.MaxBodySize > 0 {
		cfg.BufferPool.MaxRequestBodySize = oc.Gateway.MaxBodySize
	}
	if oc.Gateway.TLS != nil {
		cfg.TLS = security.TLSConfig{
			Enabled:        true,
			CACertFile:     oc.Gateway.TLS.ClientCAFile,
			ClientCertFile: oc.Gateway.TLS.CertFile,
			ClientKeyFile:  oc.Gateway.TLS.KeyFile,
			MinVersion:     oc.Gateway.TLS.MinTLSVersion,
		}
	}

	// --- Upstreams index ---
	upstreamMap := make(map[string]*OctopusUpstreamConfig, len(oc.Upstreams))
	for i := range oc.Upstreams {
		upstreamMap[oc.Upstreams[i].Name] = &oc.Upstreams[i]
	}

	// --- Routes (with upstream resolution) ---
	for _, r := range oc.Routes {
		rc := RouteConfig{
			Path:     r.Path,
			Methods:  r.Methods,
			Priority: r.Priority,
			Enabled:  true,
		}

		if r.StripPrefix != nil {
			rc.StripPrefix = true
		}
		if r.AddPrefix != nil {
			rc.AddPrefix = *r.AddPrefix
		}

		// Resolve upstream to targets.
		if upstream, ok := upstreamMap[r.Upstream]; ok {
			rc.ServiceName = upstream.Name
			for _, inst := range upstream.Instances {
				w := inst.Weight
				if w <= 0 {
					w = 1
				}
				rc.Targets = append(rc.Targets, TargetConfig{
					URL:      fmt.Sprintf("http://%s:%d", inst.Host, inst.Port),
					Weight:   w,
					Metadata: inst.Metadata,
				})
			}

			// Apply upstream-level health check.
			if upstream.HealthCheck != nil {
				hc := upstream.HealthCheck
				if hc.Path != "" {
					cfg.HealthCheck.Path = hc.Path
				}
				if hc.Interval != "" {
					if d, err := time.ParseDuration(hc.Interval); err == nil {
						cfg.HealthCheck.Interval = d
					}
				}
				if hc.Timeout != "" {
					if d, err := time.ParseDuration(hc.Timeout); err == nil {
						cfg.HealthCheck.Timeout = d
					}
				}
				if hc.HealthyThreshold > 0 {
					cfg.HealthCheck.SuccessThreshold = hc.HealthyThreshold
				}
				if hc.UnhealthyThreshold > 0 {
					cfg.HealthCheck.FailureThreshold = hc.UnhealthyThreshold
				}
			}

			// Apply upstream-level circuit breaker.
			if upstream.CircuitBreaker != nil {
				cb := upstream.CircuitBreaker
				cfg.CircuitBreaker.Enabled = true
				cfg.CircuitBreaker.FailureThreshold = int(cb.ErrorThreshold * 100)
				if cb.Timeout != "" {
					if d, err := time.ParseDuration(cb.Timeout); err == nil {
						cfg.CircuitBreaker.ResetTimeout = d
					}
				}
			}
		}

		// Per-route timeout.
		if r.Timeout != nil {
			if d, err := time.ParseDuration(*r.Timeout); err == nil {
				rc.Timeout = &TimeoutConfig{Read: d, Write: d}
			}
		}

		// Per-route rate limit.
		if r.RateLimit != nil {
			rps := float64(r.RateLimit.RequestsPerWindow)
			if r.RateLimit.WindowSize != "" {
				if windowDur, err := time.ParseDuration(r.RateLimit.WindowSize); err == nil && windowDur > 0 {
					rps = float64(r.RateLimit.RequestsPerWindow) / windowDur.Seconds()
				}
			}
			rc.RateLimit = &middleware.RateLimitConfig{
				Enabled:        true,
				RequestsPerSec: rps,
				Burst:          r.RateLimit.RequestsPerWindow,
			}
		}

		// Per-route auth.
		if r.SkipAuth {
			rc.Auth = &security.RouteAuthConfig{SkipAuth: true}
		} else if len(r.RequireScopes) > 0 || r.AuthProvider != nil {
			rc.Auth = &security.RouteAuthConfig{
				Enabled: true,
				Scopes:  r.RequireScopes,
			}
			if r.AuthProvider != nil {
				rc.Auth.Providers = []string{*r.AuthProvider}
			}
		}

		// Metadata.
		if len(r.Metadata) > 0 {
			meta := make(map[string]any, len(r.Metadata))
			for k, v := range r.Metadata {
				meta[k] = v
			}
			rc.Metadata = meta
		}

		cfg.Routes = append(cfg.Routes, rc)
	}

	// --- Load balancing from first upstream's policy as global default ---
	if len(oc.Upstreams) > 0 {
		switch oc.Upstreams[0].LBPolicy {
		case "round_robin", "":
			cfg.LoadBalancing.Strategy = LBRoundRobin
		case "least_connections":
			cfg.LoadBalancing.Strategy = LBLeastConnections
		case "random":
			cfg.LoadBalancing.Strategy = LBRandom
		}
	}

	// --- FARP ---
	cfg.Discovery.Enabled = oc.FARP.Enabled
	if oc.FARP.WatchInterval != "" {
		if d, err := time.ParseDuration(oc.FARP.WatchInterval); err == nil {
			cfg.Discovery.PollInterval = d
		}
	}

	// --- CORS ---
	if oc.CORS != nil {
		cfg.CORS = CORSConfig{
			Enabled:       true,
			AllowOrigins:  oc.CORS.AllowedOrigins,
			AllowMethods:  oc.CORS.AllowedMethods,
			AllowHeaders:  oc.CORS.AllowedHeaders,
			ExposeHeaders: oc.CORS.ExposedHeaders,
			AllowCreds:    oc.CORS.AllowCredentials,
			MaxAge:        oc.CORS.MaxAge,
		}
	}

	// --- Auth ---
	if oc.Auth.GlobalEnforce {
		cfg.Auth = security.AuthConfig{
			Enabled:        true,
			ForwardHeaders: true,
		}
	}

	// --- Observability ---
	cfg.Metrics.Enabled = oc.Observability.Metrics.Enabled
	cfg.Tracing.Enabled = oc.Observability.Tracing.Enabled

	// --- Dashboard ---
	cfg.Dashboard.Enabled = true

	return cfg
}

// WithOctopusConfig applies an octopus-format config to the gateway.
func WithOctopusConfig(oc OctopusConfig) ConfigOption {
	return func(c *Config) {
		converted := FromOctopusConfig(oc)
		*c = converted
	}
}

// WithOctopusConfigFile loads and applies an octopus-format YAML config file.
func WithOctopusConfigFile(path string) ConfigOption {
	return func(c *Config) {
		oc, err := LoadOctopusConfig(path)
		if err != nil {
			return
		}
		converted := FromOctopusConfig(oc)
		*c = converted
	}
}
