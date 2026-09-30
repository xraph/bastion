package contract

import (
	"context"
	"testing"
	"time"

	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
)

func TestOverviewStats_IdleGatewayReportsNullsNotZeros(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)
	out, err := overviewStatsHandler(deps)(context.Background(), overviewStatsRequest{}, contract.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if out.ErrorRate != nil || out.AvgLatencyMs != nil || out.P99LatencyMs != nil || out.CacheHitRate != nil || out.StartedAt != nil {
		t.Errorf("idle gateway reported a measured value: %+v", out)
	}
	if out.TopRoutes == nil {
		t.Error("topRoutes must be an empty list, not null")
	}
}

func TestOverviewStats_CountsAndRatesAndLiveCircuitsOnly(t *testing.T) {
	deps, rm, cbm, stats := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "a", Path: "/gw/a", Source: bastion.SourceManual, Enabled: true,
		Targets: []*bastion.Target{{ID: "a/0", URL: "http://a:1", Healthy: true}}})
	addRoute(t, rm, &bastion.Route{ID: "b", Path: "/gw/b", Source: bastion.SourceManual, Enabled: false})
	for i := 0; i < 4; i++ {
		stats.RecordRequest("a", "/gw/a")
		stats.RecordLatency("a", 10*time.Millisecond)
	}
	stats.RecordError("a")

	for i := 0; i < deps.Gateway.Config().CircuitBreaker.FailureThreshold; i++ {
		cbm.Get("a/0").RecordFailure()
		cbm.Get("gone/0").RecordFailure() // a breaker whose target no route has
	}

	out, _ := overviewStatsHandler(deps)(context.Background(), overviewStatsRequest{}, contract.Principal{})
	if out.ErrorRate == nil || *out.ErrorRate != 25 {
		t.Errorf("error rate = %v, want 25", out.ErrorRate)
	}
	if out.AvgLatencyMs == nil || *out.AvgLatencyMs != 10 {
		t.Errorf("avg latency = %v, want 10", out.AvgLatencyMs)
	}
	if out.TotalRoutes != 2 || out.EnabledRoutes != 1 {
		t.Errorf("routes = %d/%d, want 1 of 2 enabled", out.EnabledRoutes, out.TotalRoutes)
	}
	if out.OpenCircuits != 1 {
		t.Errorf("open circuits = %d, want 1 (the orphan breaker must not count)", out.OpenCircuits)
	}
	if len(out.TopRoutes) != 1 || out.TopRoutes[0].RouteID != "a" || out.TopRoutes[0].TotalRequests != 4 {
		t.Errorf("top routes = %+v", out.TopRoutes)
	}
}

func TestTrafficStats_BusiestFirstAndRetriesNotMeasured(t *testing.T) {
	deps, rm, _, stats := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "a", Path: "/gw/a", Source: bastion.SourceManual})
	addRoute(t, rm, &bastion.Route{ID: "b", Path: "/gw/b", Source: bastion.SourceManual})
	stats.RecordRequest("a", "/gw/a")
	for i := 0; i < 3; i++ {
		stats.RecordRequest("b", "/gw/b")
	}

	out, _ := trafficStatsHandler(deps)(context.Background(), trafficStatsRequest{}, contract.Principal{})
	if out.RetriesMeasured {
		t.Error("retries are never executed, so they are not measured")
	}
	if out.Total != 2 || out.Routes[0].RouteID != "b" {
		t.Errorf("routes = %+v, want b first", out.Routes)
	}
	if out.Routes[1].AvgLatencyMs != nil {
		t.Error("a route with no latency samples must report null latency")
	}
}

func TestCircuitsList_EveryLiveTargetWithTracking(t *testing.T) {
	deps, rm, cbm, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "a", Path: "/gw/a", Source: bastion.SourceManual,
		Targets: []*bastion.Target{{ID: "a/0", URL: "http://a:1"}, {ID: "a/1", URL: "http://a:2"}}})
	for i := 0; i < deps.Gateway.Config().CircuitBreaker.FailureThreshold; i++ {
		cbm.Get("a/0").RecordFailure()
	}
	cbm.Get("orphan")

	out, _ := circuitsListHandler(deps)(context.Background(), circuitsListRequest{}, contract.Principal{})
	if out.Total != 2 {
		t.Fatalf("circuits = %+v, want the 2 live targets", out.Circuits)
	}
	c0, c1 := out.Circuits[0], out.Circuits[1]
	if !c0.Tracked || c0.State != bastion.CircuitOpen || c0.LastFailure == nil || len(c0.Routes) != 1 {
		t.Errorf("a/0 = %+v", c0)
	}
	if c1.Tracked || c1.State != bastion.CircuitClosed || c1.LastStateChange != nil {
		t.Errorf("a/1 = %+v, want untracked and closed", c1)
	}
}
