package proxy_test

import (
	"bytes"
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

// flaky answers the first `fail` requests with status, or by dropping the
// connection when status is 0, and 200 after that. It records the body of
// every request it reads, health probes aside.
type flaky struct {
	mu     sync.Mutex
	fail   int
	status int
	hits   int
	bodies []string
	srv    *httptest.Server
}

func newFlaky(t *testing.T, fail, status int) *flaky {
	t.Helper()

	f := &flaky{fail: fail, status: status}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health probes from the gateway don't count.
		if r.URL.Path == health.DefaultPath {
			return
		}

		b, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.hits++
		f.bodies = append(f.bodies, string(b))
		failing := f.hits <= f.fail
		f.mu.Unlock()

		switch {
		case failing && f.status == 0:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		case failing:
			w.WriteHeader(f.status)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *flaky) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.hits
}

func retryCfg(methods ...string) bastion.RetryConfig {
	return bastion.RetryConfig{
		Enabled:          true,
		MaxAttempts:      3,
		Backoff:          bastion.BackoffFixed,
		InitialDelay:     time.Millisecond,
		MaxDelay:         time.Second,
		RetryableStatus:  []int{502, 503, 504},
		RetryableMethods: methods,
	}
}

func retryEngine(t *testing.T, global bastion.RetryConfig, upstreamURL string, routeRetry *bastion.RetryConfig) *proxy.Engine {
	t.Helper()

	logger := forge.NewNoopLogger()
	rm := routing.NewManager()

	if err := rm.AddRoute(&bastion.Route{
		ID:       "r",
		Path:     "/svc/*",
		Targets:  []*bastion.Target{{ID: "t", URL: upstreamURL, Weight: 1, Healthy: true}},
		Protocol: bastion.ProtocolHTTP,
		Source:   bastion.SourceManual,
		Enabled:  true,
		Retry:    routeRetry,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := bastion.Config{Retry: global}
	engine := proxy.NewEngine(cfg, logger, rm, health.NewMonitor(health.Config{}, logger),
		resilience.NewCBManager(bastion.CircuitBreakerConfig{}), bastion.NewRateLimiter(bastion.RateLimitConfig{}),
		proxy.NewStatsCollector(), bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))
	engine.SetRetryPolicy(resilience.NewRetryExecutor(global))

	return engine
}

func do(engine *proxy.Engine, method, path string, body io.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(method, path, body))

	return rec
}

func TestEngine_RetriesRetryableStatus(t *testing.T) {
	up := newFlaky(t, 2, http.StatusServiceUnavailable)
	engine := retryEngine(t, retryCfg("GET"), up.srv.URL, nil)

	rec := do(engine, http.MethodGet, "/svc/x", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 ok after retries", rec.Code, rec.Body.String())
	}
	if got := up.count(); got != 3 {
		t.Errorf("upstream hit %d times, want 3", got)
	}
}

func TestEngine_RetriesDroppedConnection(t *testing.T) {
	up := newFlaky(t, 2, 0)
	engine := retryEngine(t, retryCfg("GET"), up.srv.URL, nil)

	rec := do(engine, http.MethodGet, "/svc/x", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after retrying dropped connections", rec.Code)
	}
	if got := up.count(); got != 3 {
		t.Errorf("upstream hit %d times, want 3", got)
	}
}

func TestEngine_RetryStopsAtMaxAttempts(t *testing.T) {
	up := newFlaky(t, 100, http.StatusServiceUnavailable)
	engine := retryEngine(t, retryCfg("GET"), up.srv.URL, nil)

	rec := do(engine, http.MethodGet, "/svc/x", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want the last attempt's 503", rec.Code)
	}
	if got := up.count(); got != 3 {
		t.Errorf("upstream hit %d times, want MaxAttempts (3)", got)
	}
}

func TestEngine_RetryOnlyWhatThePolicyAllows(t *testing.T) {
	t.Run("method not retryable", func(t *testing.T) {
		up := newFlaky(t, 1, http.StatusServiceUnavailable)
		engine := retryEngine(t, retryCfg("GET"), up.srv.URL, nil)

		if rec := do(engine, http.MethodPost, "/svc/x", strings.NewReader("x")); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if got := up.count(); got != 1 {
			t.Errorf("upstream hit %d times, want 1", got)
		}
	})

	t.Run("status not retryable", func(t *testing.T) {
		up := newFlaky(t, 1, http.StatusInternalServerError)
		engine := retryEngine(t, retryCfg("GET"), up.srv.URL, nil)

		if rec := do(engine, http.MethodGet, "/svc/x", nil); rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
		if got := up.count(); got != 1 {
			t.Errorf("upstream hit %d times, want 1", got)
		}
	})

	t.Run("retry disabled", func(t *testing.T) {
		up := newFlaky(t, 1, http.StatusServiceUnavailable)
		off := retryCfg("GET")
		off.Enabled = false
		engine := retryEngine(t, off, up.srv.URL, nil)

		do(engine, http.MethodGet, "/svc/x", nil)
		if got := up.count(); got != 1 {
			t.Errorf("upstream hit %d times, want 1", got)
		}
	})
}

// The route's retry block replaces the gateway-wide one.
func TestEngine_RouteRetryOverridesGlobal(t *testing.T) {
	up := newFlaky(t, 1, http.StatusServiceUnavailable)
	off := retryCfg("GET")
	off.Enabled = false
	route := retryCfg("GET")
	engine := retryEngine(t, off, up.srv.URL, &route)

	if rec := do(engine, http.MethodGet, "/svc/x", nil); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 from the route's retry", rec.Code)
	}
	if got := up.count(); got != 2 {
		t.Errorf("upstream hit %d times, want 2", got)
	}
}

