package observability

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/xraph/forge"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState string

const (
	// CircuitClosed allows requests through (normal operation).
	CircuitClosed CircuitState = "closed"

	// CircuitOpen rejects requests (fail-fast).
	CircuitOpen CircuitState = "open"

	// CircuitHalfOpen allows limited probe requests.
	CircuitHalfOpen CircuitState = "half_open"
)

// MetricsConfig holds metrics settings used by the Metrics collector.
type MetricsConfig struct {
	// Enabled turns the gateway's metrics on. They are recorded through the
	// Forge app's metrics, so the app's own metrics have to be on as well.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Prefix is ignored. The metric names are fixed (see the Metric*
	// constants) and Forge adds its namespace in front of them. The field
	// is kept so existing configuration still parses.
	Prefix string `json:"prefix,omitempty" yaml:"prefix"`
}

// Metric names. Forge exports them with its namespace in front, so
// bastion_upstream_request_duration_seconds scrapes as
// forge_bastion_upstream_request_duration_seconds.
const (
	MetricUpstreamRequestDuration = "bastion_upstream_request_duration_seconds"
	MetricRequestsRejected        = "bastion_requests_rejected_total"
	MetricRetriesAttempted        = "bastion_retries_attempted_total"
	MetricCircuitTransitions      = "bastion_circuit_breakers_transitioned_total"
	MetricUpstreamTargets         = "bastion_upstream_targets"
	MetricActiveStreams           = "bastion_active_streams"
	MetricBackgroundJobDuration   = "bastion_background_job_duration_seconds"
)

// Protocol is the protocol label: how the gateway talks to the upstream.
type Protocol string

// The protocol label's values.
const (
	ProtocolHTTP      Protocol = "http"
	ProtocolWebSocket Protocol = "websocket"
	ProtocolSSE       Protocol = "sse"
	ProtocolGRPC      Protocol = "grpc"
)

// streamProtocols are the protocols bastion_active_streams reports.
var streamProtocols = [...]Protocol{ProtocolWebSocket, ProtocolSSE, ProtocolGRPC}

// RejectReason is the reason label of bastion_requests_rejected_total: why
// the gateway answered a request itself instead of sending it upstream.
type RejectReason string

// The reason label's values.
const (
	RejectDraining          RejectReason = "draining"
	RejectIPFiltered        RejectReason = "ip_filtered"
	RejectGlobalRateLimit   RejectReason = "global_rate_limit"
	RejectRouteRateLimit    RejectReason = "route_rate_limit"
	RejectNoRoute           RejectReason = "no_route"
	RejectPathBlocked       RejectReason = "path_blocked"
	RejectUnauthenticated   RejectReason = "unauthenticated"
	RejectHook              RejectReason = "hook"
	RejectCircuitOpen       RejectReason = "circuit_open"
	RejectNoHealthyUpstream RejectReason = "no_healthy_upstream"
)

// Job is the job label of bastion_background_job_duration_seconds.
type Job string

// The job label's values.
const (
	JobDiscovery   Job = "discovery"
	JobOpenAPI     Job = "openapi"
	JobAsyncAPI    Job = "asyncapi"
	JobHealthCheck Job = "health_check"
)

// Status classes reported on bastion_upstream_request_duration_seconds
// besides 1xx to 5xx.
const (
	// StatusClassError is an attempt that got no response: the dial, the
	// TLS handshake or the round trip failed, or it timed out.
	StatusClassError = "error"

	// StatusClassCanceled is an attempt abandoned because the client went
	// away first.
	StatusClassCanceled = "canceled"
)

var (
	upstreamBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
	jobBuckets      = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
)

// Metrics records the gateway's metrics through Forge's metrics. Every
// label is a small fixed enum: route IDs, target IDs, URLs, hosts and
// service names never become labels.
//
// A nil *Metrics records nothing, so callers don't have to check.
type Metrics struct {
	upstreamDuration *forge.Vec[forge.Histogram] // protocol, status_class
	rejected         *forge.Vec[forge.Counter]   // reason
	retries          *forge.Vec[forge.Counter]   // result
	transitions      *forge.Vec[forge.Counter]   // state
	targets          *forge.Vec[forge.Gauge]     // health
	streams          *forge.Vec[forge.Gauge]     // protocol
	jobDuration      *forge.Vec[forge.Histogram] // job, result

	// open counts the streams in flight per protocol. The gauge is set
	// from it under its lock, so it always shows the real count.
	open [len(streamProtocols)]streamCount
}

type streamCount struct {
	mu sync.Mutex
	n  int64
}

