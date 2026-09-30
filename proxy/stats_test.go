package proxy

import (
	"sync"
	"testing"
	"time"

	bastion "github.com/xraph/bastion"
)

func routes(ids ...string) []*bastion.Route {
	out := make([]*bastion.Route, 0, len(ids))
	for _, id := range ids {
		out = append(out, &bastion.Route{ID: id, Path: "/" + id})
	}
	return out
}

func TestStatsCollector_LatencyAverageAndP99(t *testing.T) {
	sc := NewStatsCollector()
	sc.RecordRequest("r1", "/r1")
	for i := 1; i <= 100; i++ {
		sc.RecordLatency("r1", time.Duration(i)*time.Millisecond)
	}

	s := sc.Snapshot(routes("r1"))

	if got, want := s.AvgLatencyMs, 50.5; got != want {
		t.Errorf("gateway avg = %v, want %v", got, want)
	}
	// Nearest rank: ceil(0.99 * 100) = 99th smallest = 99ms.
	if got, want := s.P99LatencyMs, 99.0; got != want {
		t.Errorf("gateway p99 = %v, want %v", got, want)
	}
	if s.LatencySamples != 100 {
		t.Errorf("gateway samples = %d, want 100", s.LatencySamples)
	}

	rs := s.RouteStats["r1"]
	if rs.AvgLatencyMs != 50.5 || rs.P99LatencyMs != 99 || rs.LatencySamples != 100 {
		t.Errorf("route stats = %+v", rs)
	}
}

func TestStatsCollector_P99WindowEvictsOldest(t *testing.T) {
	sc := NewStatsCollector()
	sc.RecordRequest("r1", "/r1")
	// A slow burst that falls out of the window...
	for i := 0; i < RouteLatencyWindow; i++ {
		sc.RecordLatency("r1", time.Second)
	}
	// ...then a full window of fast responses.
	for i := 0; i < RouteLatencyWindow; i++ {
		sc.RecordLatency("r1", time.Millisecond)
	}

	rs := sc.Snapshot(routes("r1")).RouteStats["r1"]
	if rs.P99LatencyMs != 1 {
		t.Errorf("p99 = %v, want 1 (slow burst should have left the window)", rs.P99LatencyMs)
	}
	if rs.LatencySamples != RouteLatencyWindow {
		t.Errorf("samples = %d, want %d", rs.LatencySamples, RouteLatencyWindow)
	}
	// The average is lifetime, so it still carries the slow burst.
	if rs.AvgLatencyMs <= 1 {
		t.Errorf("avg = %v, want lifetime average above 1ms", rs.AvgLatencyMs)
	}
}

func TestStatsCollector_NoSamplesMeansZeroAndZeroCount(t *testing.T) {
	sc := NewStatsCollector()
	sc.RecordRequest("r1", "/r1")

	s := sc.Snapshot(routes("r1"))
	if s.LatencySamples != 0 || s.P99LatencyMs != 0 || s.AvgLatencyMs != 0 {
		t.Errorf("empty latency = %+v", s)
	}
}

func TestStatsCollector_ConcurrentRecordAndSnapshot(t *testing.T) {
	sc := NewStatsCollector()
	rs := routes("r1", "r2")

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := rs[w%2].ID
			for i := 0; i < 500; i++ {
				sc.RecordRequest(id, "/"+id)
				sc.RecordLatency(id, time.Duration(i)*time.Microsecond)
				sc.RecordError(id)
			}
		}(w)
	}
	for i := 0; i < 50; i++ {
		_ = sc.Snapshot(rs)
	}
	wg.Wait()

	s := sc.Snapshot(rs)
	if s.TotalRequests != 4000 {
		t.Errorf("total requests = %d, want 4000", s.TotalRequests)
	}
}

func TestStatsCollector_DropsStatsForRemovedRoutes(t *testing.T) {
	sc := NewStatsCollector()
	sc.RecordRequest("gone", "/gone")
	sc.RecordRequest("live", "/live")

	s := sc.Snapshot(routes("live"))
	if _, ok := s.RouteStats["gone"]; ok {
		t.Error("stats for a removed route were reported")
	}
	if _, ok := s.RouteStats["live"]; !ok {
		t.Error("stats for a live route are missing")
	}
	// Gateway totals still include traffic the removed route served.
	if s.TotalRequests != 2 {
		t.Errorf("total requests = %d, want 2", s.TotalRequests)
	}
}

func TestStatsCollector_CountsUpstreamsByURL(t *testing.T) {
	sc := NewStatsCollector()
	shared := "http://orders:8080"
	rs := []*bastion.Route{
		{ID: "a", Targets: []*bastion.Target{
			{ID: "a/0", URL: shared, Healthy: true},
			{ID: "a/1", URL: "http://users:8080", Healthy: true},
		}},
		{ID: "b", Targets: []*bastion.Target{
			{ID: "b/0", URL: shared, Healthy: false},
		}},
	}

	s := sc.Snapshot(rs)
	if s.TotalUpstreams != 2 {
		t.Errorf("total upstreams = %d, want 2 distinct URLs", s.TotalUpstreams)
	}
	// orders is unhealthy on route b, so it is not counted healthy.
	if s.HealthyUpstreams != 1 {
		t.Errorf("healthy upstreams = %d, want 1", s.HealthyUpstreams)
	}
}
