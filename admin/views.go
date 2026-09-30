package admin

import (
	"slices"
	"strings"
	"time"

	bastion "github.com/xraph/bastion"
)

type RouteFilter struct{ Source, Protocol string }

type RouteSummary struct {
	ID             string                `json:"id"`
	Path           string                `json:"path"`
	Methods        []string              `json:"methods"` // empty = any method
	Protocol       bastion.RouteProtocol `json:"protocol"`
	Source         bastion.RouteSource   `json:"source"`
	ServiceName    string                `json:"serviceName"`
	Priority       int                   `json:"priority"` // effective, as sorted
	Enabled        bool                  `json:"enabled"`
	TargetCount    int                   `json:"targetCount"`
	HealthyTargets int                   `json:"healthyTargets"`
	Editable       bool                  `json:"editable"` // manual
	Config         bool                  `json:"config"`   // from the config file
	UpdatedAt      time.Time             `json:"updatedAt"`
}

type RouteInput struct {
	Path     string `json:"path"`     // without BasePath
	Priority int    `json:"priority"` // without ManualPriorityOffset
}

type TargetView struct {
	ID              string               `json:"id"`
	URL             string               `json:"url"`
	Weight          int                  `json:"weight"`
	Tags            []string             `json:"tags"`
	Healthy         bool                 `json:"healthy"`
	CircuitState    bastion.CircuitState `json:"circuitState"`
	Stats           bastion.TargetStats  `json:"stats"`
	TLS             bool                 `json:"tls"`
	HealthCheckPath string               `json:"healthCheckPath,omitempty"`
	OpenAPI         string               `json:"openapi,omitempty"`
	MetadataKeys    []string             `json:"metadataKeys"`
}

type RouteDetail struct {
	RouteSummary
	Input          *RouteInput               `json:"input,omitempty"` // manual routes only
	StripPrefix    bool                      `json:"stripPrefix"`
	AddPrefix      string                    `json:"addPrefix"`
	RewritePath    string                    `json:"rewritePath"`
	Headers        bastion.HeaderPolicy      `json:"headers"`
	Retry          *bastion.RetryConfig      `json:"retry,omitempty"`
	Timeout        *bastion.TimeoutConfig    `json:"timeout,omitempty"`
	RateLimit      *bastion.RateLimitConfig  `json:"rateLimit,omitempty"`
	Auth           *bastion.RouteAuthConfig  `json:"auth,omitempty"`
	CircuitBreaker *bastion.CBConfig         `json:"circuitBreaker,omitempty"`
	Cache          *bastion.RouteCacheConfig `json:"cache,omitempty"`
	TrafficPolicy  *bastion.TrafficPolicy    `json:"trafficPolicy,omitempty"`
	Transform      *bastion.TransformConfig  `json:"transform,omitempty"`
	MetadataKeys   []string                  `json:"metadataKeys"`
	Version        int64                     `json:"version"`
	CreatedAt      time.Time                 `json:"createdAt"`
	Targets        []TargetView              `json:"targets"`
}

type UpstreamRoute struct {
	RouteID  string `json:"routeId"`
	Path     string `json:"path"`
	TargetID string `json:"targetId"`
}

type Upstream struct {
	URL           string               `json:"url"`
	Healthy       bool                 `json:"healthy"`      // every entry healthy
	CircuitState  bastion.CircuitState `json:"circuitState"` // worst entry
	ActiveConns   int64                `json:"activeConns"`
	TotalRequests int64                `json:"totalRequests"`
	TotalErrors   int64                `json:"totalErrors"`
	AvgLatencyMs  float64              `json:"avgLatencyMs"` // request-weighted
	Routes        []UpstreamRoute      `json:"routes"`
}

type TargetRef struct {
	URL    string
	Routes []UpstreamRoute
}

func isConfigRoute(r *bastion.Route) bool {
	return r.Source == bastion.SourceManual && strings.HasPrefix(r.ID, "manual-")
}

func (s *Service) circuitStates() map[string]bastion.CircuitState {
	out := map[string]bastion.CircuitState{}
	if s.d.Breakers == nil {
		return out
	}

	for _, c := range s.d.Breakers.Snapshots() {
		out[c.TargetID] = c.State
	}

	return out
}

// stateOf reports a target's breaker state. A target with no breaker has
// never been selected, and a new breaker starts closed.
func stateOf(states map[string]bastion.CircuitState, id string) bastion.CircuitState {
	if st, ok := states[id]; ok {
		return st
	}

	return bastion.CircuitClosed
}

var circuitRank = map[bastion.CircuitState]int{
	bastion.CircuitClosed:   0,
	bastion.CircuitHalfOpen: 1,
	bastion.CircuitOpen:     2,
}

func worse(a, b bastion.CircuitState) bastion.CircuitState {
	if circuitRank[b] > circuitRank[a] {
		return b
	}

	return a
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return slices.Clone(s)
}

func summarize(r *bastion.Route) RouteSummary {
	healthy := 0
	for _, t := range r.Targets {
		if t.Healthy {
			healthy++
		}
	}

	return RouteSummary{
		ID:             r.ID,
		Path:           r.Path,
		Methods:        nonNil(r.Methods),
		Protocol:       r.Protocol,
		Source:         r.Source,
		ServiceName:    r.ServiceName,
		Priority:       r.Priority,
		Enabled:        r.Enabled,
		TargetCount:    len(r.Targets),
		HealthyTargets: healthy,
		Editable:       r.Source == bastion.SourceManual,
		Config:         isConfigRoute(r),
		UpdatedAt:      r.UpdatedAt,
	}
}

