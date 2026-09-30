package contract

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
)

type serviceView struct {
	Name         string     `json:"name"`
	Version      string     `json:"version"`
	Address      string     `json:"address"`
	Port         int        `json:"port"`
	Protocols    []string   `json:"protocols"`
	Healthy      bool       `json:"healthy"`
	RouteCount   int        `json:"routeCount"`
	DiscoveredAt *time.Time `json:"discoveredAt"`
	MetadataKeys []string   `json:"metadataKeys"`
}

type servicesListRequest struct{}
type servicesListResponse struct {
	DiscoveryEnabled bool          `json:"discoveryEnabled"`
	Services         []serviceView `json:"services"` // by name
	Total            int           `json:"total"`
}

type specView struct {
	ServiceName string     `json:"serviceName"`
	Version     string     `json:"version"`
	SpecURL     string     `json:"specUrl"`
	Healthy     bool       `json:"healthy"`
	PathCount   int        `json:"pathCount"`
	Error       string     `json:"error,omitempty"`
	FetchedAt   *time.Time `json:"fetchedAt"`
}

type openapiSummaryRequest struct{}
type openapiSummaryResponse struct {
	Enabled     bool       `json:"enabled"` // configured
	Running     bool       `json:"running"` // aggregator started
	SpecPath    string     `json:"specPath"`
	LastRefresh *time.Time `json:"lastRefresh"`
	TotalPaths  int        `json:"totalPaths"`
	Services    []specView `json:"services"`
	Total       int        `json:"total"`
}

type configSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type configSection struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Enabled  *bool           `json:"enabled"` // null: the section has no switch
	Note     string          `json:"note,omitempty"`
	Settings []configSetting `json:"settings"`
}

type configDetailRequest struct{}
type configDetailResponse struct {
	Sections []configSection `json:"sections"`
}

// toServiceView copies field by field: discovery mutates these pointers later.
func toServiceView(s *bastion.DiscoveredService) serviceView {
	protocols := slices.Clone(s.Protocols)
	if protocols == nil {
		protocols = []string{}
	}

	return serviceView{
		Name: s.Name, Version: s.Version, Address: s.Address, Port: s.Port,
		Protocols: protocols, Healthy: s.Healthy, RouteCount: s.RouteCount,
		DiscoveredAt: nonZeroTime(s.DiscoveredAt), MetadataKeys: sortedKeys(s.Metadata),
	}
}

func toSpecView(s *bastion.ServiceOpenAPISpec) specView {
	return specView{
		ServiceName: s.ServiceName, Version: s.Version, SpecURL: admin.RedactURL(s.SpecURL), Healthy: s.Healthy,
		PathCount: s.PathCount, Error: s.Error, FetchedAt: nonZeroTime(s.FetchedAt),
	}
}

func servicesListHandler(deps Deps) func(context.Context, servicesListRequest, contract.Principal) (servicesListResponse, error) {
	return func(context.Context, servicesListRequest, contract.Principal) (servicesListResponse, error) {
		out := servicesListResponse{
			DiscoveryEnabled: deps.Gateway.Config().Discovery.Enabled,
			Services:         []serviceView{},
		}

		if disc := deps.Gateway.Discovery(); disc != nil {
			for _, s := range disc.DiscoveredServices() {
				out.Services = append(out.Services, toServiceView(s))
			}
		}

		slices.SortFunc(out.Services, func(a, b serviceView) int { return strings.Compare(a.Name, b.Name) })
		out.Total = len(out.Services)

		return out, nil
	}
}

func openapiSummaryHandler(deps Deps) func(context.Context, openapiSummaryRequest, contract.Principal) (openapiSummaryResponse, error) {
	return func(context.Context, openapiSummaryRequest, contract.Principal) (openapiSummaryResponse, error) {
		cfg := deps.Gateway.Config()
		out := openapiSummaryResponse{Enabled: cfg.OpenAPI.Enabled, Services: []specView{}}

		oa := deps.Gateway.OpenAPI()
		if oa == nil {
			return out, nil
		}

		out.Running = true
		out.SpecPath = cfg.Dashboard.BasePath + oa.SpecPath()
		out.LastRefresh = nonZeroTime(oa.LastRefresh())

		if paths, ok := oa.MergedSpecMap()["paths"].(map[string]any); ok {
			out.TotalPaths = len(paths)
		}

		for _, s := range oa.ServiceSpecs() {
			out.Services = append(out.Services, toSpecView(s))
		}

		slices.SortFunc(out.Services, func(a, b specView) int { return strings.Compare(a.ServiceName, b.ServiceName) })
		out.Total = len(out.Services)

		return out, nil
	}
}

func configDetailHandler(deps Deps) func(context.Context, configDetailRequest, contract.Principal) (configDetailResponse, error) {
	return func(context.Context, configDetailRequest, contract.Principal) (configDetailResponse, error) {
		return configDetailResponse{Sections: configSections(deps.Gateway.Config())}, nil
	}
}

func boolPtr(b bool) *bool { return &b }

func setting(k string, v any) configSetting { return configSetting{Key: k, Value: fmt.Sprint(v)} }

// isSet reports a file path's presence without the path: a key's location is
// a map for anyone who reads the page.
func isSet(path string) string {
	if path == "" {
		return "not set"
	}

	return "set"
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return fmt.Sprintf("%d %ses", n, noun)
}