// NewMetrics declares the gateway's metrics on m. It returns nil, which
// records nothing, when config.Enabled is false or m is nil.
func NewMetrics(m forge.Metrics, config MetricsConfig) *Metrics {
	if !config.Enabled || m == nil {
		return nil
	}

	return &Metrics{
		upstreamDuration: forge.HistogramVec(m, MetricUpstreamRequestDuration, forge.HistogramOpts{
			Buckets: upstreamBuckets,
			Help:    "Time from sending a request upstream to its response headers (the handshake, for WebSocket), per attempt.",
			Unit:    "s",
		}, "protocol", "status_class"),
		rejected:    forge.CounterVec(m, MetricRequestsRejected, "reason"),
		retries:     forge.CounterVec(m, MetricRetriesAttempted, "result"),
		transitions: forge.CounterVec(m, MetricCircuitTransitions, "state"),
		targets:     forge.GaugeVec(m, MetricUpstreamTargets, "health"),
		streams:     forge.GaugeVec(m, MetricActiveStreams, "protocol"),
		jobDuration: forge.HistogramVec(m, MetricBackgroundJobDuration, forge.HistogramOpts{
			Buckets: jobBuckets,
			Help:    "Duration of one run of a background job: a discovery poll, a spec refresh or a health check cycle.",
			Unit:    "s",
		}, "job", "result"),
	}
}

// ObserveUpstream records one attempt to reach an upstream. statusClass
// comes from StatusClass.
func (gm *Metrics) ObserveUpstream(protocol Protocol, statusClass string, d time.Duration) {
	if gm == nil {
		return
	}

	gm.upstreamDuration.With(string(protocol), statusClass).Observe(d.Seconds())
}

// RequestRejected counts a request the gateway answered itself.
func (gm *Metrics) RequestRejected(reason RejectReason) {
	if gm == nil {
		return
	}

	gm.rejected.With(string(reason)).Inc()
}

// RetrySent counts a retry that went out.
func (gm *Metrics) RetrySent() {
	if gm == nil {
		return
	}

	gm.retries.With("sent").Inc()
}

// RetryBudgetExhausted counts a retry the policy asked for and the
// target's retry budget refused.
func (gm *Metrics) RetryBudgetExhausted() {
	if gm == nil {
		return
	}

	gm.retries.With("budget_exhausted").Inc()
}

// CircuitTransitioned counts a circuit breaker moving into state.
func (gm *Metrics) CircuitTransitioned(state CircuitState) {
	if gm == nil {
		return
	}

	switch state {
	case CircuitClosed, CircuitOpen, CircuitHalfOpen:
		gm.transitions.With(string(state)).Inc()
	}
}

// SetUpstreamTargets sets how many monitored upstream targets are healthy
// and unhealthy right now.
func (gm *Metrics) SetUpstreamTargets(healthy, unhealthy int) {
	if gm == nil {
		return
	}

	gm.targets.With("healthy").Set(float64(healthy))
	gm.targets.With("unhealthy").Set(float64(unhealthy))
}

// StreamStarted counts a WebSocket, SSE or gRPC stream as open until the
// returned func is called. Calling it more than once is harmless.
func (gm *Metrics) StreamStarted(protocol Protocol) (done func()) {
	if gm == nil {
		return func() {}
	}

	idx := -1

	for i, p := range streamProtocols {
		if p == protocol {
			idx = i

			break
		}
	}

	if idx < 0 {
		return func() {}
	}

	gauge := gm.streams.With(string(protocol))
	count := &gm.open[idx]

	adjust := func(delta int64) {
		count.mu.Lock()
		defer count.mu.Unlock()

		count.n += delta
		gauge.Set(float64(count.n))
	}

	adjust(1)

	var once sync.Once

	return func() { once.Do(func() { adjust(-1) }) }
}

// ObserveJob records one run of a background job. failed is true when the
// run, or at least one unit of work inside it, failed.
func (gm *Metrics) ObserveJob(job Job, failed bool, d time.Duration) {
	if gm == nil {
		return
	}

	gm.jobDuration.With(string(job), result(failed)).Observe(d.Seconds())
}

// StatusClass turns the outcome of an upstream attempt into the
// status_class label: 1xx to 5xx for a response, StatusClassCanceled when
// the client went away, StatusClassError for any other failure.
func StatusClass(status int, err error) string {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return StatusClassCanceled
		}

		return StatusClassError
	}

	if status < 100 || status > 599 {
		return StatusClassError
	}

	return strconv.Itoa(status/100) + "xx"
}

func result(failed bool) string {
	if failed {
		return "error"
	}

	return "ok"
}
