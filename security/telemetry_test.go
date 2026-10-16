package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/xraph/bastion/internal/telemetry"
	"github.com/xraph/bastion/internal/telemetry/telemetrytest"
)

const email = "alice@example.com"

type testRoute struct{ auth *RouteAuthConfig }

func (r testRoute) GetID() string             { return "r" }
func (r testRoute) GetAuth() *RouteAuthConfig { return r.auth }

func TestAuth_SpanWrapsTheForwardAuthCall(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	var traceparent string

	authSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("Traceparent")

		if r.Header.Get("Authorization") != "Bearer good-"+email {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.Header().Set("X-Auth-Subject", email)
	}))
	t.Cleanup(authSvc.Close)

	ga := NewAuth(AuthConfig{Enabled: true, Providers: []string{"fwd"}}, forge.NewNoopLogger())
	ga.RegisterProvider(NewForwardAuthProvider("fwd", authSvc.URL, nil))

	for _, tc := range []struct {
		token  string
		result string
	}{{"good-" + email, "ok"}, {"bad-" + email, "denied"}} {
		rec.Reset()

		ctx, server := telemetry.Start(context.Background(), "GET /*path", trace.WithSpanKind(trace.SpanKindServer))

		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/svc/users/"+email+"?email="+email, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)

		authCtx, err := ga.Authenticate(req, testRoute{})
		server.End()

		if (err == nil) != (tc.result == "ok") {
			t.Fatalf("%s: err = %v", tc.result, err)
		}

		span := telemetrytest.One(t, rec, "authenticate request")
		call := telemetrytest.One(t, rec, "call forward auth")

		if span.Parent().SpanID() != server.SpanContext().SpanID() {
			t.Error("authenticate request is not under the server span")
		}

		if call.Parent().SpanID() != span.SpanContext().SpanID() || call.SpanKind() != trace.SpanKindClient {
			t.Error("the forward-auth call is not a client span under authenticate request")
		}

		if !strings.Contains(traceparent, call.SpanContext().SpanID().String()) {
			t.Errorf("auth service got traceparent %q", traceparent)
		}

		if v, _ := telemetrytest.Attr(span, "bastion.auth.result"); v.AsString() != tc.result {
			t.Errorf("bastion.auth.result = %q, want %q", v.AsString(), tc.result)
		}

		if span.Status().Code != codes.Unset {
			t.Errorf("a %s outcome set status %v", tc.result, span.Status())
		}

		if tc.result == "ok" {
			if v, _ := telemetrytest.Attr(span, "bastion.auth.provider"); v.AsString() != "fwd" {
				t.Errorf("bastion.auth.provider = %q", v.AsString())
			}

			if authCtx.Subject != email {
				t.Errorf("subject = %q", authCtx.Subject)
			}
		}

		telemetrytest.NeverRecorded(t, rec, email)
	}
}

func TestAuth_NoSpanWhenTheRouteSkipsAuth(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	ga := NewAuth(AuthConfig{Enabled: true, Providers: []string{"fwd"}}, forge.NewNoopLogger())

	if _, err := ga.Authenticate(httptest.NewRequest(http.MethodGet, "/", nil), testRoute{auth: &RouteAuthConfig{SkipAuth: true}}); err != nil {
		t.Fatal(err)
	}

	if len(rec.Ended()) != 0 {
		t.Errorf("a route that skips auth started %d spans", len(rec.Ended()))
	}
}

func TestJWKS_RefreshSpans(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	status := http.StatusOK
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"keys":[{"kty":"EC","kid":"k1","crv":"P-256",` +
			`"x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU","y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"}]}`))
	}))
	t.Cleanup(jwks.Close)

	p := NewJWTAuthProvider(JWTAuthConfig{Name: "jwt", JWKSURL: jwks.URL})

	ctx, caller := telemetry.Start(context.Background(), "start bastion")
	p.refreshKeysTraced(ctx, true)
	caller.End()

	span := telemetrytest.One(t, rec, "refresh jwks")

	if span.Parent().IsValid() {
		t.Error("a background JWKS refresh hangs off the caller's span")
	}

	if v, _ := telemetrytest.Attr(span, "bastion.jwks.keys"); v.AsInt64() != 1 {
		t.Errorf("bastion.jwks.keys = %v", v.String())
	}

	fetch := telemetrytest.One(t, rec, "fetch jwks")
	if fetch.Parent().SpanID() != span.SpanContext().SpanID() {
		t.Error("the JWKS fetch is not under the refresh span")
	}

	rec.Reset()

	status = http.StatusInternalServerError

	p.refreshKeysTraced(context.Background(), false)

	if s := telemetrytest.One(t, rec, "refresh jwks").Status(); s.Code != codes.Error || s.Description != "unexpected status" {
		t.Errorf("failed refresh status = %+v", s)
	}
}
