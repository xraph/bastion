package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

// recorder is an upstream that remembers the request URI it was sent.
type recorder struct {
	mu  sync.Mutex
	uri string
	srv *httptest.Server
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()

	rec := &recorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.uri = r.URL.RequestURI()
		rec.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rec.srv.Close)

	return rec
}

func (r *recorder) got() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.uri
}

// Requests go through a real Forge router. With the BunRouter adapter
// Forge trims the trailing slash from r.URL.Path before the catch-all
// hands the request to the engine; the default forgemux adapter leaves the
// path alone. The upstream has to see the client's path either way.
func TestEngine_ForwardsTrailingSlashThroughForgeRouter(t *testing.T) {
	adapters := map[string]func() forge.RouterAdapter{
		"bunrouter": forge.NewBunRouterAdapter,
		"forgemux":  func() forge.RouterAdapter { return forge.NewForgeMuxAdapter() },
	}

	for name, adapter := range adapters {
		t.Run(name, func(t *testing.T) {
			testForwardsTrailingSlash(t, adapter)
		})
	}
}

func testForwardsTrailingSlash(t *testing.T, adapter func() forge.RouterAdapter) {
	t.Helper()

	cases := []struct {
		name        string
		routePath   string
		stripPrefix bool
		request     string
		want        string
	}{
		{name: "trailing slash", routePath: "/docs/*", request: "/docs/", want: "/docs/"},
		{name: "trailing slash and query", routePath: "/docs/*", request: "/docs/?q=1", want: "/docs/?q=1"},
		{name: "nested trailing slash", routePath: "/docs/*", request: "/docs/guide/", want: "/docs/guide/"},
		{name: "no trailing slash", routePath: "/docs/*", request: "/docs/guide", want: "/docs/guide"},
		{name: "strip prefix keeps the slash", routePath: "/api/*", stripPrefix: true, request: "/api/users/", want: "/users/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := newRecorder(t)
			logger := forge.NewNoopLogger()
			rm := routing.NewManager()

			if err := rm.AddRoute(&bastion.Route{
				ID:          "r",
				Path:        tc.routePath,
				StripPrefix: tc.stripPrefix,
				Targets:     []*bastion.Target{{ID: "t", URL: up.srv.URL, Weight: 1, Healthy: true}},
				Protocol:    bastion.ProtocolHTTP,
				Source:      bastion.SourceManual,
				Enabled:     true,
			}); err != nil {
				t.Fatal(err)
			}

			engine := proxy.NewEngine(bastion.Config{}, logger, rm, health.NewMonitor(health.Config{}, logger),
				resilience.NewCBManager(bastion.CircuitBreakerConfig{}), bastion.NewRateLimiter(bastion.RateLimitConfig{}),
				proxy.NewStatsCollector(), bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))

			router := forge.NewRouter(forge.WithAdapter(adapter()))

			if err := router.GET("/*path", func(ctx forge.Context) error {
				engine.ServeHTTP(ctx.Response(), ctx.Request())
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.request, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}

			if got := up.got(); got != tc.want {
				t.Errorf("upstream got %q, want %q", got, tc.want)
			}
		})
	}
}
