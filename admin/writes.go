package admin

import (
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"

	bastion "github.com/xraph/bastion"
)

var validSchemes = map[string]bool{"http": true, "https": true, "ws": true, "wss": true}

var validProtocols = map[bastion.RouteProtocol]bool{
	bastion.ProtocolHTTP: true, bastion.ProtocolWebSocket: true, bastion.ProtocolSSE: true,
	bastion.ProtocolGRPC: true, bastion.ProtocolGraphQL: true,
}

var validMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
}

func validate(dto bastion.RouteDTO) error {
	if !strings.HasPrefix(dto.Path, "/") {
		return &ValidationError{Field: "path", Message: "must start with /"}
	}

	if len(dto.Targets) == 0 {
		return &ValidationError{Field: "targets", Message: "at least one upstream is required"}
	}

	seen := map[string]bool{}

	for i, t := range dto.Targets {
		u, err := url.Parse(t.URL)
		if err != nil || u.Host == "" || !validSchemes[u.Scheme] {
			return &ValidationError{Field: "targets", Message: fmt.Sprintf("upstream %d: %q is not an http, https, ws or wss URL", i+1, t.URL)}
		}

		if seen[t.URL] {
			return &ValidationError{Field: "targets", Message: fmt.Sprintf("upstream %d: %s is listed twice", i+1, t.URL)}
		}

		seen[t.URL] = true

		if t.Weight < 0 {
			return &ValidationError{Field: "targets", Message: fmt.Sprintf("upstream %d: weight cannot be negative", i+1)}
		}
	}

	if dto.Protocol != "" && !validProtocols[dto.Protocol] {
		return &ValidationError{Field: "protocol", Message: fmt.Sprintf("%q is not a protocol bastion proxies", dto.Protocol)}
	}

	for _, m := range dto.Methods {
		if !validMethods[strings.ToUpper(m)] {
			return &ValidationError{Field: "methods", Message: fmt.Sprintf("%q is not an HTTP method", m)}
		}
	}

	return nil
}

func upper(ms []string) []string {
	if len(ms) == 0 {
		return nil
	}

	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = strings.ToUpper(m)
	}

	return out
}

func methodsOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}

	for _, m := range a {
		if slices.ContainsFunc(b, func(x string) bool { return strings.EqualFold(x, m) }) {
			return true
		}
	}

	return false
}

// checkConflict refuses a second manual route on the same path and an
// overlapping method. Discovered routes are not checked: shadowing one is
// what the manual priority offset is for.
func (s *Service) checkConflict(r *bastion.Route) error {
	for _, o := range s.d.Routes.ListRoutes() {
		if o.ID == r.ID || o.Source != bastion.SourceManual || o.Path != r.Path {
			continue
		}

		if methodsOverlap(o.Methods, r.Methods) {
			return &ConflictError{Path: r.Path, OtherID: o.ID}
		}
	}

	return nil
}

func sameTarget(t *bastion.Target, td bastion.TargetDTO, weight int) bool {
	return t.Weight == weight &&
		slices.Equal(t.Tags, td.Tags) &&
		maps.Equal(t.Metadata, td.Metadata) &&
		reflect.DeepEqual(t.TLS, td.TLS)
}

func newTarget(id string, td bastion.TargetDTO, weight int) *bastion.Target {
	return &bastion.Target{
		ID:       id,
		URL:      td.URL,
		Weight:   weight,
		Healthy:  true,
		Tags:     slices.Clone(td.Tags),
		Metadata: maps.Clone(td.Metadata),
		TLS:      td.TLS,
	}
}

// build turns entered values into a route. Against an existing route, a
// target whose URL is unchanged keeps its id, so its health entry and breaker
// carry over; if nothing else about it changed it keeps its object too, and
// its counters with it. A changed target is a new object because writing a
// live target's fields races the load balancer.
func (s *Service) build(id string, dto bastion.RouteDTO, existing *bastion.Route) *bastion.Route {
	prior := map[string]*bastion.Target{}
	used := map[string]bool{}

	if existing != nil {
		for _, t := range existing.Targets {
			prior[t.URL] = t
			used[t.ID] = true
		}
	}

	targets := make([]*bastion.Target, 0, len(dto.Targets))
	next := 0

	for _, td := range dto.Targets {
		weight := td.Weight
		if weight <= 0 {
			weight = 1
		}

		if t, ok := prior[td.URL]; ok {
			if sameTarget(t, td, weight) {
				targets = append(targets, t)
			} else {
				nt := newTarget(t.ID, td, weight)
				// What the health checker last learned about this upstream
				// still holds after a weight or tag edit.
				nt.Healthy = t.Healthy
				targets = append(targets, nt)
			}

			continue
		}

		for used[fmt.Sprintf("%s/%d", id, next)] {
			next++
		}

		tid := fmt.Sprintf("%s/%d", id, next)
		used[tid] = true
		targets = append(targets, newTarget(tid, td, weight))
	}

	protocol := dto.Protocol
	if protocol == "" {
		protocol = bastion.ProtocolHTTP
	}

	// A config route carries a service name the editor does not show.
	serviceName := ""
	if existing != nil {
		serviceName = existing.ServiceName
	}

	return &bastion.Route{
		ID:             id,
		ServiceName:    serviceName,
		Path:           strings.TrimRight(s.d.BasePath, "/") + dto.Path,
		Methods:        upper(dto.Methods),
		Targets:        targets,
		StripPrefix:    dto.StripPrefix,
		AddPrefix:      dto.AddPrefix,
		RewritePath:    dto.RewritePath,
		Headers:        cloneHeaders(dto.Headers),
		Protocol:       protocol,
		Source:         bastion.SourceManual,
		Priority:       dto.Priority + ManualPriorityOffset,
		Enabled:        dto.Enabled,
		Retry:          cloneRetry(dto.Retry),
		Timeout:        clonePtr(dto.Timeout),
		RateLimit:      cloneRateLimit(dto.RateLimit),
		Auth:           cloneAuth(dto.Auth),
		CircuitBreaker: clonePtr(dto.CircuitBreaker),
		Cache:          cloneCache(dto.Cache),
		TrafficPolicy:  cloneTraffic(dto.TrafficPolicy),
		Transform:      cloneTransform(dto.Transform),
		Metadata:       maps.Clone(dto.Metadata),
	}
}

