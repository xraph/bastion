package extension_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/extension"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/internal/telemetry/telemetrytest"
	"github.com/xraph/bastion/observability"
)

// A request through a real Forge app: Forge's server span, bastion's client
// span under it, the trace context at the upstream, and the metrics on the
// app's scrape under their forge_ names.
func TestExtension_TelemetryEndToEnd(t *testing.T) {
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	upstreamParent := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The gateway's health probes hit this server too.
		if r.URL.Path == health.DefaultPath {
			return
		}

		select {
		case upstreamParent <- r.Header.Get("Traceparent"):
		default:
		}
	}))
	t.Cleanup(up.Close)

	app := forge.New(forge.WithAppName("bastion-e2e"), forge.WithAppLogger(forge.NewNoopLogger()))

	rec := tracetest.NewSpanRecorder()
	if err := forge.RegisterSpanProcessor(app, rec); err != nil {
		t.Fatalf("RegisterSpanProcessor: %v", err)
	}

	ext := extension.New(
		bastion.WithDiscoveryEnabled(false),
		bastion.WithRoute(bastion.RouteConfig{
			Path:    "/svc/*",
			Enabled: true,
			Targets: []bastion.TargetConfig{{URL: up.URL}},
		}),
	)

	if err := app.RegisterExtension(ext); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/svc/users?email=alice@example.com", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}

	proxied := telemetrytest.One(t, rec, "proxy request")

	var server trace.SpanContext

	for _, s := range rec.Ended() {
		if s.SpanKind() == trace.SpanKindServer && s.SpanContext().TraceID() == proxied.SpanContext().TraceID() {
			server = s.SpanContext()
		}
	}

	if !server.IsValid() || proxied.Parent().SpanID() != server.SpanID() {
		t.Fatal("proxy request is not a child of Forge's server span")
	}

	if got := <-upstreamParent; !strings.Contains(got, proxied.SpanContext().SpanID().String()) {
		t.Errorf("upstream got traceparent %q, want the proxy request span", got)
	}

	rr = httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/nowhere", nil))

	body := telemetrytest.Scrape(t, app)

	for _, want := range []string{
		`forge_` + observability.MetricUpstreamRequestDuration + `_count{protocol="http",status_class="2xx"} 1`,
		`forge_` + observability.MetricRequestsRejected + `{reason="no_route"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %s", want)
		}
	}

	// Forge calls Start inside its boot span. The health loop Start begins
	// runs its first cycle at once, and that cycle must start a trace of
	// its own.
	if cycle := telemetrytest.Await(t, rec, "check upstream health", 1)[0]; cycle.Parent().IsValid() {
		t.Error("the health cycle hangs off the boot span")
	}

	telemetrytest.NeverRecorded(t, rec, "alice@example.com")
}

// A breaker that trips counts as a transition, and a gateway that is
// draining counts the requests it turns away.
func TestExtension_BreakerTransitionAndDraining(t *testing.T) {
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	app := forge.New(forge.WithAppName("bastion-breaker"), forge.WithAppLogger(forge.NewNoopLogger()))

	ext := extension.New(
		bastion.WithDiscoveryEnabled(false),
		bastion.WithCircuitBreaker(bastion.CircuitBreakerConfig{Enabled: true, FailureThreshold: 1, ResetTimeout: time.Hour, HalfOpenMax: 1}),
		bastion.WithRetry(bastion.RetryConfig{}),
		bastion.WithRoute(bastion.RouteConfig{
			Path:    "/svc/*",
			Enabled: true,
			Targets: []bastion.TargetConfig{{URL: deadURL}},
		}),
	)

	if err := app.RegisterExtension(ext); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	// The transition is recorded on the breaker's own goroutine, just
	// before the circuit-break hooks run.
	opened := make(chan struct{}, 1)
	ext.Gateway().Hooks().OnCircuitBreak(func(_ string, _, to bastion.CircuitState) {
		if to == bastion.CircuitOpen {
			select {
			case opened <- struct{}{}:
			default:
			}
		}
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/svc/x", nil))

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 from the dead upstream", rr.Code)
	}

	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the breaker never opened")
	}

	// Gateway.Stop is what sets draining. Under app.Run Forge stops the
	// HTTP server first, so this only shows when the extension is stopped
	// on its own, as here.
	if err := ext.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	rr = httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/svc/x", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d while draining, want 503", rr.Code)
	}

	body := telemetrytest.Scrape(t, app)

	for _, want := range []string{
		`forge_` + observability.MetricCircuitTransitions + `{state="open"} 1`,
		`forge_` + observability.MetricRequestsRejected + `{reason="draining"} 1`,
		`forge_` + observability.MetricUpstreamRequestDuration + `_count{protocol="http",status_class="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %s", want)
		}
	}
}
