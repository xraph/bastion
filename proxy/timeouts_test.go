package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

const upstreamDelay = 400 * time.Millisecond

// slowUpstream waits upstreamDelay before it answers, or until the gateway
// gives up on it.
func slowUpstream(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		select {
		case <-time.After(upstreamDelay):
			_, _ = io.WriteString(w, "slow answer")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func timeoutEngine(t *testing.T, cfg bastion.Config, upstreamURL string, timeout *bastion.TimeoutConfig) *proxy.Engine {
	t.Helper()

	logger := forge.NewNoopLogger()
	rm := routing.NewManager()

	if err := rm.AddRoute(&bastion.Route{
		ID:       "slow",
		Path:     "/slow/*",
		Targets:  []*bastion.Target{{ID: "t", URL: upstreamURL, Weight: 1, Healthy: true}},
		Protocol: bastion.ProtocolHTTP,
		Source:   bastion.SourceManual,
		Enabled:  true,
		Timeout:  timeout,
	}); err != nil {
		t.Fatal(err)
	}

	return proxy.NewEngine(cfg, logger, rm, health.NewMonitor(health.Config{}, logger),
		resilience.NewCBManager(bastion.CircuitBreakerConfig{}), bastion.NewRateLimiter(bastion.RateLimitConfig{}),
		proxy.NewStatsCollector(), bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))
}

func serve(engine *proxy.Engine, req *http.Request) (*httptest.ResponseRecorder, time.Duration) {
	rec := httptest.NewRecorder()
	start := time.Now()
	engine.ServeHTTP(rec, req)

	return rec, time.Since(start)
}

func wantCutOff(t *testing.T, rec *httptest.ResponseRecorder, took time.Duration) {
	t.Helper()

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
	}
	if took >= upstreamDelay {
		t.Errorf("took %v, want the gateway to give up before the upstream's %v", took, upstreamDelay)
	}
}

func wantSlowAnswer(t *testing.T, rec *httptest.ResponseRecorder, took time.Duration) {
	t.Helper()

	if rec.Code != http.StatusOK || rec.Body.String() != "slow answer" {
		t.Errorf("got %d %q, want 200 %q", rec.Code, rec.Body.String(), "slow answer")
	}
	if took < upstreamDelay {
		t.Errorf("took %v, less than the upstream's %v", took, upstreamDelay)
	}
}

// A route's read timeout cuts off an upstream that is slow to answer, even
// though the gateway as a whole sets none.
func TestEngine_RouteReadTimeoutCutsOffSlowUpstream(t *testing.T) {
	up := slowUpstream(t)
	engine := timeoutEngine(t, bastion.Config{}, up.URL, &bastion.TimeoutConfig{Read: 50 * time.Millisecond})

	rec, took := serve(engine, httptest.NewRequest(http.MethodGet, "/slow/x", nil))
	wantCutOff(t, rec, took)
}

// A route without a timeout block behaves as it did before route timeouts
// were read: it waits as long as the gateway-wide timeouts allow.
func TestEngine_RouteWithoutTimeoutKeepsGatewayBehaviour(t *testing.T) {
	up := slowUpstream(t)

	t.Run("no gateway read timeout waits for the upstream", func(t *testing.T) {
		engine := timeoutEngine(t, bastion.Config{}, up.URL, nil)

		rec, took := serve(engine, httptest.NewRequest(http.MethodGet, "/slow/x", nil))
		wantSlowAnswer(t, rec, took)
	})

	t.Run("gateway read timeout still applies", func(t *testing.T) {
		cfg := bastion.Config{Timeouts: bastion.TimeoutConfig{Read: 50 * time.Millisecond}}
		engine := timeoutEngine(t, cfg, up.URL, nil)

		rec, took := serve(engine, httptest.NewRequest(http.MethodGet, "/slow/x", nil))
		wantCutOff(t, rec, took)
	})
}

// A field the route leaves at zero falls back to the gateway-wide value,
// and a route value beats the gateway-wide one in both directions.
func TestEngine_RouteTimeoutFallsBackPerField(t *testing.T) {
	up := slowUpstream(t)
	gatewayRead := bastion.Config{Timeouts: bastion.TimeoutConfig{Read: 50 * time.Millisecond}}

	t.Run("route without read takes the gateway read", func(t *testing.T) {
		engine := timeoutEngine(t, gatewayRead, up.URL, &bastion.TimeoutConfig{Connect: time.Second})

		rec, took := serve(engine, httptest.NewRequest(http.MethodGet, "/slow/x", nil))
		wantCutOff(t, rec, took)
	})

	t.Run("route read longer than the gateway read wins", func(t *testing.T) {
		engine := timeoutEngine(t, gatewayRead, up.URL, &bastion.TimeoutConfig{Read: 2 * time.Second})

		rec, took := serve(engine, httptest.NewRequest(http.MethodGet, "/slow/x", nil))
		wantSlowAnswer(t, rec, took)
	})
}

// A route's write timeout cuts off a request body that takes too long to
// send, and leaves one that arrives in time alone.
func TestEngine_RouteWriteTimeout(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}))
	t.Cleanup(up.Close)

	t.Run("slow body is cut off", func(t *testing.T) {
		engine := timeoutEngine(t, bastion.Config{}, up.URL, &bastion.TimeoutConfig{Write: 50 * time.Millisecond})

		pr, pw := io.Pipe()
		go func() {
			_, _ = io.WriteString(pw, "first part ")
			time.Sleep(upstreamDelay)
			_, _ = io.WriteString(pw, "too late")
			_ = pw.Close()
		}()

		req := httptest.NewRequest(http.MethodPost, "/slow/x", pr)
		req.ContentLength = -1

		rec, took := serve(engine, req)
		wantCutOff(t, rec, took)
	})

	t.Run("body sent in time goes through", func(t *testing.T) {
		engine := timeoutEngine(t, bastion.Config{}, up.URL, &bastion.TimeoutConfig{Write: time.Second})

		rec, _ := serve(engine, httptest.NewRequest(http.MethodPost, "/slow/x", strings.NewReader("hello")))
		if rec.Code != http.StatusOK || rec.Body.String() != "hello" {
			t.Errorf("got %d %q, want 200 %q", rec.Code, rec.Body.String(), "hello")
		}
	})
}
