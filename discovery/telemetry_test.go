package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/bastion/internal/telemetry"
	"github.com/xraph/bastion/internal/telemetry/telemetrytest"
	"github.com/xraph/bastion/observability"
)

// spyDiscovery records, for every ListServices call, whether ctx carried a
// valid span context. It lists nothing until listFrom calls have been made.
type spyDiscovery struct {
	mu       sync.Mutex
	calls    int
	listFrom int
	services []string
	inst     map[string][]*ServiceInstanceInfo
	traced   chan bool
}

func (s *spyDiscovery) ListServices(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()

	select {
	case s.traced <- trace.SpanContextFromContext(ctx).IsValid():
	default:
	}

	if n < s.listFrom {
		return nil, nil
	}

	return s.services, nil
}

func (s *spyDiscovery) DiscoverHealthy(_ context.Context, name string) ([]*ServiceInstanceInfo, error) {
	return s.inst[name], nil
}

// boot stands in for Forge calling Start inside its "start bastion" span:
// it returns a ctx carrying that span, already ended.
func boot(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ctx, span := telemetry.Start(ctx, "start bastion")
	span.End()

	return ctx
}

func TestDiscoveryLoop_IdlePollsCarryNoSpan(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	spy := &spyDiscovery{traced: make(chan bool, 16)}
	sd := NewManager(DiscoveryConfig{Enabled: true, PollInterval: 5 * time.Millisecond}, newTestLogger(), &mockRouteRegistry{}, spy)

	if err := sd.Start(boot(t)); err != nil {
		t.Fatal(err)
	}

	// The initial pass runs inside Start, under the boot span.
	if !<-spy.traced {
		t.Error("the initial pass in Start lost the boot span")
	}

	// The polls after it run on the loop's ctx, which must not carry it.
	for i := range 3 {
		if <-spy.traced {
			t.Errorf("idle poll %d listed services under the boot span", i+1)
		}
	}

	sd.Stop()

	if got := telemetrytest.Named(rec, spanDiscoverServices); len(got) != 0 {
		t.Errorf("idle polls started %d %q spans", len(got), spanDiscoverServices)
	}
}

func TestDiscoveryLoop_PollWithWorkIsARootSpan(t *testing.T) {
	app := telemetrytest.MetricsApp(t)
	rec := telemetrytest.Recorder(t)

	var (
		mu      sync.Mutex
		parents []string
	)

	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		parents = append(parents, r.Header.Get("Traceparent"))
		mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{"service_name": "users"})
	}))
	t.Cleanup(manifest.Close)

	spy := &spyDiscovery{
		traced:   make(chan bool, 64),
		listFrom: 2, // Start's pass finds nothing; the first poll finds users.
		services: []string{"users"},
		inst: map[string][]*ServiceInstanceInfo{"users": {{
			ID: "users-1", Name: "users", Address: "10.0.0.1", Port: 8080, Healthy: true,
			Metadata: map[string]string{"farp.enabled": "true", "farp.manifest": manifest.URL + "?email=alice@example.com"},
		}}},
	}

	sd := NewManager(DiscoveryConfig{Enabled: true, PollInterval: 5 * time.Millisecond, AutoPrefix: true}, newTestLogger(), &mockRouteRegistry{}, spy)
	sd.SetMetrics(observability.NewMetrics(app.Metrics(), observability.MetricsConfig{Enabled: true}))

	if err := sd.Start(boot(t)); err != nil {
		t.Fatal(err)
	}

	fetch := telemetrytest.Await(t, rec, spanFetchManifest, 1)[0]
	pass := telemetrytest.Await(t, rec, spanDiscoverServices, 1)[0]

	sd.Stop()

	if pass.Parent().IsValid() {
		t.Error("the poll's span hangs off another span; want a new trace")
	}

	if v, _ := telemetrytest.Attr(pass, "bastion.discovery.services"); v.AsInt64() != 1 {
		t.Errorf("bastion.discovery.services = %v", v.String())
	}

	if fetch.Parent().SpanID() != pass.SpanContext().SpanID() || fetch.SpanKind() != trace.SpanKindClient {
		t.Error("the manifest fetch is not a client span under the poll's span")
	}

	mu.Lock()
	got := parents[0]
	mu.Unlock()

	if !strings.Contains(got, pass.SpanContext().TraceID().String()) {
		t.Errorf("manifest server got traceparent %q, want the poll's trace", got)
	}

	telemetrytest.NeverRecorded(t, rec, "alice@example.com")

	series := strings.Join(telemetrytest.Series(telemetrytest.Scrape(t, app), "forge_"+observability.MetricBackgroundJobDuration+"_count"), "\n")
	if !strings.Contains(series, `job="discovery",result="ok"`) {
		t.Errorf("job duration series:\n%s", series)
	}
}

