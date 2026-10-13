package proxy_test

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

// fakeClock is a clock the test moves by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// budgetEngine proxies /svc/* to upstreamURL with one retry per request
// and a retry budget of percent.
func budgetEngine(t *testing.T, upstreamURL string, percent float64) (*proxy.Engine, *proxy.StatsCollector, *fakeClock) {
	t.Helper()

	cfg := retryCfg("GET")
	cfg.MaxAttempts = 2
	cfg.BudgetPercent = percent

	logger := forge.NewNoopLogger()
	rm := routing.NewManager()

	if err := rm.AddRoute(&bastion.Route{
		ID:       "r",
		Path:     "/svc/*",
		Targets:  []*bastion.Target{{ID: "t", URL: upstreamURL, Weight: 1, Healthy: true}},
		Protocol: bastion.ProtocolHTTP,
		Source:   bastion.SourceManual,
		Enabled:  true,
	}); err != nil {
		t.Fatal(err)
	}

	stats := proxy.NewStatsCollector()
	engine := proxy.NewEngine(bastion.Config{Retry: cfg}, logger, rm, health.NewMonitor(health.Config{}, logger),
		resilience.NewCBManager(bastion.CircuitBreakerConfig{}), bastion.NewRateLimiter(bastion.RateLimitConfig{}),
		stats, bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))
	engine.SetRetryPolicy(resilience.NewRetryExecutor(cfg))

	clock := &fakeClock{now: time.Unix(1_000, 0)}
	proxy.SetClock(engine, clock.Now)

	return engine, stats, clock
}

// A burst of failing requests gets retries up to the budget, and no more.
func TestEngine_RetryBudgetCapsABurst(t *testing.T) {
	up := newFlaky(t, 1_000, http.StatusServiceUnavailable)
	engine, stats, _ := budgetEngine(t, up.srv.URL, 20)

	for range 50 {
		if rec := do(engine, http.MethodGet, "/svc/x", nil); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want the last attempt's 503", rec.Code)
		}
	}

	// 20% of 50 requests is 10 retries.
	if got := up.count(); got != 60 {
		t.Errorf("upstream hit %d times, want 60 (50 requests, 10 retries)", got)
	}

	snap := stats.Snapshot(nil)
	if snap.RetryAttempts != 10 || snap.RetryBudgetExhausted != 40 {
		t.Errorf("stats: %d retries, %d refused by budget; want 10 and 40", snap.RetryAttempts, snap.RetryBudgetExhausted)
	}
}

// A target with little traffic still gets the floor of retries, though
// one retry is already well over 20% of one request.
func TestEngine_RetryBudgetFloorForLowTraffic(t *testing.T) {
	up := newFlaky(t, 1, http.StatusServiceUnavailable)
	engine, _, _ := budgetEngine(t, up.srv.URL, 20)

	if rec := do(engine, http.MethodGet, "/svc/x", nil); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 from the retry the floor allows", rec.Code)
	}
	if got := up.count(); got != 2 {
		t.Errorf("upstream hit %d times, want 2", got)
	}
}

// Once the window rolls past a burst, the target can retry again.
func TestEngine_RetryBudgetRecoversAsTheWindowRolls(t *testing.T) {
	up := newFlaky(t, 1_000, http.StatusServiceUnavailable)
	engine, _, clock := budgetEngine(t, up.srv.URL, 20)

	// Ten requests: the floor allows 3 retries, 20% of 10 allows 2.
	for range 10 {
		do(engine, http.MethodGet, "/svc/x", nil)
	}
	if got := up.count(); got != 13 {
		t.Fatalf("upstream hit %d times, want 13", got)
	}

	do(engine, http.MethodGet, "/svc/x", nil)
	if got := up.count(); got != 14 {
		t.Fatalf("upstream hit %d times, want 14: the budget is spent", got)
	}

	clock.Advance(10 * time.Second)

	do(engine, http.MethodGet, "/svc/x", nil)
	if got := up.count(); got != 16 {
		t.Errorf("upstream hit %d times, want 16: the window rolled and the retry is back", got)
	}
}

// A zero budget leaves retries unlimited.
func TestEngine_ZeroRetryBudgetIsUnlimited(t *testing.T) {
	up := newFlaky(t, 1_000, http.StatusServiceUnavailable)
	engine, stats, _ := budgetEngine(t, up.srv.URL, 0)

	for range 20 {
		do(engine, http.MethodGet, "/svc/x", nil)
	}

	if got := up.count(); got != 40 {
		t.Errorf("upstream hit %d times, want 40", got)
	}
	if got := stats.Snapshot(nil).RetryBudgetExhausted; got != 0 {
		t.Errorf("budget refused %d retries, want 0", got)
	}
}
