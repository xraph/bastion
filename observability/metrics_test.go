package observability_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xraph/bastion/internal/telemetry/telemetrytest"
	"github.com/xraph/bastion/observability"
)

// The scrape name of a bastion metric: forge puts its namespace in front.
func scraped(name string) string { return "forge_" + name }

func TestMetrics_NamesAndLabelsOnTheScrape(t *testing.T) {
	app := telemetrytest.MetricsApp(t)
	m := observability.NewMetrics(app.Metrics(), observability.MetricsConfig{Enabled: true, Prefix: "ignored"})

	m.ObserveUpstream(observability.ProtocolHTTP, "2xx", 20*time.Millisecond)
	m.ObserveUpstream(observability.ProtocolGRPC, observability.StatusClassError, time.Second)
	m.RequestRejected(observability.RejectNoRoute)
	m.RequestRejected(observability.RejectNoRoute)
	m.RetrySent()
	m.RetryBudgetExhausted()
	m.CircuitTransitioned(observability.CircuitOpen)
	m.CircuitTransitioned("bogus")
	m.SetUpstreamTargets(3, 1)
	m.ObserveJob(observability.JobDiscovery, false, 50*time.Millisecond)
	m.ObserveJob(observability.JobOpenAPI, true, 2*time.Second)

	ws := m.StreamStarted(observability.ProtocolWebSocket)
	sse := m.StreamStarted(observability.ProtocolSSE)
	sse()
	sse() // a second call is a no-op

	body := telemetrytest.Scrape(t, app)

	want := map[string][]string{
		observability.MetricUpstreamRequestDuration + "_count": {
			`protocol="http",status_class="2xx"} 1`,
			`protocol="grpc",status_class="error"} 1`,
		},
		observability.MetricRequestsRejected:   {`reason="no_route"} 2`},
		observability.MetricRetriesAttempted:   {`result="sent"} 1`, `result="budget_exhausted"} 1`},
		observability.MetricCircuitTransitions: {`state="open"} 1`},
		observability.MetricUpstreamTargets:    {`health="healthy"} 3`, `health="unhealthy"} 1`},
		observability.MetricActiveStreams:      {`protocol="websocket"} 1`, `protocol="sse"} 0`},
		observability.MetricBackgroundJobDuration + "_count": {
			`job="discovery",result="ok"} 1`,
			`job="openapi",result="error"} 1`,
		},
	}

	for name, samples := range want {
		series := telemetrytest.Series(body, scraped(name))
		if len(series) != len(samples) {
			t.Errorf("%s: got %d series, want %d:\n%s", scraped(name), len(series), len(samples), strings.Join(series, "\n"))

			continue
		}

		for _, sample := range samples {
			found := false

			for _, line := range series {
				if strings.HasSuffix(line, sample) {
					found = true
				}
			}

			if !found {
				t.Errorf("%s: no series ending %s in:\n%s", scraped(name), sample, strings.Join(series, "\n"))
			}
		}
	}

	if strings.Contains(body, "gateway_") || strings.Contains(body, "ignored") {
		t.Error("the old prefix still shows up on the scrape")
	}

	ws()

	streams := strings.Join(telemetrytest.Series(telemetrytest.Scrape(t, app), scraped(observability.MetricActiveStreams)), "\n")
	if !strings.Contains(streams, `protocol="websocket"} 0`) {
		t.Errorf("the closed websocket stream is still counted:\n%s", streams)
	}
}

func TestMetrics_DisabledOrNilRecordsNothing(t *testing.T) {
	app := telemetrytest.MetricsApp(t)

	if m := observability.NewMetrics(app.Metrics(), observability.MetricsConfig{Enabled: false}); m != nil {
		t.Fatal("disabled metrics are not nil")
	}

	if m := observability.NewMetrics(nil, observability.MetricsConfig{Enabled: true}); m != nil {
		t.Fatal("metrics without a sink are not nil")
	}

	var m *observability.Metrics

	m.ObserveUpstream(observability.ProtocolHTTP, "2xx", time.Second)
	m.RequestRejected(observability.RejectHook)
	m.RetrySent()
	m.RetryBudgetExhausted()
	m.CircuitTransitioned(observability.CircuitOpen)
	m.SetUpstreamTargets(1, 1)
	m.ObserveJob(observability.JobHealthCheck, true, time.Second)
	m.StreamStarted(observability.ProtocolGRPC)()

	if strings.Contains(telemetrytest.Scrape(t, app), "bastion_") {
		t.Error("a nil Metrics recorded something")
	}
}

func TestStatusClass(t *testing.T) {
	for _, tc := range []struct {
		status int
		err    error
		want   string
	}{
		{101, nil, "1xx"},
		{200, nil, "2xx"},
		{302, nil, "3xx"},
		{404, nil, "4xx"},
		{503, nil, "5xx"},
		{0, errors.New("dial tcp: refused"), observability.StatusClassError},
		{0, context.DeadlineExceeded, observability.StatusClassError},
		{0, context.Canceled, observability.StatusClassCanceled},
		{0, nil, observability.StatusClassError},
	} {
		if got := observability.StatusClass(tc.status, tc.err); got != tc.want {
			t.Errorf("StatusClass(%d, %v) = %q, want %q", tc.status, tc.err, got, tc.want)
		}
	}
}
