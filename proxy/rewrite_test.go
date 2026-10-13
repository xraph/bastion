package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

func rewriteEngine(t *testing.T, route *bastion.Route) *proxy.Engine {
	t.Helper()

	logger := forge.NewNoopLogger()
	rm := routing.NewManager()

	if err := rm.AddRoute(route); err != nil {
		t.Fatal(err)
	}

	return proxy.NewEngine(bastion.Config{}, logger, rm, health.NewMonitor(health.Config{}, logger),
		resilience.NewCBManager(bastion.CircuitBreakerConfig{}), bastion.NewRateLimiter(bastion.RateLimitConfig{}),
		proxy.NewStatsCollector(), bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))
}

// The upstream sees the path after strip_prefix, then add_prefix, then
// rewrite_path.
func TestEngine_AppliesRewritePath(t *testing.T) {
	cases := []struct {
		name    string
		route   bastion.Route
		request string
		header  http.Header
		want    string
	}{
		{
			name:    "rewrite alone",
			route:   bastion.Route{Path: "/api/*", RewritePath: "^/api/v1/(.*):/v2/$1"},
			request: "/api/v1/users?page=2",
			want:    "/v2/users?page=2",
		},
		{
			// The pattern only matches once add_prefix has run, so this
			// passes only in the documented order.
			name: "strip, then add, then rewrite",
			route: bastion.Route{
				Path: "/svc/*", StripPrefix: true, AddPrefix: "/internal",
				RewritePath: "^/internal/old/(.*):/internal/new/$1",
			},
			request: "/svc/old/x",
			want:    "/internal/new/x",
		},
		{
			name:    "invalid rule leaves the path alone",
			route:   bastion.Route{Path: "/api/*", RewritePath: "([:/x"},
			request: "/api/users",
			want:    "/api/users",
		},
		{
			name:    "SSE requests are rewritten too",
			route:   bastion.Route{Path: "/events/*", StripPrefix: true, RewritePath: "^/(.*):/stream/$1"},
			request: "/events/orders",
			header:  http.Header{"Accept": {"text/event-stream"}},
			want:    "/stream/orders",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := newRecorder(t)

			route := tc.route
			route.ID = "r"
			route.Enabled = true
			route.Protocol = bastion.ProtocolHTTP
			route.Source = bastion.SourceManual
			route.Targets = []*bastion.Target{{ID: "t", URL: up.srv.URL, Weight: 1, Healthy: true}}

			engine := rewriteEngine(t, &route)

			req := httptest.NewRequest(http.MethodGet, tc.request, nil)
			for k, v := range tc.header {
				req.Header[k] = v
			}

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			if got := up.got(); got != tc.want {
				t.Errorf("upstream got %q, want %q", got, tc.want)
			}
		})
	}
}
