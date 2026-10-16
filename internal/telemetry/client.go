package telemetry

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// knownMethods are the methods recorded as sent. Anything else is recorded
// as "_OTHER", as semconv asks, so a client can't invent span values.
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
	http.MethodConnect: true, http.MethodOptions: true, http.MethodTrace: true,
}

// StartClient starts a Client span named name as a child of req's context
// and returns a clone of req that carries the span and its trace headers.
// Pair it with EndClient.
//
// The span records the method, the scheme and the upstream's host and
// port. It leaves out the URL's path and query and every header: for a
// gateway those are the client's request, and they carry IDs, emails and
// tokens. That is also why bastion doesn't use otelhttp, which records the
// full URL and copies transport error text into the span status.
//
// When the span isn't part of any trace (tracing off and no incoming trace
// context), req is returned unchanged.
func StartClient(req *http.Request, name string, opts ...trace.SpanStartOption) (*http.Request, trace.Span) {
	opts = append(opts, trace.WithSpanKind(trace.SpanKindClient))

	ctx, span := Tracer().Start(req.Context(), name, opts...)
	if !span.SpanContext().IsValid() {
		return req, span
	}

	if span.IsRecording() {
		span.SetAttributes(RequestAttributes(req)...)
	}

	out := req.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(out.Header))

	return out, span
}

// RequestAttributes returns the attributes a client span records for req.
func RequestAttributes(req *http.Request) []attribute.KeyValue {
	return URLAttributes(req.Method, req.URL)
}

// URLAttributes returns the attributes a client span records for a request
// with method to u: the method, the scheme, and u's host and port. u's path
// and query are left out.
func URLAttributes(method string, u *url.URL) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 4)

	if knownMethods[method] {
		attrs = append(attrs, attribute.String("http.request.method", method))
	} else {
		attrs = append(attrs, attribute.String("http.request.method", "_OTHER"))
	}

	if u == nil {
		return attrs
	}

	if u.Scheme != "" {
		attrs = append(attrs, attribute.String("url.scheme", u.Scheme))
	}

	host, port := u.Hostname(), u.Port()
	if host != "" {
		attrs = append(attrs, attribute.String("server.address", host))
	}

	if p, err := strconv.Atoi(port); err == nil {
		attrs = append(attrs, attribute.Int("server.port", p))
	}

	return attrs
}

// EndClient records how the round trip ended on span, the span StartClient
// returned. A failed round trip, a stream (stream true) and a response
// with no body to read end the span now. Otherwise resp comes back with
// its body wrapped, and the span ends once the body is read to the end or
// closed, so it covers the whole response.
//
// A 5xx marks the span failed. A 4xx doesn't: it is the upstream's answer
// to the client, which the gateway passes on. A client that went away
// first is not a failure either; the span gets bastion.canceled instead.
func EndClient(span trace.Span, resp *http.Response, err error, stream bool) *http.Response {
	if err != nil {
		RecordRoundTripError(span, err)
		span.End()

		return resp
	}

	if resp == nil {
		span.End()

		return resp
	}

	RecordStatus(span, resp.StatusCode)

	if stream || resp.StatusCode == http.StatusSwitchingProtocols || resp.Body == nil || resp.Body == http.NoBody {
		recordGRPCStatus(span, resp)
		span.End()

		return resp
	}

	resp.Body = &spanBody{ReadCloser: resp.Body, span: span, resp: resp}

	return resp
}

// RecordRoundTripError records a round trip that got no response. A client
// that went away first only sets bastion.canceled; anything else sets
// error.type to the error's Go type and marks the span failed.
func RecordRoundTripError(span trace.Span, err error) {
	if errors.Is(err, context.Canceled) {
		span.SetAttributes(attribute.Bool("bastion.canceled", true))

		return
	}

	span.SetAttributes(attribute.String("error.type", ErrorType(err)))
	Fail(span, failureClass(err), err)
}

// RecordStatus records the response status on a client span. A 5xx marks
// the span failed; anything below is the upstream's answer.
func RecordStatus(span trace.Span, status int) {
	span.SetAttributes(attribute.Int("http.response.status_code", status))

	if status >= http.StatusInternalServerError {
		code := strconv.Itoa(status)
		span.SetAttributes(attribute.String("error.type", code))
		Fail(span, code, nil)
	}
}

// failureClass is the span status description for a failed round trip.
func failureClass(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout"
	}

	return "transport error"
}

// spanBody ends the client span when the response body is done with.
type spanBody struct {
	io.ReadCloser

	span trace.Span
	resp *http.Response
	once sync.Once
}

func (b *spanBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)

	switch {
	case err == nil:
	case errors.Is(err, io.EOF):
		b.end(nil)
	default:
		b.end(err)
	}

	return n, err
}

func (b *spanBody) Close() error {
	err := b.ReadCloser.Close()
	b.end(nil)

	return err
}

func (b *spanBody) end(readErr error) {
	b.once.Do(func() {
		if readErr != nil && !errors.Is(readErr, context.Canceled) {
			Fail(b.span, "response body error", readErr)
		}

		recordGRPCStatus(b.span, b.resp)
		b.span.End()
	})
}

// grpcServerFaults are the gRPC status codes that mark a call failed:
// UNKNOWN, DEADLINE_EXCEEDED, UNIMPLEMENTED, INTERNAL, UNAVAILABLE and
// DATA_LOSS. The rest (NOT_FOUND, PERMISSION_DENIED and so on) are the
// server's answer, passed on like an HTTP 4xx.
var grpcServerFaults = map[int]bool{2: true, 4: true, 12: true, 13: true, 14: true, 15: true}

// recordGRPCStatus records the grpc-status of a gRPC response. It is in the
// trailers once the body has been read, or in the headers of a
// trailers-only response.
func recordGRPCStatus(span trace.Span, resp *http.Response) {
	if resp == nil || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/grpc") {
		return
	}

	raw := resp.Trailer.Get("Grpc-Status")
	if raw == "" {
		raw = resp.Header.Get("Grpc-Status")
	}

	code, err := strconv.Atoi(raw)
	if err != nil {
		return
	}

	span.SetAttributes(
		attribute.String("rpc.system", "grpc"),
		attribute.Int("rpc.grpc.status_code", code),
	)

	if grpcServerFaults[code] {
		Fail(span, "grpc status "+raw, nil)
	}
}

// Transport gives every request a Client span named Name and sends the
// trace context with it. Bastion's background fetches (FARP manifests,
// OpenAPI and AsyncAPI specs, JWKS, forward auth) go through it.
type Transport struct {
	// Base sends the request. Nil means http.DefaultTransport.
	Base http.RoundTripper

	// Name is the span name, such as "fetch farp manifest".
	Name string
}

// NewTransport wraps base, nil meaning http.DefaultTransport, in a
// Transport whose spans are called name.
func NewTransport(base http.RoundTripper, name string) *Transport {
	return &Transport{Base: base, Name: name}
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	req, span := StartClient(req, t.Name)

	resp, err := base.RoundTrip(req)

	return EndClient(span, resp, err, false), err
}

// CloseIdleConnections closes the base transport's idle connections, so
// http.Client.CloseIdleConnections still reaches it.
func (t *Transport) CloseIdleConnections() {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	if c, ok := base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
