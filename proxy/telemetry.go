package proxy

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/internal/telemetry"
	"github.com/xraph/bastion/observability"
)

// spanProxyRequest names the Client span of every attempt to reach an
// upstream, whatever the protocol.
const spanProxyRequest = "proxy request"

// spanMirrorRequest names the Client span of a mirrored copy.
const spanMirrorRequest = "mirror request"

// SetMetrics sets where the engine records its metrics. Nil records none.
func (pe *Engine) SetMetrics(m *observability.Metrics) { pe.metrics = m }

// routeAttributes describe the matched route on a span. The route ID and
// path pattern come from configuration or discovery, never from the
// client, so they are fine on a span. They never become metric labels.
func routeAttributes(route *bastion.Route, protocol observability.Protocol) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("bastion.route.id", route.ID),
		attribute.String("bastion.route.path", route.Path),
		attribute.String("bastion.route.source", string(route.Source)),
		attribute.String("bastion.protocol", string(protocol)),
	}
}

// annotateServerSpan puts the matched route on the request's server span,
// the one Forge starts for the catch-all route.
func annotateServerSpan(r *http.Request, route *bastion.Route, protocol observability.Protocol) {
	if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
		span.SetAttributes(routeAttributes(route, protocol)...)
	}
}

// reject counts a request the gateway answers itself and says why on the
// request's server span.
func (pe *Engine) reject(r *http.Request, reason observability.RejectReason) {
	pe.metrics.RequestRejected(reason)

	if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
		span.SetAttributes(attribute.String("bastion.reject.reason", string(reason)))
	}
}

// requestProtocol is the protocol a request on route goes upstream with,
// decided the same way ServeHTTP dispatches it.
func requestProtocol(r *http.Request, route *bastion.Route) observability.Protocol {
	switch {
	case isWebSocketUpgrade(r):
		return observability.ProtocolWebSocket
	case isSSERequest(r) || route.Protocol == bastion.ProtocolSSE:
		return observability.ProtocolSSE
	case isGRPCRequest(r) || route.Protocol == bastion.ProtocolGRPC:
		return observability.ProtocolGRPC
	default:
		return observability.ProtocolHTTP
	}
}

// upstreamTransport sends one proxied request to its upstream. Each call
// to RoundTrip is one attempt: it gets its own Client span, child of the
// request's server span, with the trace context sent upstream, and one
// observation on bastion_upstream_request_duration_seconds. It sits below
// retryTransport, so a retried request shows one span per attempt.
type upstreamTransport struct {
	base     http.RoundTripper
	route    *bastion.Route
	protocol observability.Protocol
	metrics  *observability.Metrics

	// stream ends the span once the response headers arrive, for SSE,
	// whose body can stay open for hours.
	stream bool

	attempts atomic.Int64
}

func newUpstreamTransport(base http.RoundTripper, route *bastion.Route, protocol observability.Protocol, m *observability.Metrics) *upstreamTransport {
	return &upstreamTransport{base: base, route: route, protocol: protocol, metrics: m}
}

func (t *upstreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attempt := t.attempts.Add(1)

	req, span := telemetry.StartClient(req, spanProxyRequest)
	if span.IsRecording() {
		span.SetAttributes(routeAttributes(t.route, t.protocol)...)
		span.SetAttributes(attribute.Int64("bastion.proxy.attempt", attempt))
	}

	start := time.Now()
	resp, err := t.base.RoundTrip(req)

	status := 0
	if err == nil && resp != nil {
		status = resp.StatusCode
	}

	t.metrics.ObserveUpstream(t.protocol, observability.StatusClass(status, err), time.Since(start))

	return telemetry.EndClient(span, resp, err, t.stream), err
}

// propagateTraceContext writes ctx's trace context into header, replacing
// whatever trace headers the client sent.
func propagateTraceContext(ctx context.Context, header http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}
