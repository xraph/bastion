package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/bastion/internal/telemetry"
	"github.com/xraph/bastion/internal/telemetry/telemetrytest"
	"github.com/xraph/bastion/observability"
)

func TestMonitor_CycleIsARootSpanWithCounts(t *testing.T) {
	app := telemetrytest.MetricsApp(t)
	rec := telemetrytest.Recorder(t)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/down" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(up.Close)

	hm := NewMonitor(Config{
		Enabled:          true,
		Interval:         5 * time.Millisecond,
		Timeout:          time.Second,
		FailureThreshold: 1,
		SuccessThreshold: 1,
	}, forge.NewNoopLogger())
	hm.SetMetrics(observability.NewMetrics(app.Metrics(), observability.MetricsConfig{Enabled: true}))
	hm.Register("r", &probeTarget{id: "up", url: up.URL, path: "/ok", healthy: true})
	hm.Register("r", &probeTarget{id: "down", url: up.URL, path: "/down", healthy: true})

	// Start runs inside Forge's "start bastion" span; the loop must not
	// keep it.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	ctx, bootSpan := telemetry.Start(ctx, "start bastion")
	hm.Start(ctx)
	bootSpan.End()

	cycle := telemetrytest.Await(t, rec, "check upstream health", 1)[0]

	hm.Stop()

	if cycle.Parent().IsValid() {
		t.Error("the cycle span hangs off the boot span; want a new trace")
	}

	for key, want := range map[string]int64{
		"bastion.health.targets":   2,
		"bastion.health.failed":    1,
		"bastion.health.unhealthy": 1,
	} {
		if v, _ := telemetrytest.Attr(cycle, key); v.AsInt64() != want {
			t.Errorf("%s = %v, want %d", key, v.String(), want)
		}
	}

	body := telemetrytest.Scrape(t, app)

	targets := strings.Join(telemetrytest.Series(body, "forge_"+observability.MetricUpstreamTargets), "\n")
	if !strings.Contains(targets, `{health="healthy"} 1`) || !strings.Contains(targets, `{health="unhealthy"} 1`) {
		t.Errorf("upstream targets series:\n%s", targets)
	}

	jobs := strings.Join(telemetrytest.Series(body, "forge_"+observability.MetricBackgroundJobDuration+"_count"), "\n")
	if !strings.Contains(jobs, `job="health_check",result="error"`) {
		t.Errorf("job duration series:\n%s", jobs)
	}
}

func TestMonitor_NoTargetsNoSpan(t *testing.T) {
	rec := telemetrytest.Recorder(t)

	hm := NewMonitor(Config{Enabled: true, Interval: time.Second, Timeout: time.Second}, forge.NewNoopLogger())
	hm.checkAll(context.Background())

	if len(rec.Ended()) != 0 {
		t.Errorf("an idle cycle started %d spans", len(rec.Ended()))
	}
}

func TestMonitor_TargetGaugeFollowsRegistration(t *testing.T) {
	app := telemetrytest.MetricsApp(t)

	hm := NewMonitor(Config{}, forge.NewNoopLogger())
	hm.SetMetrics(observability.NewMetrics(app.Metrics(), observability.MetricsConfig{Enabled: true}))

	hm.Register("r", &probeTarget{id: "a", healthy: true})
	hm.Register("r", &probeTarget{id: "b", healthy: false})
	hm.Register("r", &probeTarget{id: "c", healthy: true})
	hm.Deregister("c")

	got := strings.Join(telemetrytest.Series(telemetrytest.Scrape(t, app), "forge_"+observability.MetricUpstreamTargets), "\n")
	if !strings.Contains(got, `{health="healthy"} 1`) || !strings.Contains(got, `{health="unhealthy"} 1`) {
		t.Errorf("upstream targets series:\n%s", got)
	}
}
