package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/xraph/forge"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/internal/telemetry"
	"github.com/xraph/bastion/internal/telemetry/telemetrytest"
	"github.com/xraph/bastion/observability"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

const email = "alice@example.com"

// traced is an engine wired for telemetry: spans go to rec, metrics to app.
type traced struct {
	engine *proxy.Engine
	rec    *tracetest.SpanRecorder
	app    forge.App
}

func newTraced(t *testing.T, cfg bastion.Config, route *bastion.Route) traced {
	t.Helper()

	// The app first: forge.New replaces the OTel globals, MetricsApp puts
	// them back, and the recorder then goes in on top.
	app := telemetrytest.MetricsApp(t)
	rec := telemetrytest.Recorder(t)

	logger := forge.NewNoopLogger()
	rm := routing.NewManager()

	route.Enabled = true
	if route.Source == "" {
		route.Source = bastion.SourceManual
	}

	if err := rm.AddRoute(route); err != nil {
		t.Fatal(err)
	}

	engine := proxy.NewEngine(cfg, logger, rm, health.NewMonitor(health.Config{}, logger),
		resilience.NewCBManager(bastion.CircuitBreakerConfig{}), bastion.NewRateLimiter(bastion.RateLimitConfig{}),
		proxy.NewStatsCollector(), bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))
	engine.SetMetrics(observability.NewMetrics(app.Metrics(), observability.MetricsConfig{Enabled: true}))
	engine.SetTrafficRouter(routing.NewTrafficSplitter(true))

	if cfg.Retry.Enabled {
		engine.SetRetryPolicy(resilience.NewRetryExecutor(cfg.Retry))
	}

	return traced{engine: engine, rec: rec, app: app}
}

// serve runs req through the engine under a server span standing in for
// the one Forge starts, and returns that span.
func (tr traced) serve(w http.ResponseWriter, req *http.Request) trace.Span {
	ctx, span := telemetry.Start(req.Context(), "GET /*path", trace.WithSpanKind(trace.SpanKindServer))
	tr.engine.ServeHTTP(w, req.WithContext(ctx))
	span.End()

	return span
}

func (tr traced) series(t *testing.T, name string) string {
	t.Helper()

	return strings.Join(telemetrytest.Series(telemetrytest.Scrape(t, tr.app), "forge_"+name), "\n")
}

// echo answers 200 and records the traceparent of every request.
type echo struct {
	mu      sync.Mutex
	parents []string
	srv     *httptest.Server
}

func newEcho(t *testing.T) *echo {
	t.Helper()

	e := &echo{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.parents = append(e.parents, r.Header.Get("Traceparent"))
		e.mu.Unlock()

		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(e.srv.Close)

	return e
}

func (e *echo) traceparents() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.parents...)
}

// traceparent is the W3C header a sampled span context goes upstream as.
func traceparent(sc trace.SpanContext) string {
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"
}

func attr(t *testing.T, rec *tracetest.SpanRecorder, name, key string) string {
	t.Helper()

	v, ok := telemetrytest.Attr(telemetrytest.One(t, rec, name), key)
	if !ok {
		t.Fatalf("%s has no %s", name, key)
	}

	return v.String()
}

