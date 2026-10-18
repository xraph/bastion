package telemetry_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/xraph/bastion/internal/telemetry"
	"github.com/xraph/bastion/internal/telemetry/telemetrytest"
)

const (
	email = "alice@example.com"
	token = "s3cr3t-t0ken"
)

// upstream answers with status and records the traceparent it was sent.
func upstream(t *testing.T, status int) (*httptest.Server, *string) {
	t.Helper()

	var got string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Traceparent")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "body")
	}))
	t.Cleanup(srv.Close)

	return srv, &got
}

func get(t *testing.T, ctx context.Context, client *http.Client, url string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return resp
}

func TestTransport_ClientSpanAndPropagation(t *testing.T) {
	rec := telemetrytest.Recorder(t)
	srv, traceparent := upstream(t, http.StatusOK)

	ctx, parent := telemetry.Start(context.Background(), "parent")
	client := &http.Client{Transport: telemetry.NewTransport(nil, "fetch thing")}

	get(t, ctx, client, srv.URL+"/users/x?email="+email+"&token="+token)
	parent.End()

	span := telemetrytest.One(t, rec, "fetch thing")

	if span.SpanKind() != trace.SpanKindClient {
		t.Errorf("kind = %v, want client", span.SpanKind())
	}

	if span.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("the client span is not a child of the caller's span")
	}

	if span.InstrumentationScope().Name != telemetry.ScopeName {
		t.Errorf("scope = %q", span.InstrumentationScope().Name)
	}

	want := "00-" + span.SpanContext().TraceID().String() + "-" + span.SpanContext().SpanID().String() + "-01"
	if *traceparent != want {
		t.Errorf("upstream got traceparent %q, want %q", *traceparent, want)
	}

	for key, value := range map[string]string{
		"http.request.method":       "GET",
		"url.scheme":                "http",
		"server.address":            "127.0.0.1",
		"http.response.status_code": "200",
	} {
		got, ok := telemetrytest.Attr(span, key)
		if !ok || got.String() != value {
			t.Errorf("%s = %q (present %v), want %q", key, got.String(), ok, value)
		}
	}

	if _, ok := telemetrytest.Attr(span, "server.port"); !ok {
		t.Error("server.port missing")
	}

	for _, key := range []string{"url.full", "url.path", "url.query"} {
		if _, ok := telemetrytest.Attr(span, key); ok {
			t.Errorf("%s is recorded", key)
		}
	}

	if span.Status().Code != codes.Unset {
		t.Errorf("status = %v", span.Status())
	}

	telemetrytest.NeverRecorded(t, rec, email)
	telemetrytest.NeverRecorded(t, rec, token)
	telemetrytest.NeverRecorded(t, rec, "/users/x")
}

func TestTransport_ServerErrorFailsTheSpanClientErrorDoesNot(t *testing.T) {
	rec := telemetrytest.Recorder(t)
	bad, _ := upstream(t, http.StatusServiceUnavailable)
	missing, _ := upstream(t, http.StatusNotFound)

	client := &http.Client{Transport: telemetry.NewTransport(nil, "fetch thing")}

	get(t, context.Background(), client, bad.URL)
	get(t, context.Background(), client, missing.URL)

	spans := telemetrytest.Named(rec, "fetch thing")
	if len(spans) != 2 {
		t.Fatalf("got %d spans", len(spans))
	}

	if s := spans[0].Status(); s.Code != codes.Error || s.Description != "503" {
		t.Errorf("503 status = %+v", s)
	}

	if v, _ := telemetrytest.Attr(spans[0], "error.type"); v.AsString() != "503" {
		t.Errorf("503 error.type = %q", v.AsString())
	}

	if s := spans[1].Status(); s.Code != codes.Unset {
		t.Errorf("404 status = %+v", s)
	}
}