// forget drops health entries and breakers for targets that no route uses
// after a write. Ids still used elsewhere are kept: legacy REST routes share
// target ids by URL.
func (s *Service) forget(removed []*bastion.Target) {
	if len(removed) == 0 {
		return
	}

	live := s.Targets()

	for _, t := range removed {
		if _, ok := live[t.ID]; ok {
			continue
		}

		if s.d.Health != nil {
			s.d.Health.Deregister(t.ID)
		}

		if s.d.Breakers != nil {
			s.d.Breakers.Remove(t.ID)
		}
	}
}

func (s *Service) manual(id string) (*bastion.Route, error) {
	r, ok := s.d.Routes.GetRoute(id)
	if !ok {
		return nil, ErrNotFound
	}

	if r.Source != bastion.SourceManual {
		return nil, &SourceError{ID: id, Source: r.Source}
	}

	return r, nil
}

// Entry returns a manual route's values as the operator entered them, with
// nothing redacted. It is for merging a partial update, never for display.
func (s *Service) Entry(id string) (bastion.RouteDTO, error) {
	r, err := s.manual(id)
	if err != nil {
		return bastion.RouteDTO{}, err
	}

	in := s.input(r)
	dto := bastion.RouteDTO{
		Path:           in.Path,
		Methods:        slices.Clone(r.Methods),
		StripPrefix:    r.StripPrefix,
		AddPrefix:      r.AddPrefix,
		RewritePath:    r.RewritePath,
		Headers:        cloneHeaders(r.Headers),
		Protocol:       r.Protocol,
		Priority:       in.Priority,
		Enabled:        r.Enabled,
		Retry:          cloneRetry(r.Retry),
		Timeout:        clonePtr(r.Timeout),
		RateLimit:      cloneRateLimit(r.RateLimit),
		Auth:           cloneAuth(r.Auth),
		CircuitBreaker: clonePtr(r.CircuitBreaker),
		Cache:          cloneCache(r.Cache),
		TrafficPolicy:  cloneTraffic(r.TrafficPolicy),
		Transform:      cloneTransform(r.Transform),
		Metadata:       maps.Clone(r.Metadata),
		Targets:        make([]bastion.TargetDTO, 0, len(r.Targets)),
	}

	for _, t := range r.Targets {
		dto.Targets = append(dto.Targets, bastion.TargetDTO{
			URL: t.URL, Weight: t.Weight, Tags: slices.Clone(t.Tags), Metadata: maps.Clone(t.Metadata), TLS: t.TLS,
		})
	}

	return dto, nil
}

// CreateRoute adds a manual route from entered values.
func (s *Service) CreateRoute(dto bastion.RouteDTO) (*bastion.Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validate(dto); err != nil {
		return nil, err
	}

	r := s.build(uuid.NewString(), dto, nil)
	if err := s.checkConflict(r); err != nil {
		return nil, err
	}

	if err := s.d.Routes.AddRoute(r); err != nil {
		return nil, err
	}

	return r, nil
}

// UpdateRoute replaces a manual route from entered values.
func (s *Service) UpdateRoute(id string, dto bastion.RouteDTO) (*bastion.Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.manual(id)
	if err != nil {
		return nil, err
	}

	if err := validate(dto); err != nil {
		return nil, err
	}

	r := s.build(id, dto, existing)
	if err := s.checkConflict(r); err != nil {
		return nil, err
	}

	if err := s.d.Routes.UpdateRoute(r); err != nil {
		return nil, err
	}

	kept := map[string]bool{}
	for _, t := range r.Targets {
		kept[t.ID] = true
	}

	var removed []*bastion.Target
	for _, t := range existing.Targets {
		if !kept[t.ID] {
			removed = append(removed, t)
		}
	}

	s.forget(removed)

	return r, nil
}

// DeleteRoute removes a manual route.
func (s *Service) DeleteRoute(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.manual(id)
	if err != nil {
		return err
	}

	if err := s.d.Routes.RemoveRoute(id); err != nil {
		return err
	}

	s.forget(existing.Targets)

	return nil
}

// SetEnabled turns a manual route on or off. It writes a copy: the live route
// is read by the proxy without a lock.
func (s *Service) SetEnabled(id string, enabled bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.manual(id)
	if err != nil {
		return false, err
	}

	cp := *existing
	cp.Enabled = enabled

	if err := s.d.Routes.UpdateRoute(&cp); err != nil {
		return false, err
	}

	return !isConfigRoute(existing) && s.persisted(), nil
}
