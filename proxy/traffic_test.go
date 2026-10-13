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

	"github.com/xraph/forge"
	forgetesting "github.com/xraph/forge/testing"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/gateway"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

// named is an upstream that answers with its own name.
func named(t *testing.T, name string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, name)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func trafficEngine(t *testing.T, route *bastion.Route) *proxy.Engine {
	t.Helper()

	logger := forge.NewNoopLogger()
	rm := routing.NewManager()

	route.ID = "r"
	route.Path = "/svc/*"
	route.Enabled = true
	route.Protocol = bastion.ProtocolHTTP
	route.Source = bastion.SourceManual

	if err := rm.AddRoute(route); err != nil {
		t.Fatal(err)
	}

	engine := proxy.NewEngine(bastion.Config{}, logger, rm, health.NewMonitor(health.Config{}, logger),
		resilience.NewCBManager(bastion.CircuitBreakerConfig{}), bastion.NewRateLimiter(bastion.RateLimitConfig{}),
		proxy.NewStatsCollector(), bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))
	engine.SetTrafficRouter(routing.NewTrafficSplitter(true))

	return engine
}

func blueGreen(t *testing.T) []*bastion.Target {
	t.Helper()

	return []*bastion.Target{
		{ID: "blue", URL: named(t, "blue").URL, Weight: 1, Healthy: true, Tags: []string{"blue"}},
		{ID: "green", URL: named(t, "green").URL, Weight: 1, Healthy: true, Tags: []string{"green"}},
	}
}

func answers(t *testing.T, engine *proxy.Engine, n int, header http.Header) map[string]int {
	t.Helper()

	seen := map[string]int{}

	for range n {
		req := httptest.NewRequest(http.MethodGet, "/svc/x", nil)
		for k, v := range header {
			req.Header[k] = v
		}

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		seen[rec.Body.String()]++
	}

	return seen
}

func TestEngine_TrafficPolicyFiltersTargets(t *testing.T) {
	t.Run("blue-green sends everything to the active colour", func(t *testing.T) {
		engine := trafficEngine(t, &bastion.Route{
			Targets: blueGreen(t),
			TrafficPolicy: &bastion.TrafficPolicy{
				Type:  bastion.TrafficBlueGreen,
				Rules: []bastion.TrafficRule{{TargetTags: []string{"green"}}},
			},
		})

		if seen := answers(t, engine, 10, nil); seen["green"] != 10 {
			t.Errorf("answers = %v, want all 10 from green", seen)
		}
	})

	t.Run("A/B test routes a matching header to its group", func(t *testing.T) {
		engine := trafficEngine(t, &bastion.Route{
			Targets: blueGreen(t),
			TrafficPolicy: &bastion.TrafficPolicy{
				Type: bastion.TrafficABTest,
				Rules: []bastion.TrafficRule{{
					Match:      bastion.TrafficMatch{Type: bastion.MatchHeader, Key: "X-Group", Value: "beta"},
					TargetTags: []string{"green"},
				}},
			},
		})

		if seen := answers(t, engine, 6, http.Header{"X-Group": {"beta"}}); seen["green"] != 6 {
			t.Errorf("beta answers = %v, want all 6 from green", seen)
		}
		if seen := answers(t, engine, 6, nil); seen["blue"] == 0 || seen["green"] == 0 {
			t.Errorf("unmatched answers = %v, want both targets", seen)
		}
	})

	t.Run("a policy that leaves no target falls back to all of them", func(t *testing.T) {
		engine := trafficEngine(t, &bastion.Route{
			Targets: blueGreen(t),
			TrafficPolicy: &bastion.TrafficPolicy{
				Type:  bastion.TrafficBlueGreen,
				Rules: []bastion.TrafficRule{{TargetTags: []string{"purple"}}},
			},
		})

		if seen := answers(t, engine, 4, nil); seen["blue"] == 0 || seen["green"] == 0 {
			t.Errorf("answers = %v, want both targets", seen)
		}
	})
}

// shadow records the requests it is sent, slowly.
type shadow struct {
	mu   sync.Mutex
	got  []string
	seen chan struct{}
	srv  *httptest.Server
}