func list(v []string) string {
	if len(v) == 0 {
		return "none"
	}

	return strings.Join(v, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

// configSections renders the gateway config for reading. Nothing that
// locates a secret leaves: TLS paths show as set or not set, IP lists as
// counts.
func configSections(cfg bastion.Config) []configSection {
	hc := cfg.HealthCheck

	return []configSection{
		{ID: "gateway", Title: "Gateway", Enabled: boolPtr(cfg.Enabled), Settings: []configSetting{
			setting("Base path", cfg.BasePath), setting("Routes in config", len(cfg.Routes)),
		}},
		{ID: "timeouts", Title: "Timeouts", Settings: []configSetting{
			setting("Connect", cfg.Timeouts.Connect), setting("Read", cfg.Timeouts.Read),
			setting("Write", cfg.Timeouts.Write), setting("Idle", cfg.Timeouts.Idle),
		}},
		{ID: "loadBalancing", Title: "Load balancing", Settings: []configSetting{
			setting("Strategy", cfg.LoadBalancing.Strategy), setting("Consistent hash key", cfg.LoadBalancing.ConsistentKey),
		}},
		{ID: "circuitBreaker", Title: "Circuit breaker", Enabled: boolPtr(cfg.CircuitBreaker.Enabled), Settings: []configSetting{
			setting("Failure threshold", cfg.CircuitBreaker.FailureThreshold), setting("Failure window", cfg.CircuitBreaker.FailureWindow),
			setting("Reset timeout", cfg.CircuitBreaker.ResetTimeout), setting("Half-open probes", cfg.CircuitBreaker.HalfOpenMax),
		}},
		{ID: "retry", Title: "Retry", Enabled: boolPtr(cfg.Retry.Enabled),
			Note: "Nothing in the proxy calls the retry policy, so no request is retried whatever this says.",
			Settings: []configSetting{
				setting("Max attempts", cfg.Retry.MaxAttempts), setting("Backoff", cfg.Retry.Backoff),
				setting("Initial delay", cfg.Retry.InitialDelay), setting("Max delay", cfg.Retry.MaxDelay),
			}},
		{ID: "rateLimiting", Title: "Rate limiting", Enabled: boolPtr(cfg.RateLimiting.Enabled), Settings: []configSetting{
			setting("Requests per second", cfg.RateLimiting.RequestsPerSec), setting("Burst", cfg.RateLimiting.Burst),
			setting("Per client", cfg.RateLimiting.PerClient),
		}},
		{ID: "healthCheck", Title: "Health checks", Enabled: boolPtr(hc.Enabled), Settings: []configSetting{
			setting("Interval", hc.Interval), setting("Timeout", hc.Timeout), setting("Path", hc.Path),
			setting("Failure threshold", hc.FailureThreshold), setting("Success threshold", hc.SuccessThreshold),
			setting("Passive checks", hc.EnablePassive),
		}},
		{ID: "caching", Title: "Response cache", Enabled: boolPtr(cfg.Caching.Enabled),
			Note: "Nothing writes to the cache, so every lookup misses whatever this says.",
			Settings: []configSetting{
				setting("Default TTL", cfg.Caching.DefaultTTL), setting("Max size", cfg.Caching.MaxSize),
				setting("Methods", list(cfg.Caching.Methods)),
			}},
		{ID: "auth", Title: "Authentication", Enabled: boolPtr(cfg.Auth.Enabled), Settings: []configSetting{
			setting("Default policy", cfg.Auth.DefaultPolicy), setting("Providers", list(cfg.Auth.Providers)),
			setting("Forward headers", cfg.Auth.ForwardHeaders),
		}},
		{ID: "tls", Title: "Upstream TLS", Enabled: boolPtr(cfg.TLS.Enabled), Settings: []configSetting{
			setting("CA certificate", isSet(cfg.TLS.CACertFile)), setting("Client certificate", isSet(cfg.TLS.ClientCertFile)),
			setting("Client key", isSet(cfg.TLS.ClientKeyFile)), setting("Skip verification", cfg.TLS.InsecureSkipVerify),
			setting("Minimum version", cfg.TLS.MinVersion),
		}},
		{ID: "ipFilter", Title: "IP filter", Enabled: boolPtr(cfg.IPFilter.Enabled), Settings: []configSetting{
			setting("Allow list", count(len(cfg.IPFilter.AllowIPs), "address")),
			setting("Deny list", count(len(cfg.IPFilter.DenyIPs), "address")),
		}},
		{ID: "cors", Title: "CORS", Enabled: boolPtr(cfg.CORS.Enabled), Settings: []configSetting{
			setting("Allowed origins", list(cfg.CORS.AllowOrigins)), setting("Credentials", cfg.CORS.AllowCreds),
			setting("Max age", cfg.CORS.MaxAge),
		}},
		{ID: "discovery", Title: "Discovery", Enabled: boolPtr(cfg.Discovery.Enabled), Settings: []configSetting{
			setting("Poll interval", cfg.Discovery.PollInterval), setting("Watch mode", cfg.Discovery.WatchMode),
			setting("Auto prefix", cfg.Discovery.AutoPrefix),
		}},
		{ID: "openapi", Title: "OpenAPI aggregation", Enabled: boolPtr(cfg.OpenAPI.Enabled), Settings: []configSetting{
			setting("Spec path", cfg.OpenAPI.Path), setting("UI path", cfg.OpenAPI.UIPath),
		}},
		{ID: "metrics", Title: "Metrics", Enabled: boolPtr(cfg.Metrics.Enabled), Settings: []configSetting{
			setting("Prefix", cfg.Metrics.Prefix),
		}},
		{ID: "accessLog", Title: "Access log", Enabled: boolPtr(cfg.AccessLog.Enabled), Settings: []configSetting{
			setting("Include body", cfg.AccessLog.IncludeBody),
		}},
	}
}