func TestOpenAPILoop_RefreshIsARootSpanAndFetchesUnderIt(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	var (
		mu      sync.Mutex
		parents []string
	)

	spec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		parents = append(parents, r.Header.Get("Traceparent"))
		mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{
			"openapi": "3.0.0",
			"info":    map[string]any{"title": "users", "version": "1"},
			"paths":   map[string]any{"/users": map[string]any{"get": map[string]any{}}},
		})
	}))
	t.Cleanup(spec.Close)

	rm := &mockRouteRegistry{routes: []*Route{{
		ID: "users", Path: "/users/*", ServiceName: "users",
		Targets: []*Target{{ID: "u1", URL: spec.URL, Metadata: map[string]string{"openapi": "/openapi.json"}}},
	}}}

	cfg := DefaultOpenAPIConfig()
	cfg.Enabled = true
	cfg.RefreshInterval = 5 * time.Millisecond
	cfg.FetchTimeout = 5 * time.Second

	ctx, cancel := context.WithCancel(boot(t))

	aggr := NewAggregator(cfg, newTestLogger(), rm, nil)
	aggr.Start(ctx)

	refresh := telemetrytest.Await(t, rec, "refresh openapi specs", 1)[0]

	cancel()

	if refresh.Parent().IsValid() {
		t.Error("the loop's refresh hangs off another span; want a new trace")
	}

	fetch := telemetrytest.Named(rec, "fetch openapi spec")
	merge := telemetrytest.Named(rec, "merge openapi specs")

	if !childOf(fetch, refresh.SpanContext()) || !childOf(merge, refresh.SpanContext()) {
		t.Error("the fetch and merge spans are not under the refresh span")
	}

	mu.Lock()
	got := strings.Join(parents, " ")
	mu.Unlock()

	if !strings.Contains(got, refresh.SpanContext().TraceID().String()) {
		t.Errorf("spec server got traceparents %q, none in the refresh's trace", got)
	}

	if v, _ := telemetrytest.Attr(refresh, "bastion.openapi.paths"); v.AsInt64() < 1 {
		t.Errorf("bastion.openapi.paths = %v", v.String())
	}
}

func TestOpenAPI_RefreshDetachedStartsALinkedTrace(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	spec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"openapi":"3.0.0","info":{"title":"x","version":"1"},"paths":{}}`))
	}))
	t.Cleanup(spec.Close)

	rm := &mockRouteRegistry{routes: []*Route{{
		ID: "users", Path: "/users/*", ServiceName: "users",
		Targets: []*Target{{ID: "u1", URL: spec.URL, Metadata: map[string]string{"openapi": "/openapi.json"}}},
	}}}

	cfg := DefaultOpenAPIConfig()
	cfg.Enabled = true
	cfg.FetchTimeout = 5 * time.Second

	aggr := NewAggregator(cfg, newTestLogger(), rm, nil)

	reqCtx, req := telemetry.Start(context.Background(), "POST /refresh", trace.WithSpanKind(trace.SpanKindServer))
	req.End()

	aggr.RefreshDetached(reqCtx)

	refresh := telemetrytest.One(t, rec, "refresh openapi specs")

	if refresh.Parent().IsValid() {
		t.Error("a detached refresh hangs off the request span")
	}

	if links := refresh.Links(); len(links) != 1 || links[0].SpanContext.SpanID() != req.SpanContext().SpanID() {
		t.Errorf("links = %+v, want one to the request span", links)
	}
}

// childOf reports whether any of spans is a child of parent. A loop keeps
// refreshing while the test looks, so later refreshes add spans of their
// own under other parents.
func childOf(spans []sdktrace.ReadOnlySpan, parent trace.SpanContext) bool {
	for _, s := range spans {
		if s.Parent().SpanID() == parent.SpanID() {
			return true
		}
	}

	return false
}
