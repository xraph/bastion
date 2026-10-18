// Package telemetrytest records bastion's spans and scrapes its metrics in
// tests.
package telemetrytest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Recorder installs a tracer provider that records every span, and the W3C
// trace context propagator, as the OTel globals. The previous ones come
// back when the test ends.
func Recorder(t testing.TB) *tracetest.SpanRecorder {
	t.Helper()

	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())

		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	return rec
}

// MetricsApp returns a Forge app to record metrics into and scrape. forge.New
// installs its own tracer provider and propagator as the globals; MetricsApp
// puts the previous ones back, so a Recorder installed before it keeps
// recording.
func MetricsApp(t testing.TB) forge.App {
	t.Helper()

	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()

	app := forge.New(forge.WithAppName("bastion-telemetry-test"), forge.WithAppLogger(forge.NewNoopLogger()))

	otel.SetTracerProvider(prevTP)
	otel.SetTextMapPropagator(prevProp)

	return app
}

// Scrape returns the Prometheus exposition of app's metrics.
func Scrape(t testing.TB, app forge.App) string {
	t.Helper()

	p, ok := app.Metrics().(interface{ PrometheusHandler() http.Handler })
	if !ok {
		t.Fatalf("app metrics %T have no Prometheus handler", app.Metrics())
	}

	rr := httptest.NewRecorder()
	p.PrometheusHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/_/metrics", nil))

	body, _ := io.ReadAll(rr.Body)

	return string(body)
}

// Series returns the scrape lines for the metric called name (its sample
// lines, not HELP or TYPE).
func Series(body, name string) []string {
	var out []string

	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, name+"{") || strings.HasPrefix(line, name+" ") {
			out = append(out, line)
		}
	}

	return out
}

// Named returns the ended spans called name.
func Named(rec *tracetest.SpanRecorder, name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan

	for _, s := range rec.Ended() {
		if s.Name() == name {
			out = append(out, s)
		}
	}

	return out
}

// Await waits until n spans called name have ended, for spans that end on
// a goroutine of their own, and returns them. It fails t after five
// seconds.
func Await(t testing.TB, rec *tracetest.SpanRecorder, name string, n int) []sdktrace.ReadOnlySpan {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for {
		if spans := Named(rec, name); len(spans) >= n {
			return spans
		}

		if time.Now().After(deadline) {
			t.Fatalf("waited for %d %q spans, got %v", n, name, names(rec))
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// One returns the only ended span called name, failing t unless there is
// exactly one.
func One(t testing.TB, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	spans := Named(rec, name)
	if len(spans) != 1 {
		t.Fatalf("want one %q span, got %d (all: %v)", name, len(spans), names(rec))
	}

	return spans[0]
}

func names(rec *tracetest.SpanRecorder) []string {
	var out []string

	for _, s := range rec.Ended() {
		out = append(out, s.Name())
	}

	return out
}

// Attr returns the value of key on s and whether s has it.
func Attr(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}

	return attribute.Value{}, false
}

// NeverRecorded fails t if value shows up anywhere on the spans rec ended:
// a name, an attribute key or value, an event or its attributes, a link's
// attributes or a status description.
func NeverRecorded(t testing.TB, rec *tracetest.SpanRecorder, value string) {
	t.Helper()

	check := func(span, where, got string) {
		if strings.Contains(got, value) {
			t.Errorf("span %q carries %q in %s: %q", span, value, where, got)
		}
	}

	attrs := func(span, where string, kvs []attribute.KeyValue) {
		for _, kv := range kvs {
			check(span, where, string(kv.Key)+"="+kv.Value.String())
		}
	}

	for _, s := range rec.Ended() {
		check(s.Name(), "its name", s.Name())
		check(s.Name(), "its status", s.Status().Description)
		attrs(s.Name(), "an attribute", s.Attributes())

		for _, ev := range s.Events() {
			check(s.Name(), "an event", ev.Name)
			attrs(s.Name(), "an event attribute", ev.Attributes)
		}

		for _, l := range s.Links() {
			attrs(s.Name(), "a link attribute", l.Attributes)
		}
	}
}