func TestTransport_TransportErrorRecordsTypeNotText(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	// A port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := ln.Addr().String()
	_ = ln.Close()

	client := &http.Client{Transport: telemetry.NewTransport(nil, "fetch thing")}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/?email="+email, nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected a dial error")
	}

	span := telemetrytest.One(t, rec, "fetch thing")

	if s := span.Status(); s.Code != codes.Error || s.Description != "transport error" {
		t.Errorf("status = %+v", s)
	}

	events := span.Events()
	if len(events) != 1 || events[0].Name != "exception" {
		t.Fatalf("events = %+v", events)
	}

	if len(events[0].Attributes) != 1 || events[0].Attributes[0].Key != "exception.type" {
		t.Errorf("exception event carries %v, want exception.type only", events[0].Attributes)
	}

	telemetrytest.NeverRecorded(t, rec, addr)
	telemetrytest.NeverRecorded(t, rec, email)
	telemetrytest.NeverRecorded(t, rec, "refused")
}

func TestTransport_CanceledIsNotAFailure(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) })

	ctx, cancel := context.WithCancel(context.Background())
	transport := telemetry.NewTransport(nil, "fetch thing")

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)

	done := make(chan error, 1)

	go func() {
		_, err := transport.RoundTrip(req)
		done <- err
	}()

	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}

	span := telemetrytest.One(t, rec, "fetch thing")

	if span.Status().Code != codes.Unset {
		t.Errorf("status = %+v", span.Status())
	}

	if v, ok := telemetrytest.Attr(span, "bastion.canceled"); !ok || !v.AsBool() {
		t.Error("bastion.canceled not set")
	}
}

func TestEndClient_SpanCoversTheBodyUnlessStreaming(t *testing.T) {
	rec := telemetrytest.Recorder(t)
	srv, _ := upstream(t, http.StatusOK)

	for _, stream := range []bool{false, true} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		req, span := telemetry.StartClient(req, "fetch thing")

		resp, err := http.DefaultTransport.RoundTrip(req)
		resp = telemetry.EndClient(span, resp, err, stream)

		endedBeforeBody := len(telemetrytest.Named(rec, "fetch thing"))

		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if stream && endedBeforeBody != 1 {
			t.Error("a stream's span did not end at the headers")
		}

		if !stream && endedBeforeBody != 0 {
			t.Error("the span ended before the body was read")
		}

		rec.Reset()
	}
}

func TestEndClient_GRPCStatus(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	status := "14"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0, 0, 0, 0, 0})
		w.Header().Set("Grpc-Status", status)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: telemetry.NewTransport(nil, "call grpc")}

	for _, tc := range []struct {
		status string
		failed bool
	}{{"14", true}, {"5", false}, {"0", false}} {
		status = tc.status

		get(t, context.Background(), client, srv.URL)

		span := telemetrytest.One(t, rec, "call grpc")

		if v, _ := telemetrytest.Attr(span, "rpc.grpc.status_code"); v.String() != tc.status {
			t.Errorf("rpc.grpc.status_code = %q, want %s", v.String(), tc.status)
		}

		if failed := span.Status().Code == codes.Error; failed != tc.failed {
			t.Errorf("grpc %s: failed = %v, want %v", tc.status, failed, tc.failed)
		}

		rec.Reset()
	}
}

func TestStartClient_NoTraceLeavesTheRequestAlone(t *testing.T) {
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	otel.SetTracerProvider(noop.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})

	req := httptest.NewRequest(http.MethodGet, "http://upstream/x", nil)
	out, span := telemetry.StartClient(req, "fetch thing")
	span.End()

	if out != req {
		t.Error("the request was cloned with no trace to carry")
	}

	if got := out.Header.Get("Traceparent"); got != "" {
		t.Errorf("traceparent %q injected with no trace", got)
	}
}

func TestErrorType(t *testing.T) {
	_, err := net.Dial("tcp", "127.0.0.1:1")
	if err == nil {
		t.Skip("something listens on port 1")
	}

	got := telemetry.ErrorType(err)
	if strings.Contains(got, "127.0.0.1") || got == "" {
		t.Errorf("ErrorType = %q", got)
	}

	if got := telemetry.ErrorType(context.Canceled); got != "*errors.errorString" {
		t.Errorf("ErrorType(context.Canceled) = %q", got)
	}
}
