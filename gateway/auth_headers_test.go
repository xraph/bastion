package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/xraph/forge"
	forgetesting "github.com/xraph/forge/testing"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/gateway"
	"github.com/xraph/bastion/security"
)

var identityHeaders = []string{"X-Auth-Subject", "X-Auth-Provider", "X-Auth-Scopes", "X-Auth-Role", "X-Auth-Email"}

// The identity headers an upstream sees are the ones the gateway set:
// whatever the client sent under those five names is gone, on routes that
// skip auth, with forwarding off and with auth disabled. Other X-Auth-
// headers pass through.
func TestGateway_UpstreamOnlySeesIdentityHeadersTheGatewaySet(t *testing.T) {
	key := newES256Key(t)

	point, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(marshal(t, map[string]any{"keys": []map[string]any{{
			"kty": "EC", "crv": "P-256", "kid": "k1", "x": b64(point[1:33]), "y": b64(point[33:]),
		}}}))
	}))
	t.Cleanup(jwks.Close)

	// A signed token with a subject and nothing else: no role, email or
	// scope claim for the client's headers to stand in for.
	token := es256Token(t, key, map[string]any{"alg": "ES256", "kid": "k1"}, map[string]any{"sub": "alice"})

	for _, tc := range []struct {
		name    string
		auth    bastion.AuthConfig
		path    string
		bearer  bool
		subject string // the X-Auth-Subject the upstream should see
	}{
		{"verified token", bastion.AuthConfig{Enabled: true, Providers: []string{"jwt"}, ForwardHeaders: true}, "/svc/x", true, "alice"},
		{"route that skips auth", bastion.AuthConfig{Enabled: true, Providers: []string{"jwt"}, ForwardHeaders: true}, "/public/x", false, ""},
		{"forwarding off", bastion.AuthConfig{Enabled: true, Providers: []string{"jwt"}}, "/svc/x", true, ""},
		{"default policy allow", bastion.AuthConfig{Enabled: true, DefaultPolicy: "allow", ForwardHeaders: true}, "/svc/x", false, ""},
		{"auth disabled", bastion.AuthConfig{}, "/svc/x", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				seen http.Header
			)

			up := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					return
				}

				mu.Lock()
				seen = r.Header.Clone()
				mu.Unlock()
			}))
			t.Cleanup(up.Close)

			app := forgetesting.NewTestApp("gateway-auth-headers", "0.0.0")
			gw := gateway.New(
				bastion.WithDiscoveryEnabled(false),
				bastion.WithAuth(tc.auth),
				bastion.WithRoute(bastion.RouteConfig{Path: "/svc/*", Enabled: true, Targets: []bastion.TargetConfig{{URL: up.URL}}}),
				bastion.WithRoute(bastion.RouteConfig{
					Path: "/public/*", Enabled: true, Targets: []bastion.TargetConfig{{URL: up.URL}},
					Auth: &bastion.RouteAuthConfig{SkipAuth: true},
				}),
			)

			if err := app.RegisterExtension(gw); err != nil {
				t.Fatal(err)
			}

			if err := app.RegisterHookFn(forge.PhaseAfterRegister, "bastion-jwt", func(context.Context, forge.App) error {
				gw.Auth().RegisterProvider(security.NewJWTAuthProvider(security.JWTAuthConfig{Name: "jwt", JWKSURL: jwks.URL}))

				return nil
			}); err != nil {
				t.Fatal(err)
			}

			if err := app.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = app.Stop(context.Background()) })

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("X-Auth-Subject", "root")
			req.Header.Set("X-Auth-Provider", "spoofed")
			req.Header.Set("X-Auth-Scopes", "admin")
			req.Header.Set("X-Auth-Role", "admin")
			req.Header.Set("X-Auth-Email", "root@example.com")
			req.Header.Set("X-Auth-Token", "client-token")

			if tc.bearer {
				req.Header.Set("Authorization", "Bearer "+token)
			}

			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
			}

			mu.Lock()
			h := seen
			mu.Unlock()

			if h == nil {
				t.Fatal("the request never reached the upstream")
			}

			want := map[string]string{"X-Auth-Subject": tc.subject}
			if tc.subject != "" {
				want["X-Auth-Provider"] = "jwt"
			}

			for _, name := range identityHeaders {
				if got := h.Get(name); got != want[name] {
					t.Errorf("%s = %q, want %q", name, got, want[name])
				}
			}

			if got := h.Get("X-Auth-Token"); got != "client-token" {
				t.Errorf("X-Auth-Token = %q, want it passed through", got)
			}
		})
	}
}