func TestEngine_RetryReplaysBody(t *testing.T) {
	up := newFlaky(t, 2, http.StatusServiceUnavailable)
	engine := retryEngine(t, retryCfg("POST"), up.srv.URL, nil)

	if rec := do(engine, http.MethodPost, "/svc/x", strings.NewReader("hello")); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	up.mu.Lock()
	defer up.mu.Unlock()

	for i, b := range up.bodies {
		if b != "hello" {
			t.Errorf("attempt %d body = %q, want %q", i+1, b, "hello")
		}
	}
}

// A body too big to hold in memory goes upstream once, whole.
func TestEngine_RetrySkipsOversizedBody(t *testing.T) {
	up := newFlaky(t, 1, http.StatusServiceUnavailable)
	engine := retryEngine(t, retryCfg("POST"), up.srv.URL, nil)
	big := bytes.Repeat([]byte("a"), 2<<20)

	t.Run("known length", func(t *testing.T) {
		if rec := do(engine, http.MethodPost, "/svc/x", bytes.NewReader(big)); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 (no retry)", rec.Code)
		}
	})

	t.Run("chunked", func(t *testing.T) {
		up.mu.Lock()
		up.fail = up.hits + 1
		up.mu.Unlock()

		req := httptest.NewRequest(http.MethodPost, "/svc/x", io.MultiReader(bytes.NewReader(big)))
		req.ContentLength = -1

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 (no retry)", rec.Code)
		}
	})

	up.mu.Lock()
	defer up.mu.Unlock()

	if up.hits != 2 {
		t.Errorf("upstream hit %d times, want once per request", up.hits)
	}
	for _, b := range up.bodies {
		if len(b) != len(big) {
			t.Errorf("upstream got %d bytes, want the whole %d", len(b), len(big))
		}
	}
}

func TestEngine_RetryWaitsForBackoff(t *testing.T) {
	up := newFlaky(t, 1, http.StatusServiceUnavailable)
	cfg := retryCfg("GET")
	cfg.InitialDelay = 80 * time.Millisecond
	engine := retryEngine(t, cfg, up.srv.URL, nil)

	start := time.Now()
	do(engine, http.MethodGet, "/svc/x", nil)

	if took := time.Since(start); took < 80*time.Millisecond {
		t.Errorf("took %v, want at least the 80ms backoff", took)
	}
}

// Streams aren't retried: an SSE request reaches the upstream once.
func TestEngine_RetrySkipsStreams(t *testing.T) {
	up := newFlaky(t, 1, http.StatusServiceUnavailable)
	engine := retryEngine(t, retryCfg("GET"), up.srv.URL, nil)

	req := httptest.NewRequest(http.MethodGet, "/svc/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	if got := up.count(); got != 1 {
		t.Errorf("upstream hit %d times, want 1", got)
	}
}

// Through gateway.New, the gateway-wide retry block, on by default, now
// takes effect.
func TestGateway_RetriesWithDefaultConfig(t *testing.T) {
	up := newFlaky(t, 1, http.StatusServiceUnavailable)
	app := forgetesting.NewTestApp("retry", "0.0.0")
	gw := gateway.New(
		bastion.WithDiscoveryEnabled(false),
		bastion.WithRoute(bastion.RouteConfig{
			Path: "/svc/*", Enabled: true,
			Targets: []bastion.TargetConfig{{URL: up.srv.URL}},
		}),
	)

	if err := app.RegisterExtension(gw); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/svc/x", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 after one retry", rec.Code)
	}
	if got := up.count(); got != 2 {
		t.Errorf("upstream hit %d times, want 2", got)
	}
}