func newShadow(t *testing.T, delay time.Duration) *shadow {
	t.Helper()

	s := &shadow{seen: make(chan struct{}, 8)}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		time.Sleep(delay)

		s.mu.Lock()
		s.got = append(s.got, r.Method+" "+r.URL.RequestURI()+" "+string(b)+" "+r.Header.Get("X-Test"))
		s.mu.Unlock()

		w.WriteHeader(http.StatusTeapot)
		s.seen <- struct{}{}
	}))
	t.Cleanup(s.srv.Close)

	return s
}

func TestEngine_MirrorsWithoutDelayingTheClient(t *testing.T) {
	sh := newShadow(t, 300*time.Millisecond)
	engine := trafficEngine(t, &bastion.Route{
		Targets:       []*bastion.Target{{ID: "live", URL: named(t, "live").URL, Weight: 1, Healthy: true}},
		StripPrefix:   true,
		TrafficPolicy: &bastion.TrafficPolicy{Type: bastion.TrafficMirror, MirrorTarget: sh.srv.URL},
	})

	req := httptest.NewRequest(http.MethodPost, "/svc/orders?id=7", strings.NewReader("payload"))
	req.Header.Set("X-Test", "copied")

	start := time.Now()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	took := time.Since(start)

	if rec.Code != http.StatusOK || rec.Body.String() != "live" {
		t.Errorf("client got %d %q, want 200 %q from the live target", rec.Code, rec.Body.String(), "live")
	}
	if took >= 300*time.Millisecond {
		t.Errorf("client waited %v, want no wait for the slow mirror", took)
	}

	select {
	case <-sh.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("mirror never received the copy")
	}

	sh.mu.Lock()
	defer sh.mu.Unlock()

	if want := "POST /orders?id=7 payload copied"; len(sh.got) != 1 || sh.got[0] != want {
		t.Errorf("mirror got %q, want [%q]", sh.got, want)
	}
}

func TestEngine_MirrorFailureDoesNotReachTheClient(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	engine := trafficEngine(t, &bastion.Route{
		Targets:       []*bastion.Target{{ID: "live", URL: named(t, "live").URL, Weight: 1, Healthy: true}},
		TrafficPolicy: &bastion.TrafficPolicy{Type: bastion.TrafficMirror, MirrorTarget: deadURL},
	})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/svc/x", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "live" {
		t.Errorf("client got %d %q, want 200 %q", rec.Code, rec.Body.String(), "live")
	}
}

// Through gateway.New, with traffic_split on, a route's policy picks its
// targets and mirrors its traffic.
func TestGateway_AppliesTrafficPolicy(t *testing.T) {
	sh := newShadow(t, 0)
	app := forgetesting.NewTestApp("traffic", "0.0.0")
	gw := gateway.New(
		bastion.WithDiscoveryEnabled(false),
		func(c *bastion.Config) { c.TrafficSplit.Enabled = true },
		bastion.WithRoute(bastion.RouteConfig{
			Path: "/svc/*", Enabled: true,
			Targets: []bastion.TargetConfig{
				{URL: named(t, "blue").URL, Tags: []string{"blue"}},
				{URL: named(t, "green").URL, Tags: []string{"green"}},
			},
			TrafficPolicy: &bastion.TrafficPolicy{
				Type:  bastion.TrafficBlueGreen,
				Rules: []bastion.TrafficRule{{TargetTags: []string{"green"}}},
			},
		}),
		bastion.WithRoute(bastion.RouteConfig{
			Path: "/mirrored/*", Enabled: true,
			Targets:       []bastion.TargetConfig{{URL: named(t, "live").URL}},
			TrafficPolicy: &bastion.TrafficPolicy{Type: bastion.TrafficMirror, MirrorTarget: sh.srv.URL},
		}),
	)

	if err := app.RegisterExtension(gw); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	for range 6 {
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/svc/x", nil))

		if rec.Body.String() != "green" {
			t.Fatalf("answer = %q, want green from the active colour", rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mirrored/x", nil))

	select {
	case <-sh.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("mirror never received the copy")
	}
}
