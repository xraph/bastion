package contract

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
)

type topRoute struct {
	RouteID       string `json:"routeId"`
	Path          string `json:"path"`
	TotalRequests int64  `json:"totalRequests"`
	TotalErrors   int64  `json:"totalErrors"`
}

type overviewStatsResponse struct {
	TotalRequests         int64      `json:"totalRequests"`
	TotalErrors           int64      `json:"totalErrors"`
	ErrorRate             *float64   `json:"errorRate"`    // percent; null before any request
	AvgLatencyMs          *float64   `json:"avgLatencyMs"` // null with no samples
	P99LatencyMs          *float64   `json:"p99LatencyMs"`
	LatencySamples        int        `json:"latencySamples"`
	CacheLookups          int64      `json:"cacheLookups"`
	CacheHitRate          *float64   `json:"cacheHitRate"` // percent; null with no lookups
	RateLimited           int64      `json:"rateLimited"`
	CircuitBreaks         int64      `json:"circuitBreaks"`
	TotalRoutes           int        `json:"totalRoutes"`
	EnabledRoutes         int        `json:"enabledRoutes"`
	HealthyUpstreams      int        `json:"healthyUpstreams"`
	TotalUpstreams        int        `json:"totalUpstreams"`
	OpenCircuits          int        `json:"openCircuits"`
	HalfOpenCircuits      int        `json:"halfOpenCircuits"`
	CircuitBreakerEnabled bool       `json:"circuitBreakerEnabled"`
	DiscoveryEnabled      bool       `json:"discoveryEnabled"`
	StartedAt             *time.Time `json:"startedAt"` // null before Start
	UptimeSeconds         int64      `json:"uptimeSeconds"`
	TopRoutes             []topRoute `json:"topRoutes"` // at most 5, busiest first
}

type routeTraffic struct {
	RouteID        string   `json:"routeId"`
	Path           string   `json:"path"`
	TotalRequests  int64    `json:"totalRequests"`
	TotalErrors    int64    `json:"totalErrors"`
	ErrorRate      *float64 `json:"errorRate"`
	AvgLatencyMs   *float64 `json:"avgLatencyMs"`
	P99LatencyMs   *float64 `json:"p99LatencyMs"`
	LatencySamples int      `json:"latencySamples"`
}

type trafficStatsResponse struct {
	TotalRequests   int64          `json:"totalRequests"`
	TotalErrors     int64          `json:"totalErrors"`
	RateLimited     int64          `json:"rateLimited"`
	CircuitBreaks   int64          `json:"circuitBreaks"`
	CacheHits       int64          `json:"cacheHits"`
	CacheMisses     int64          `json:"cacheMisses"`
	RetriesMeasured bool           `json:"retriesMeasured"` // false: nothing retries
	AvgLatencyMs    *float64       `json:"avgLatencyMs"`
	P99LatencyMs    *float64       `json:"p99LatencyMs"`
	LatencySamples  int            `json:"latencySamples"`
	Routes          []routeTraffic `json:"routes"` // busiest first
	Total           int            `json:"total"`
}

type circuitView struct {
	TargetID        string                `json:"targetId"`
	URL             string                `json:"url"`
	Routes          []admin.UpstreamRoute `json:"routes"`
	Tracked         bool                  `json:"tracked"` // false: never selected, no breaker yet
	State           bastion.CircuitState  `json:"state"`
	FailureCount    int                   `json:"failureCount"`
	LastFailure     *time.Time            `json:"lastFailure"`
	LastStateChange *time.Time            `json:"lastStateChange"`
}

type circuitsListResponse struct {
	Enabled             bool          `json:"enabled"`
	FailureThreshold    int           `json:"failureThreshold"`
	ResetTimeoutSeconds float64       `json:"resetTimeoutSeconds"`
	HalfOpenMax         int           `json:"halfOpenMax"`
	Circuits            []circuitView `json:"circuits"` // by target id
	Total               int           `json:"total"`
}

type overviewStatsRequest struct{}
type trafficStatsRequest struct{}
type circuitsListRequest struct{}

// percent is n/d as a percentage, or nil when d is zero: a rate over nothing
// is unknown, not 0%, and a 0 reads as "nothing wrong".
func percent(n, d int64) *float64 {
	if d == 0 {
		return nil
	}

	v := float64(n) / float64(d) * 100

	return &v
}

func ptr[T any](v T) *T { return &v }

func nonZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	return &t
}

func topRoutes(s *bastion.GatewayStats, n int) []topRoute {
	out := make([]topRoute, 0, len(s.RouteStats))
	for _, rs := range s.RouteStats {
		if rs.TotalRequests > 0 {
			out = append(out, topRoute{RouteID: rs.RouteID, Path: rs.Path, TotalRequests: rs.TotalRequests, TotalErrors: rs.TotalErrors})
		}
	}

	slices.SortFunc(out, func(a, b topRoute) int {
		if c := cmp.Compare(b.TotalRequests, a.TotalRequests); c != 0 {
			return c
		}

		return strings.Compare(a.Path, b.Path)
	})

	if len(out) > n {
		out = out[:n]
	}

	return out
}