// ListRoutes returns the routes in match order, filtered by source and
// protocol when either is set.
func (s *Service) ListRoutes(f RouteFilter) []RouteSummary {
	routes := s.d.Routes.ListRoutes()
	out := make([]RouteSummary, 0, len(routes))

	for _, r := range routes {
		if f.Source != "" && string(r.Source) != f.Source {
			continue
		}

		if f.Protocol != "" && string(r.Protocol) != f.Protocol {
			continue
		}

		out = append(out, summarize(r))
	}

	return out
}

// input reports a manual route's path and priority as the operator entered
// them.
func (s *Service) input(r *bastion.Route) RouteInput {
	return RouteInput{
		Path:     strings.TrimPrefix(r.Path, strings.TrimRight(s.d.BasePath, "/")),
		Priority: r.Priority - ManualPriorityOffset,
	}
}

func targetView(t *bastion.Target, states map[string]bastion.CircuitState) TargetView {
	return TargetView{
		ID:              t.ID,
		URL:             RedactURL(t.URL),
		Weight:          t.Weight,
		Tags:            nonNil(t.Tags),
		Healthy:         t.Healthy,
		CircuitState:    stateOf(states, t.ID),
		Stats:           t.Stats(),
		TLS:             t.TLS != nil && t.TLS.Enabled,
		HealthCheckPath: t.Metadata["health_check_path"],
		OpenAPI:         t.Metadata["openapi"],
		MetadataKeys:    sortedKeys(t.Metadata),
	}
}

// GetRoute returns one route with its targets. Header values that may carry
// credentials are redacted, and metadata is reduced to its keys.
func (s *Service) GetRoute(id string) (RouteDetail, error) {
	r, ok := s.d.Routes.GetRoute(id)
	if !ok {
		return RouteDetail{}, ErrNotFound
	}

	states := s.circuitStates()
	d := RouteDetail{
		RouteSummary:   summarize(r),
		StripPrefix:    r.StripPrefix,
		AddPrefix:      r.AddPrefix,
		RewritePath:    r.RewritePath,
		Headers:        RedactHeaders(r.Headers),
		Retry:          r.Retry,
		Timeout:        r.Timeout,
		RateLimit:      r.RateLimit,
		Auth:           r.Auth,
		CircuitBreaker: r.CircuitBreaker,
		Cache:          r.Cache,
		TrafficPolicy:  redactTraffic(r.TrafficPolicy),
		Transform:      RedactTransform(r.Transform),
		MetadataKeys:   sortedKeys(r.Metadata),
		Version:        r.Version,
		CreatedAt:      r.CreatedAt,
		Targets:        make([]TargetView, 0, len(r.Targets)),
	}

	if r.Source == bastion.SourceManual {
		in := s.input(r)
		d.Input = &in
	}

	for _, t := range r.Targets {
		d.Targets = append(d.Targets, targetView(t, states))
	}

	return d, nil
}

// Upstreams groups every live target by URL. An upstream is healthy only
// when every entry for it is, and reports its worst breaker state.
func (s *Service) Upstreams() []Upstream {
	states := s.circuitStates()
	byURL := map[string]*Upstream{}
	weighted := map[string]float64{}

	for _, r := range s.d.Routes.ListRoutes() {
		for _, t := range r.Targets {
			u, ok := byURL[t.URL]
			if !ok {
				u = &Upstream{URL: t.URL, Healthy: true, CircuitState: bastion.CircuitClosed, Routes: []UpstreamRoute{}}
				byURL[t.URL] = u
			}

			st := t.Stats()
			u.Healthy = u.Healthy && t.Healthy
			u.CircuitState = worse(u.CircuitState, stateOf(states, t.ID))
			u.ActiveConns += st.ActiveConns
			u.TotalRequests += st.TotalRequests
			u.TotalErrors += st.TotalErrors
			weighted[t.URL] += st.AvgLatencyMs * float64(st.TotalRequests)
			u.Routes = append(u.Routes, UpstreamRoute{RouteID: r.ID, Path: r.Path, TargetID: t.ID})
		}
	}

	out := make([]Upstream, 0, len(byURL))
	for url, u := range byURL {
		if u.TotalRequests > 0 {
			u.AvgLatencyMs = weighted[url] / float64(u.TotalRequests)
		}

		// The key stays raw so two credentials for one host do not merge; only
		// the emitted URL is redacted.
		u.URL = RedactURL(u.URL)
		out = append(out, *u)
	}

	slices.SortFunc(out, func(a, b Upstream) int { return strings.Compare(a.URL, b.URL) })

	return out
}

// Targets indexes every live target id. A breaker whose id is not here
// belongs to a target no route uses any more.
func (s *Service) Targets() map[string]TargetRef {
	out := map[string]TargetRef{}

	for _, r := range s.d.Routes.ListRoutes() {
		for _, t := range r.Targets {
			ref := out[t.ID]
			ref.URL = RedactURL(t.URL)
			ref.Routes = append(ref.Routes, UpstreamRoute{RouteID: r.ID, Path: r.Path, TargetID: t.ID})
			out[t.ID] = ref
		}
	}

	return out
}