func TestTelemetry_ProxyRequestIsAClientSpanUnderTheServerSpan(t *testing.T) {
	up := newEcho(t)
	tr := newTraced(t, bastion.Config{}, &bastion.Route{
		ID:       "r-http",
		Path:     "/svc/*",
		Targets:  []*bastion.Target{{ID: "t", URL: up.srv.URL, Weight: 1, Healthy: true}},
		Protocol: bastion.ProtocolHTTP,
	})

	// Personal data in the path, the query and the headers.
	req := httptest.NewRequest(http.MethodGet, "/svc/users/"+email+"?email="+email, nil)
	req.Header.Set("Authorization", "Bearer token-"+email)
	req.Header.Set("Cookie", "session="+email)

	rec := httptest.NewRecorder()
	server := tr.serve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	span := telemetrytest.One(t, tr.rec, "proxy request")

	if span.SpanKind() != trace.SpanKindClient {
		t.Errorf("kind = %v", span.SpanKind())
	}

	if span.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Error("proxy request is not a child of the server span")
	}

	for key, want := range map[string]string{
		"bastion.route.id":          "r-http",
		"bastion.route.path":        "/svc/*",
		"bastion.route.source":      "manual",
		"bastion.protocol":          "http",
		"bastion.proxy.attempt":     "1",
		"http.request.method":       "GET",
		"server.address":            "127.0.0.1",
		"http.response.status_code": "200",
	} {
		if got := attr(t, tr.rec, "proxy request", key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// The upstream joins the trace under the client span, not the caller's.
	if got := up.traceparents(); len(got) != 1 || got[0] != traceparent(span.SpanContext()) {
		t.Errorf("upstream traceparents = %v", got)
	}

	// The server span names the route.
	if got := attr(t, tr.rec, "GET /*path", "bastion.route.id"); got != "r-http" {
		t.Errorf("server span bastion.route.id = %q", got)
	}

	telemetrytest.NeverRecorded(t, tr.rec, email)

	if got := tr.series(t, observability.MetricUpstreamRequestDuration+"_count"); !strings.Contains(got, `{protocol="http",status_class="2xx"} 1`) {
		t.Errorf("upstream duration series:\n%s", got)
	}
}

func TestTelemetry_EachRetryAttemptGetsItsOwnSpan(t *testing.T) {
	up := newFlaky(t, 1, http.StatusServiceUnavailable)
	tr := newTraced(t, bastion.Config{Retry: retryCfg(http.MethodGet)}, &bastion.Route{
		ID:       "r-retry",
		Path:     "/svc/*",
		Targets:  []*bastion.Target{{ID: "t", URL: up.srv.URL, Weight: 1, Healthy: true}},
		Protocol: bastion.ProtocolHTTP,
	})

	rec := httptest.NewRecorder()
	server := tr.serve(rec, httptest.NewRequest(http.MethodGet, "/svc/x", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	spans := telemetrytest.Named(tr.rec, "proxy request")
	if len(spans) != 2 {
		t.Fatalf("got %d proxy request spans, want 2", len(spans))
	}

	for i, s := range spans {
		if s.Parent().SpanID() != server.SpanContext().SpanID() {
			t.Errorf("attempt %d is not a child of the server span", i+1)
		}

		if v, _ := telemetrytest.Attr(s, "bastion.proxy.attempt"); v.AsInt64() != int64(i+1) {
			t.Errorf("span %d has attempt %d", i, v.AsInt64())
		}
	}

	if spans[0].Status().Code != codes.Error || spans[1].Status().Code != codes.Unset {
		t.Errorf("statuses = %v, %v; want the 503 attempt failed and the retry not", spans[0].Status(), spans[1].Status())
	}

	if got := tr.series(t, observability.MetricRetriesAttempted); !strings.Contains(got, `{result="sent"} 1`) {
		t.Errorf("retries series:\n%s", got)
	}

	got := tr.series(t, observability.MetricUpstreamRequestDuration+"_count")
	if !strings.Contains(got, `status_class="5xx"} 1`) || !strings.Contains(got, `status_class="2xx"} 1`) {
		t.Errorf("upstream duration series:\n%s", got)
	}
}

func TestTelemetry_RejectionsAreCountedAndNamedOnTheServerSpan(t *testing.T) {
	tr := newTraced(t, bastion.Config{}, &bastion.Route{
		ID:       "r-dead",
		Path:     "/svc/*",
		Targets:  []*bastion.Target{{ID: "t", URL: "http://127.0.0.1:1", Weight: 1, Healthy: false}},
		Protocol: bastion.ProtocolHTTP,
	})

	rec := httptest.NewRecorder()
	tr.serve(rec, httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}

	if got := attr(t, tr.rec, "GET /*path", "bastion.reject.reason"); got != "no_route" {
		t.Errorf("reject reason = %q", got)
	}

	tr.rec.Reset()

	rec = httptest.NewRecorder()
	tr.serve(rec, httptest.NewRequest(http.MethodGet, "/svc/x", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}

	if len(telemetrytest.Named(tr.rec, "proxy request")) != 0 {
		t.Error("a rejected request got a proxy request span")
	}

	got := tr.series(t, observability.MetricRequestsRejected)
	if !strings.Contains(got, `{reason="no_route"} 1`) || !strings.Contains(got, `{reason="no_healthy_upstream"} 1`) {
		t.Errorf("rejected series:\n%s", got)
	}
}

func TestTelemetry_MirrorCopyIsALinkedRootSpan(t *testing.T) {
	live := newEcho(t)
	shadow := newEcho(t)
	tr := newTraced(t, bastion.Config{}, &bastion.Route{
		ID:            "r-mirror",
		Path:          "/svc/*",
		Targets:       []*bastion.Target{{ID: "live", URL: live.srv.URL, Weight: 1, Healthy: true}},
		Protocol:      bastion.ProtocolHTTP,
		TrafficPolicy: &bastion.TrafficPolicy{Type: bastion.TrafficMirror, MirrorTarget: shadow.srv.URL},
	})

	server := tr.serve(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/svc/x?email="+email, nil))

	mirror := telemetrytest.Await(t, tr.rec, "mirror request", 1)[0]

	if mirror.Parent().IsValid() {
		t.Error("the mirror span has a parent; want a new trace")
	}

	if mirror.SpanKind() != trace.SpanKindClient {
		t.Errorf("kind = %v", mirror.SpanKind())
	}

	links := mirror.Links()
	if len(links) != 1 || links[0].SpanContext.SpanID() != server.SpanContext().SpanID() {
		t.Errorf("links = %+v, want one to the server span", links)
	}

	got := shadow.traceparents()
	if len(got) != 1 || !strings.Contains(got[0], mirror.SpanContext().TraceID().String()) {
		t.Errorf("shadow traceparents = %v, want the mirror span's trace", got)
	}

	telemetrytest.NeverRecorded(t, tr.rec, email)
}

func TestTelemetry_SSESpanEndsAtTheHeaders(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: hi\n\n")
	}))
	t.Cleanup(up.Close)

	tr := newTraced(t, bastion.Config{}, &bastion.Route{
		ID:       "r-sse",
		Path:     "/svc/*",
		Targets:  []*bastion.Target{{ID: "t", URL: up.URL, Weight: 1, Healthy: true}},
		Protocol: bastion.ProtocolSSE,
	})

	rec := httptest.NewRecorder()
	tr.serve(rec, httptest.NewRequest(http.MethodGet, "/svc/events", nil))

	if !strings.Contains(rec.Body.String(), "data: hi") {
		t.Fatalf("body %q", rec.Body.String())
	}

	if got := attr(t, tr.rec, "proxy request", "bastion.protocol"); got != "sse" {
		t.Errorf("protocol = %q", got)
	}

	if got := tr.series(t, observability.MetricActiveStreams); !strings.Contains(got, `{protocol="sse"} 0`) {
		t.Errorf("active streams after the stream ended:\n%s", got)
	}
}

func TestTelemetry_GRPCCallRecordsItsStatus(t *testing.T) {
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Traceparent") == "" {
			http.Error(w, "no traceparent", http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0, 0, 0, 0, 0})
		w.Header().Set("Grpc-Status", "14")
	}))
	up.EnableHTTP2 = true
	up.StartTLS()
	t.Cleanup(up.Close)

	cfg := bastion.Config{}
	cfg.TLS.InsecureSkipVerify = true

	tr := newTraced(t, cfg, &bastion.Route{
		ID:       "r-grpc",
		Path:     "/svc/*",
		Targets:  []*bastion.Target{{ID: "t", URL: up.URL, Weight: 1, Healthy: true}},
		Protocol: bastion.ProtocolGRPC,
	})

	req := httptest.NewRequest(http.MethodPost, "/svc/pkg.Service/Method", strings.NewReader("\x00\x00\x00\x00\x00"))
	req.Header.Set("Content-Type", "application/grpc")

	rec := httptest.NewRecorder()
	tr.serve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}

	span := telemetrytest.One(t, tr.rec, "proxy request")

	if v, _ := telemetrytest.Attr(span, "rpc.grpc.status_code"); v.AsInt64() != 14 {
		t.Errorf("rpc.grpc.status_code = %v", v.String())
	}

	if span.Status().Code != codes.Error {
		t.Errorf("UNAVAILABLE did not fail the span: %v", span.Status())
	}

	got := tr.series(t, observability.MetricUpstreamRequestDuration+"_count")
	if !strings.Contains(got, `{protocol="grpc",status_class="2xx"} 1`) {
		t.Errorf("upstream duration series:\n%s", got)
	}
}