func overviewStatsHandler(deps Deps) func(context.Context, overviewStatsRequest, contract.Principal) (overviewStatsResponse, error) {
	return func(context.Context, overviewStatsRequest, contract.Principal) (overviewStatsResponse, error) {
		s := deps.Gateway.Snapshot()
		cfg := deps.Gateway.Config()

		out := overviewStatsResponse{
			TotalRequests:         s.TotalRequests,
			TotalErrors:           s.TotalErrors,
			ErrorRate:             percent(s.TotalErrors, s.TotalRequests),
			LatencySamples:        s.LatencySamples,
			CacheLookups:          s.CacheHits + s.CacheMisses,
			CacheHitRate:          percent(s.CacheHits, s.CacheHits+s.CacheMisses),
			RateLimited:           s.RateLimited,
			CircuitBreaks:         s.CircuitBreaks,
			HealthyUpstreams:      s.HealthyUpstreams,
			TotalUpstreams:        s.TotalUpstreams,
			CircuitBreakerEnabled: cfg.CircuitBreaker.Enabled,
			DiscoveryEnabled:      cfg.Discovery.Enabled,
			StartedAt:             nonZeroTime(s.StartedAt),
			UptimeSeconds:         s.Uptime,
			TopRoutes:             topRoutes(s, 5),
		}

		if s.LatencySamples > 0 {
			out.AvgLatencyMs = ptr(s.AvgLatencyMs)
			out.P99LatencyMs = ptr(s.P99LatencyMs)
		}

		for _, r := range deps.Admin.ListRoutes(admin.RouteFilter{}) {
			out.TotalRoutes++
			if r.Enabled {
				out.EnabledRoutes++
			}
		}

		live := deps.Admin.Targets()
		for _, c := range deps.Gateway.Circuits() {
			if _, ok := live[c.TargetID]; !ok {
				continue
			}

			switch c.State {
			case bastion.CircuitOpen:
				out.OpenCircuits++
			case bastion.CircuitHalfOpen:
				out.HalfOpenCircuits++
			}
		}

		return out, nil
	}
}

func trafficStatsHandler(deps Deps) func(context.Context, trafficStatsRequest, contract.Principal) (trafficStatsResponse, error) {
	return func(context.Context, trafficStatsRequest, contract.Principal) (trafficStatsResponse, error) {
		s := deps.Gateway.Snapshot()

		out := trafficStatsResponse{
			TotalRequests:  s.TotalRequests,
			TotalErrors:    s.TotalErrors,
			RateLimited:    s.RateLimited,
			CircuitBreaks:  s.CircuitBreaks,
			CacheHits:      s.CacheHits,
			CacheMisses:    s.CacheMisses,
			LatencySamples: s.LatencySamples,
			Routes:         make([]routeTraffic, 0, len(s.RouteStats)),
		}

		if s.LatencySamples > 0 {
			out.AvgLatencyMs = ptr(s.AvgLatencyMs)
			out.P99LatencyMs = ptr(s.P99LatencyMs)
		}

		for _, rs := range s.RouteStats {
			rt := routeTraffic{
				RouteID:        rs.RouteID,
				Path:           rs.Path,
				TotalRequests:  rs.TotalRequests,
				TotalErrors:    rs.TotalErrors,
				ErrorRate:      percent(rs.TotalErrors, rs.TotalRequests),
				LatencySamples: rs.LatencySamples,
			}
			if rs.LatencySamples > 0 {
				rt.AvgLatencyMs = ptr(rs.AvgLatencyMs)
				rt.P99LatencyMs = ptr(rs.P99LatencyMs)
			}

			out.Routes = append(out.Routes, rt)
		}

		slices.SortFunc(out.Routes, func(a, b routeTraffic) int {
			if c := cmp.Compare(b.TotalRequests, a.TotalRequests); c != 0 {
				return c
			}

			return strings.Compare(a.Path, b.Path)
		})

		out.Total = len(out.Routes)

		return out, nil
	}
}

func circuitsListHandler(deps Deps) func(context.Context, circuitsListRequest, contract.Principal) (circuitsListResponse, error) {
	return func(context.Context, circuitsListRequest, contract.Principal) (circuitsListResponse, error) {
		cfg := deps.Gateway.Config().CircuitBreaker
		live := deps.Admin.Targets()

		snaps := map[string]bastion.CircuitBreakerSnapshot{}
		for _, c := range deps.Gateway.Circuits() {
			snaps[c.TargetID] = c
		}

		out := circuitsListResponse{
			Enabled:             cfg.Enabled,
			FailureThreshold:    cfg.FailureThreshold,
			ResetTimeoutSeconds: cfg.ResetTimeout.Seconds(),
			HalfOpenMax:         cfg.HalfOpenMax,
			Circuits:            make([]circuitView, 0, len(live)),
		}

		for id, ref := range live {
			v := circuitView{TargetID: id, URL: ref.URL, Routes: ref.Routes, State: bastion.CircuitClosed}
			if s, ok := snaps[id]; ok {
				v.Tracked = true
				v.State = s.State
				v.FailureCount = s.FailureCount
				v.LastFailure = nonZeroTime(s.LastFailure)
				v.LastStateChange = nonZeroTime(s.LastStateChange)
			}

			out.Circuits = append(out.Circuits, v)
		}

		slices.SortFunc(out.Circuits, func(a, b circuitView) int { return strings.Compare(a.TargetID, b.TargetID) })
		out.Total = len(out.Circuits)

		return out, nil
	}
}
