package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

type rig struct {
	engine *proxy.Engine
	cbm    *resilience.CBManager
	stats  *proxy.StatsCollector
}

func newRig(t *testing.T, targets ...*bastion.Target) rig {
	t.Helper()

	return newRigWith(t, bastion.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 1,
		ResetTimeout:     time.Hour,
		HalfOpenMax:      1,
	}, targets...)
}

func newRigWith(t *testing.T, cbCfg bastion.CircuitBreakerConfig, targets ...*bastion.Target) rig {
	t.Helper()

	cfg := bastion.Config{}
	logger := forge.NewNoopLogger()
	rm := routing.NewManager()

	if err := rm.AddRoute(&bastion.Route{
		ID:       "r1",
		Path:     "/ok",
		Methods:  []string{http.MethodGet},
		Targets:  targets,
		Protocol: bastion.ProtocolHTTP,
		Source:   bastion.SourceManual,
		Enabled:  true,
	}); err != nil {
		t.Fatal(err)
	}

	cbm := resilience.NewCBManager(cbCfg)
	stats := proxy.NewStatsCollector()
	e := proxy.NewEngine(cfg, logger, rm, health.NewMonitor(health.Config{}, logger),
		cbm, bastion.NewRateLimiter(bastion.RateLimitConfig{}), stats,
		bastion.NewHookEngine(), routing.NewLoadBalancer(bastion.LBRoundRobin))

	return rig{engine: e, cbm: cbm, stats: stats}
}

func upstream(t *testing.T, status int) *httptest.Server {
	t.Helper()

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(s.Close)

	return s
}

func get(r rig) int {
	rec := httptest.NewRecorder()
	r.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok", nil))

	return rec.Code
}

func TestEngine_SkipsTargetWithOpenCircuit(t *testing.T) {
	good := upstream(t, http.StatusOK)
	r := newRig(t,
		&bastion.Target{ID: "bad", URL: "http://127.0.0.1:1", Weight: 1, Healthy: true},
		&bastion.Target{ID: "good", URL: good.URL, Weight: 1, Healthy: true},
	)
	r.cbm.Get("bad").RecordFailure() // threshold 1: now open

	for i := 0; i < 4; i++ {
		if code := get(r); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 from the target whose circuit is closed", i, code)
		}
	}
}

func TestEngine_AllCircuitsOpenIs503(t *testing.T) {
	r := newRig(t, &bastion.Target{ID: "bad", URL: "http://127.0.0.1:1", Weight: 1, Healthy: true})
	r.cbm.Get("bad").RecordFailure()

	if code := get(r); code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
}

func TestEngine_HalfOpenProbeSuccessClosesBreaker(t *testing.T) {
	good := upstream(t, http.StatusOK)
	r := newRigWith(t, bastion.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 1,
		ResetTimeout:     10 * time.Millisecond,
		HalfOpenMax:      1,
	}, &bastion.Target{ID: "t", URL: good.URL, Weight: 1, Healthy: true})

	r.cbm.Get("t").RecordFailure()
	time.Sleep(20 * time.Millisecond)

	// The first request is the half-open probe. Without a recorded success
	// the breaker stays half-open with its one probe spent, and the second
	// request is refused.
	for i := 0; i < 2; i++ {
		if code := get(r); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, code)
		}
	}

	if s := r.cbm.Snapshots()[0]; s.State != bastion.CircuitClosed || s.FailureCount != 0 {
		t.Errorf("breaker = %+v, want closed with no failures", s)
	}
}

func TestEngine_5xxLeavesBreakerAlone(t *testing.T) {
	bad := upstream(t, http.StatusInternalServerError)
	r := newRig(t, &bastion.Target{ID: "t", URL: bad.URL, Weight: 1, Healthy: true})

	for i := 0; i < 3; i++ {
		if code := get(r); code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want the upstream's 500 passed through", code)
		}
	}

	if s := r.cbm.Snapshots()[0]; s.State != bastion.CircuitClosed || s.FailureCount != 0 {
		t.Errorf("breaker = %+v, want closed and untouched by 5xx", s)
	}
}

func TestEngine_RecordsLatencyForAnsweredRequests(t *testing.T) {
	good := upstream(t, http.StatusOK)
	r := newRig(t, &bastion.Target{ID: "t", URL: good.URL, Weight: 1, Healthy: true})
	get(r)

	rs := r.stats.Snapshot([]*bastion.Route{{ID: "r1"}}).RouteStats["r1"]
	if rs == nil || rs.LatencySamples != 1 {
		t.Errorf("route stats = %+v, want one latency sample", rs)
	}
}

func TestEngine_5xxProbeDoesNotWedgeHalfOpenBreaker(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(up.Close)

	r := newRigWith(t, bastion.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 1,
		ResetTimeout:     10 * time.Millisecond,
		HalfOpenMax:      1,
	}, &bastion.Target{ID: "t", URL: up.URL, Weight: 1, Healthy: true})

	r.cbm.Get("t").RecordFailure()
	time.Sleep(20 * time.Millisecond)

	// The probe lands on a warming upstream answering 503.
	if code := get(r); code != http.StatusServiceUnavailable {
		t.Fatalf("probe = %d, want the upstream's 503", code)
	}

	// Once the upstream is healthy, the breaker must let a request through
	// and close, not refuse forever because the 5xx probe spent its slot.
	status.Store(http.StatusOK)
	if code := get(r); code != http.StatusOK {
		t.Fatalf("after recovery = %d, want 200", code)
	}
	if s := r.cbm.Snapshots()[0]; s.State != bastion.CircuitClosed {
		t.Errorf("breaker = %+v, want closed", s)
	}
}

func TestEngine_BusyHalfOpenTargetFallsBackToSibling(t *testing.T) {
	good := upstream(t, http.StatusOK)
	r := newRigWith(t, bastion.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 1,
		ResetTimeout:     10 * time.Millisecond,
		HalfOpenMax:      1,
	},
		&bastion.Target{ID: "probing", URL: "http://127.0.0.1:1", Weight: 1, Healthy: true},
		&bastion.Target{ID: "good", URL: good.URL, Weight: 1, Healthy: true},
	)

	r.cbm.Get("probing").RecordFailure()
	time.Sleep(20 * time.Millisecond)
	// Take the only probe slot, as an in-flight probe would.
	if !r.cbm.Get("probing").Allow() {
		t.Fatal("setup: probe refused")
	}

	for i := 0; i < 4; i++ {
		if code := get(r); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200 from the sibling", i, code)
		}
	}
}