func TestTelemetry_WebSocketHandshakeSpan(t *testing.T) {
	gotParent := make(chan string, 1)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotParent <- r.Header.Get("Traceparent")

		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(up.Close)

	tr := newTraced(t, bastion.Config{}, &bastion.Route{
		ID:       "r-ws",
		Path:     "/svc/*",
		Targets:  []*bastion.Target{{ID: "t", URL: up.URL, Weight: 1, Healthy: true}},
		Protocol: bastion.ProtocolWebSocket,
	})

	served := make(chan trace.Span, 1)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served <- tr.serve(w, r)
	}))
	t.Cleanup(gw.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, _, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(gw.URL, "http")+"/svc/socket")
	if err != nil {
		t.Fatal(err)
	}

	_ = conn.Close()

	server := <-served
	span := telemetrytest.One(t, tr.rec, "proxy request")

	if span.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Error("the handshake span is not a child of the server span")
	}

	if got := <-gotParent; got != traceparent(span.SpanContext()) {
		t.Errorf("upstream got traceparent %q", got)
	}

	if v, _ := telemetrytest.Attr(span, "http.response.status_code"); v.AsInt64() != http.StatusSwitchingProtocols {
		t.Errorf("status = %v", v.String())
	}

	if got := tr.series(t, observability.MetricUpstreamRequestDuration+"_count"); !strings.Contains(got, `{protocol="websocket",status_class="1xx"} 1`) {
		t.Errorf("upstream duration series:\n%s", got)
	}

	if got := tr.series(t, observability.MetricActiveStreams); !strings.Contains(got, `{protocol="websocket"} 0`) {
		t.Errorf("active streams after the socket closed:\n%s", got)
	}
}
