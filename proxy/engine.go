package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
	"github.com/xraph/bastion/observability"
	"github.com/xraph/bastion/security"
)

// Engine handles HTTP reverse proxying to upstream targets.
type Engine struct {
	config bastion.Config
	logger forge.Logger
	lb     bastion.LoadBalancer
	cbm    bastion.CircuitControl
	hm     *health.Monitor
	rm     bastion.RouteRegistry
	rl     *bastion.RateLimiter
	stats  *StatsCollector
	hooks  *bastion.HookEngine

	// Optional components (set after construction)
	auth   *bastion.GatewayAuth
	cache  *bastion.ResponseCache
	tlsMgr *bastion.TLSManager

	transport *http.Transport
	// routeTransports holds the transports for routes with their own
	// timeouts. See transportFor.
	routeTransports transportCache
	// retry decides whether a failed attempt is sent again. Nil sends
	// every request once.
	retry bastion.RetryPolicy
	// traffic applies routes' traffic policies. Nil leaves every target
	// a candidate and mirrors nothing.
	traffic bastion.TrafficRouter
	// budgets holds a *retryBudget per target ID. See retryBudget.
	budgets sync.Map
	// metrics records the gateway's metrics. Nil records none.
	metrics *observability.Metrics
	// clock replaces time.Now for the retry budget in tests.
	clock       func() time.Time
	mirrorSlots chan struct{}
	bufPool     sync.Pool
}

// StatsCollector collects gateway-level statistics.
type StatsCollector struct {
	mu            sync.RWMutex
	totalReqs     int64
	totalErrors   int64
	cacheHits     int64
	cacheMisses   int64
	rateLimited   int64
	circuitBreaks int64
	retryAttempts int64
	budgetRefused int64
	startedAt     time.Time
	routeStats    map[string]*bastion.RouteStats
	latency       *latencyTrack
	routeLatency  map[string]*latencyTrack
}

// NewStatsCollector creates a new stats collector.
func NewStatsCollector() *StatsCollector {
	return &StatsCollector{
		startedAt:    time.Now(),
		routeStats:   make(map[string]*bastion.RouteStats),
		latency:      newLatencyTrack(GatewayLatencyWindow),
		routeLatency: make(map[string]*latencyTrack),
	}
}

// Snapshot returns the current gateway stats.
func (sc *StatsCollector) Snapshot(routes []*bastion.Route) *bastion.GatewayStats {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	stats := &bastion.GatewayStats{
		TotalRequests: sc.totalReqs,
		TotalErrors:   sc.totalErrors,
		CacheHits:     sc.cacheHits,
		CacheMisses:   sc.cacheMisses,
		RateLimited:   sc.rateLimited,
		CircuitBreaks: sc.circuitBreaks,
		RetryAttempts: sc.retryAttempts,

		RetryBudgetExhausted: sc.budgetRefused,
		AvgLatencyMs:         sc.latency.avgMs(),
		P99LatencyMs:         sc.latency.p99Ms(),
		TotalRoutes:          len(routes),
		StartedAt:            sc.startedAt,
		Uptime:               int64(time.Since(sc.startedAt).Seconds()),
		RouteStats:           make(map[string]*bastion.RouteStats),

		LatencySamples: sc.latency.samples(),
	}

	// Copy route stats, for routes that still exist. Gateway totals keep the
	// traffic removed routes served.
	live := make(map[string]struct{}, len(routes))
	for _, r := range routes {
		live[r.ID] = struct{}{}
	}

	for k, v := range sc.routeStats {
		if _, ok := live[k]; !ok {
			continue
		}

		rs := &bastion.RouteStats{
			RouteID:       v.RouteID,
			Path:          v.Path,
			TotalRequests: v.TotalRequests,
			TotalErrors:   v.TotalErrors,
			CacheHits:     v.CacheHits,
			CacheMisses:   v.CacheMisses,
			RateLimited:   v.RateLimited,
		}
		if lt, ok := sc.routeLatency[k]; ok {
			rs.AvgLatencyMs = lt.avgMs()
			rs.P99LatencyMs = lt.p99Ms()
			rs.LatencySamples = lt.samples()
		}

		stats.RouteStats[k] = rs
	}

	// Count upstreams by URL. Two routes proxying to one service are one
	// upstream. A URL counts healthy only if every entry for it is healthy.
	urlHealthy := make(map[string]bool)

	for _, route := range routes {
		for _, t := range route.Targets {
			prev, seen := urlHealthy[t.URL]
			urlHealthy[t.URL] = t.Healthy && (!seen || prev)
		}
	}

	stats.TotalUpstreams = len(urlHealthy)

	for _, ok := range urlHealthy {
		if ok {
			stats.HealthyUpstreams++
		}
	}

	return stats
}

// RecordRequest records a request for a route.
func (sc *StatsCollector) RecordRequest(routeID, path string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.totalReqs++

	if _, ok := sc.routeStats[routeID]; !ok {
		sc.routeStats[routeID] = &bastion.RouteStats{
			RouteID: routeID,
			Path:    path,
		}
	}

	sc.routeStats[routeID].TotalRequests++
}

// RecordLatency records how long an upstream took to answer a proxied HTTP
// request. Cache hits, transport errors and streaming protocols are not
// recorded, so latency describes answered upstream requests only.
func (sc *StatsCollector) RecordLatency(routeID string, d time.Duration) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.latency.record(d)

	lt, ok := sc.routeLatency[routeID]
	if !ok {
		lt = newLatencyTrack(RouteLatencyWindow)
		sc.routeLatency[routeID] = lt
	}

	lt.record(d)
}

// RecordError records an error for a route.
func (sc *StatsCollector) RecordError(routeID string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.totalErrors++

	if rs, ok := sc.routeStats[routeID]; ok {
		rs.TotalErrors++
	}
}

// RecordRateLimited records a rate-limited request.
func (sc *StatsCollector) RecordRateLimited() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.rateLimited++
}

// RecordCircuitBreak records a circuit breaker activation.
func (sc *StatsCollector) RecordCircuitBreak() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.circuitBreaks++
}

// RecordRetryBudgetExhausted records a retry the retry budget refused.
func (sc *StatsCollector) RecordRetryBudgetExhausted() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.budgetRefused++
}

// RecordRetryAttempt records a retry attempt.
func (sc *StatsCollector) RecordRetryAttempt() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.retryAttempts++
}

// RecordCacheHit records a cache hit.
func (sc *StatsCollector) RecordCacheHit() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.cacheHits++
}

// RecordCacheMiss records a cache miss.
func (sc *StatsCollector) RecordCacheMiss() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.cacheMisses++
}

// NewEngine creates a new proxy engine.
func NewEngine(
	config bastion.Config,
	logger forge.Logger,
	rm bastion.RouteRegistry,
	hm *health.Monitor,
	cbm bastion.CircuitControl,
	rl *bastion.RateLimiter,
	stats *StatsCollector,
	hooks *bastion.HookEngine,
	lb bastion.LoadBalancer,
) *Engine {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if config.TLS.InsecureSkipVerify {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // user-configured
	}

	transport := newHTTPTransport(config.Timeouts, tlsConfig)

	return &Engine{
		config:      config,
		logger:      logger,
		lb:          lb,
		cbm:         cbm,
		hm:          hm,
		rm:          rm,
		rl:          rl,
		stats:       stats,
		hooks:       hooks,
		transport:   transport,
		mirrorSlots: make(chan struct{}, maxMirrorsInFlight),
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, 32*1024)

				return &buf
			},
		},
	}
}

// isExtensionPathBlocked checks whether the request path targets an excluded
// extension. Only applies to FARP/discovery routes with BlockTraffic enabled.
func (pe *Engine) isExtensionPathBlocked(route *bastion.Route, requestPath string) bool {
	if route.Source != bastion.SourceFARP && route.Source != bastion.SourceDiscovery {
		return false
	}

	if route.ServiceName == "" {
		return false
	}

	filters := pe.config.OpenAPI.ExtensionFilters
	if len(filters) == 0 {
		return false
	}

	var filter *bastion.ExtensionPathFilter
	for i := range filters {
		if filters[i].MatchesService(route.ServiceName) {
			filter = &filters[i]
			break
		}
	}

	if filter == nil || !filter.BlockTraffic {
		return false
	}

	// Strip the route prefix to get the upstream-relative path.
	prefix := strings.TrimSuffix(route.Path, "/*")
	prefix = strings.TrimSuffix(prefix, "*")
	upstreamPath := strings.TrimPrefix(requestPath, prefix)
	if upstreamPath == "" {
		upstreamPath = "/"
	}

	return bastion.IsExtensionPathExcluded(upstreamPath, filter)
}

// SetAuth sets the gateway auth handler on the proxy engine.
func (pe *Engine) SetAuth(auth *bastion.GatewayAuth) { pe.auth = auth }

// SetCache sets the response cache on the proxy engine.
func (pe *Engine) SetCache(cache *bastion.ResponseCache) { pe.cache = cache }

// SetTLSManager sets the TLS manager on the proxy engine.
func (pe *Engine) SetTLSManager(tlsMgr *bastion.TLSManager) { pe.tlsMgr = tlsMgr }

// ServeHTTP is the main gateway handler.
func (pe *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// The identity headers ForwardAuthHeaders sets are the gateway's to
	// set. Whatever a client sent under those names goes before anything
	// else runs, on every route and every protocol, auth or no auth.
	security.DropAuthHeaders(r.Header)

	// Rate limiting
	if !pe.rl.Allow(r) {
		pe.stats.RecordRateLimited()
		pe.reject(r, observability.RejectGlobalRateLimit)
		// Name the limiter and show its numbers. Both 429 sites in this file
		// used to emit a byte-identical body, so an operator could not tell a
		// global cap from a per-route one, and a zero-valued config looked
		// exactly like a busy service.
		http.Error(w, fmt.Sprintf(
			`{"error":"rate limit exceeded","limiter":"global","rps":%v,"burst":%v,"perClient":%v}`,
			pe.rl.Config().RequestsPerSec, pe.rl.Config().Burst, pe.rl.Config().PerClient,
		), http.StatusTooManyRequests)

		return
	}

	// Route matching
	route := pe.rm.MatchRoute(r.URL.Path, r.Method)
	if route == nil {
		pe.reject(r, observability.RejectNoRoute)
		pe.logger.Debug("no matching route",
			forge.F("path", r.URL.Path),
			forge.F("method", r.Method),
			forge.F("route_count", pe.rm.RouteCount()),
		)
		http.Error(w, `{"error":"no matching route"}`, http.StatusNotFound)

		return
	}

	annotateServerSpan(r, route, requestProtocol(r, route))

	// Extension path blocking — reject requests to excluded extension paths
	if pe.isExtensionPathBlocked(route, r.URL.Path) {
		pe.reject(r, observability.RejectPathBlocked)
		http.Error(w, `{"error":"extension path not allowed"}`, http.StatusForbidden)

		return
	}

	// Per-route rate limiting
	if route.RateLimit != nil && !pe.rl.AllowWithConfig(r, route.RateLimit) {
		pe.stats.RecordRateLimited()
		pe.reject(r, observability.RejectRouteRateLimit)
		http.Error(w, fmt.Sprintf(
			`{"error":"rate limit exceeded","limiter":"route","route":%q,"rps":%v,"burst":%v,"perClient":%v}`,
			route.Path, route.RateLimit.RequestsPerSec, route.RateLimit.Burst, route.RateLimit.PerClient,
		), http.StatusTooManyRequests)

		return
	}

	pe.stats.RecordRequest(route.ID, route.Path)

	// Authentication check
	if pe.auth != nil {
		authCtx, authErr := pe.auth.Authenticate(r, route)
		if authErr != nil {
			pe.reject(r, authRejectReason(authErr))

			if ae, ok := authErr.(*bastion.AuthError); ok {
				http.Error(w, fmt.Sprintf(`{"error":"%s"}`, ae.Message), ae.Code)
			} else {
				http.Error(w, `{"error":"authentication failed"}`, http.StatusUnauthorized)
			}

			return
		}

		// Forward auth headers to upstream
		if authCtx != nil {
			pe.auth.ForwardAuthHeaders(r, authCtx)
		}
	}

	// Response cache check (before proxying)
	if pe.cache != nil {
		if cached := pe.cache.Get(r, route); cached != nil {
			pe.cache.WriteCachedResponse(w, cached)
			return
		}
	}

	// OnRequest hook
	if pe.hooks != nil {
		if err := pe.hooks.RunOnRequest(r, route); err != nil {
			pe.reject(r, observability.RejectHook)
			pe.logger.Warn("on_request hook rejected request",
				forge.F("route_id", route.ID),
				forge.F("error", err),
			)

			http.Error(w, `{"error":"request rejected"}`, http.StatusForbidden)

			return
		}
	}

	// Protocol-specific handling. Check both request headers and the route's
	// declared protocol — FARP/discovery may have tagged the route as SSE or
	// gRPC even if the client doesn't send protocol-specific headers.
	switch {
	case isWebSocketUpgrade(r):
		pe.proxyWebSocket(w, r, route)

		return
	case isSSERequest(r) || route.Protocol == bastion.ProtocolSSE:
		pe.proxySSE(w, r, route)

		return
	case isGRPCRequest(r) || route.Protocol == bastion.ProtocolGRPC:
		pe.proxyGRPC(w, r, route)

		return
	}

	// Select a target whose breaker admits the request. A half-open breaker
	// with every probe slot in flight refuses; the next candidate gets the
	// request instead of the client getting a 503.
	target, cb, refused, open := pe.admitTarget(r, route)
	if target == nil {
		pe.reject(r, unavailableReason(refused || open))

		if refused {
			pe.stats.RecordCircuitBreak()
			http.Error(w, `{"error":"circuit breaker open"}`, http.StatusServiceUnavailable)

			return
		}

		// Every breaker was already open, or no target is healthy. The
		// body has always said "no healthy upstream" for both; the metric
		// tells them apart.
		pe.stats.RecordError(route.ID)
		http.Error(w, `{"error":"no healthy upstream"}`, http.StatusServiceUnavailable)

		return
	}

	// Build reverse proxy
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		pe.stats.RecordError(route.ID)
		http.Error(w, `{"error":"invalid upstream URL"}`, http.StatusBadGateway)

		return
	}

	// Wrap the writer to track whether headers have been flushed. This
	// lets the error handler avoid writing a duplicate response when the
	// upstream drops the connection mid-stream (which causes "Body has
	// already been consumed" errors on the client side).
	tw := &statusTrackingWriter{ResponseWriter: w}

	// A route's own timeouts: connect, read and idle live on its transport,
	// write bounds sending the request body.
	transport := pe.transportFor(route)

	if eff, ok := routeTimeouts(route, pe.config.Timeouts); ok {
		var stop func()

		r, stop = withWriteTimeout(r, eff.Write)
		defer stop()
	}

	// Every attempt gets its own client span and duration observation.
	traced := newUpstreamTransport(transport, route, observability.ProtocolHTTP, pe.metrics)

	var roundTripper http.RoundTripper = traced

	// Retries and mirroring both send the body more than once, so it has
	// to be replayable: none, or one small enough to hold in memory. A
	// bigger body goes upstream once and isn't mirrored.
	retryCfg := effectiveRetry(route, pe.config.Retry)
	retryable := pe.retry != nil && mayRetry(retryCfg, r.Method)
	mirrorTo := pe.mirrorTarget(route)

	// Every request to the target counts toward its retry budget, retried
	// or not, since the budget is a share of the target's traffic.
	var budget *retryBudget
	if pe.retry != nil && retryCfg.Enabled && retryCfg.BudgetPercent > 0 {
		budget = pe.budgetFor(target.ID)
		budget.recordRequest(pe.now())
	}

	if retryable || mirrorTo != nil {
		body, replayable, err := bufferBody(r, maxReplayBody)
		if err != nil {
			cb.ReleaseProbe()
			pe.stats.RecordError(route.ID)
			http.Error(w, `{"error":"failed to read request body"}`, http.StatusBadGateway)

			return
		}

		if retryable && replayable {
			roundTripper = &retryTransport{
				base: traced, policy: pe.retry, pe: pe,
				route: route, target: target, cb: cb, body: body,
				budget: budget, budgetPercent: retryCfg.BudgetPercent,
			}
		}

		if mirrorTo != nil && replayable {
			pe.mirror(r, route, mirrorTo, body)
		}
	}

	proxy := &httputil.ReverseProxy{
		Director:       pe.director(r, route, targetURL),
		Transport:      roundTripper,
		ModifyResponse: pe.modifyResponse(route, target, cb, start),
		ErrorHandler:   pe.errorHandler(route, target, cb, tw),
		BufferPool:     &proxyBufferPool{pool: &pe.bufPool},
		// FlushInterval enables streaming for chunked/SSE responses that
		// arrive through the regular HTTP proxy path (e.g., SSE endpoints
		// registered under the service's HTTP catch-all route rather than
		// a dedicated /ws/* route). Without this, httputil.ReverseProxy
		// buffers the entire response, causing SSE streams to appear as
		// an instant 200 with no body.
		FlushInterval: -1, // flush immediately as data arrives
	}

	target.IncrConns()
	defer target.DecrConns()

	proxy.ServeHTTP(tw, r)
}

func (pe *Engine) director(origReq *http.Request, route *bastion.Route, target *url.URL) func(req *http.Request) {
	return func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host

		// Path rewriting: strip_prefix, then add_prefix, then rewrite_path.
		path := bastion.RewritePath(forwardPath(origReq), route)

		req.URL.Path = singleJoiningSlash(target.Path, path)
		req.URL.RawQuery = origReq.URL.RawQuery

		// Set host
		req.Host = target.Host

		// Standard proxy headers
		if clientIP, _, err := net.SplitHostPort(origReq.RemoteAddr); err == nil {
			if prior := origReq.Header.Get("X-Forwarded-For"); prior != "" {
				clientIP = prior + ", " + clientIP
			}

			req.Header.Set("X-Forwarded-For", clientIP)
			req.Header.Set("X-Real-IP", clientIP)
		}

		req.Header.Set("X-Forwarded-Host", origReq.Host)
		req.Header.Set("X-Forwarded-Proto", schemeFromRequest(origReq))

		// Apply header policy
		applyHeaderPolicy(req, route.Headers)

		// Apply transform headers
		if route.Transform != nil {
			applyHeaderPolicy(req, route.Transform.RequestHeaders)
		}
	}
}

func (pe *Engine) modifyResponse(route *bastion.Route, target *bastion.Target, cb bastion.Breaker, start time.Time) func(*http.Response) error {
	return func(resp *http.Response) error {
		latency := time.Since(start)
		isError := resp.StatusCode >= 500
		target.RecordRequest(latency, isError)
		pe.stats.RecordLatency(route.ID, latency)

		if isError {
			// A 5xx neither trips nor heals the breaker: only transport
			// errors trip it (see errorHandler). It does hand back a
			// half-open probe slot, or a warming upstream answering 503
			// would leave the breaker refusing forever.
			cb.ReleaseProbe()
			pe.stats.RecordError(route.ID)
			pe.hm.RecordPassiveFailure(target.ID)
		} else {
			// An answer below 500 closes a half-open breaker and resets a
			// closed one's consecutive failure count.
			cb.RecordSuccess()
			pe.hm.RecordPassiveSuccess(target.ID)
		}

		// Remove Content-Length and switch to chunked transfer encoding for
		// streaming-safe proxying. When the upstream drops the connection
		// mid-stream, a fixed Content-Length causes the client to receive
		// fewer bytes than promised, leading to "Body has already been
		// consumed" parse errors in JavaScript fetch() clients. Chunked
		// encoding lets the client detect truncation cleanly.
		if resp.ContentLength > 0 && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
			resp.Header.Del("Content-Length")
			resp.ContentLength = -1
		}

		// Rewrite Location headers on redirect responses when the gateway
		// stripped a prefix. The upstream's Location header refers to its own
		// path namespace (e.g., /api/v1/queries) but the client must be
		// redirected through the gateway prefix (e.g., /twinos/api/v1/queries).
		if isRedirect(resp.StatusCode) && route.StripPrefix {
			pe.rewriteLocationHeader(resp, route)
		}

		// Apply response header transforms
		if route.Transform != nil {
			applyResponseHeaderPolicy(resp, route.Transform.ResponseHeaders)
		}

		// Add gateway headers
		resp.Header.Set("X-Gateway-Route", route.ID)

		// SSE responses must not be buffered by intermediaries. nginx
		// honours X-Accel-Buffering: no; other proxies (Apache, some
		// CDNs) check the same header. FlushInterval=-1 already covers
		// our own ReverseProxy; this header tells anything downstream of
		// us to do the same.
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			resp.Header.Set("X-Accel-Buffering", "no")
			resp.Header.Set("Cache-Control", "no-cache")
		}

		// Run OnResponse hooks
		if pe.hooks != nil {
			pe.hooks.RunOnResponse(resp, route)
		}

		return nil
	}
}

func (pe *Engine) errorHandler(route *bastion.Route, target *bastion.Target, cb bastion.Breaker, tw *statusTrackingWriter) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, _ *http.Request, err error) {
		pe.logger.Warn("upstream error",
			forge.F("route_id", route.ID),
			forge.F("target_url", target.URL),
			forge.F("error", err),
		)

		// The retry loop has already counted a failure it gave up on.
		if !isAccounted(err) {
			cb.RecordFailure()
			pe.hm.RecordPassiveFailure(target.ID)
		}

		pe.stats.RecordError(route.ID)

		// If the proxy already started writing the response (headers
		// flushed), we cannot write an error body. Signal the client
		// that the stream ended abruptly by closing the underlying TCP
		// connection. This is cleaner than sending nothing — the client
		// sees a network error instead of silently receiving a truncated
		// body that fails to parse.
		if tw.written {
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, hijackErr := hijacker.Hijack(); hijackErr == nil {
					_ = conn.Close()
				}
			}
			return
		}

		if pe.hooks != nil {
			pe.hooks.RunOnError(err, route, w)
		}

		http.Error(w, `{"error":"upstream error"}`, http.StatusBadGateway)
	}
}

// selectTarget picks a target whose breaker isn't open. open reports
// whether an open breaker took any target out of the running.
func (pe *Engine) selectTarget(r *http.Request, route *bastion.Route) (target *bastion.Target, open bool) {
	candidates, open := pe.closedCircuits(pe.routeTargets(r, route))

	return pe.pick(r, candidates), open
}

// admitTarget picks a target and asks its breaker to admit the request,
// moving on to the remaining candidates when a breaker refuses. refused
// reports whether a breaker refused the request (a half-open one with its
// probe slots taken), so the caller can tell "circuit breaker open" from
// "no healthy upstream". open reports whether an open breaker took any
// target out of the running before that.
func (pe *Engine) admitTarget(r *http.Request, route *bastion.Route) (target *bastion.Target, cb bastion.Breaker, refused, open bool) {
	candidates, open := pe.closedCircuits(pe.routeTargets(r, route))

	for len(candidates) > 0 {
		t := pe.pick(r, candidates)
		if t == nil {
			break
		}

		cb := pe.cbm.Get(t.ID)
		if cb.Allow() {
			return t, cb, refused, open
		}

		refused = true
		candidates = slices.DeleteFunc(candidates, func(c *bastion.Target) bool { return c == t })
	}

	return nil, nil, refused, open
}

// unavailableReason is the reject reason when no target is left for a
// request: circuit_open when a breaker took at least one target out, so a
// pool whose breakers are all open counts there, and no_healthy_upstream
// otherwise.
func unavailableReason(breakerOpen bool) observability.RejectReason {
	if breakerOpen {
		return observability.RejectCircuitOpen
	}

	return observability.RejectNoHealthyUpstream
}

// authRejectReason is the reject reason for a failed authentication.
func authRejectReason(err error) observability.RejectReason {
	switch security.AuthFailureClass(err) {
	case security.AuthFailureForbidden:
		return observability.RejectForbidden
	case security.AuthFailureUnavailable:
		return observability.RejectAuthUnavailable
	default:
		return observability.RejectUnauthenticated
	}
}

// pick asks the load balancer for one of candidates.
func (pe *Engine) pick(r *http.Request, candidates []*bastion.Target) *bastion.Target {
	if len(candidates) == 0 {
		return nil
	}

	// Determine consistent hash key
	var key string
	if pe.config.LoadBalancing.Strategy == bastion.LBConsistentHash {
		key = pe.config.LoadBalancing.ConsistentKey
		if key != "" {
			key = r.Header.Get(key)
		}

		if key == "" {
			key = r.RemoteAddr
		}
	}

	return pe.lb.Select(candidates, key)
}

// closedCircuits drops targets whose breaker is open, and reports whether
// it dropped any. A breaker past its reset timeout reports half_open and
// stays eligible, so probes still reach it. The proxy asks the breaker
// rather than reading Target.CircuitState, which nothing assigns on live
// targets.
func (pe *Engine) closedCircuits(targets []*bastion.Target) (out []*bastion.Target, dropped bool) {
	out = make([]*bastion.Target, 0, len(targets))

	for _, t := range targets {
		if pe.cbm.Get(t.ID).State() != bastion.CircuitOpen {
			out = append(out, t)
		} else {
			dropped = true
		}
	}

	return out, dropped
}

// proxyWebSocket handles WebSocket upgrade requests.
func (pe *Engine) proxyWebSocket(w http.ResponseWriter, r *http.Request, route *bastion.Route) {
	target, open := pe.selectTarget(r, route)
	if target == nil {
		pe.reject(r, unavailableReason(open))
		http.Error(w, `{"error":"no healthy upstream"}`, http.StatusServiceUnavailable)

		return
	}

	defer pe.metrics.StreamStarted(observability.ProtocolWebSocket)()

	proxyWebSocket(w, r, route, target, pe.config.WebSocket, pe.logger, pe.metrics)
}

// proxyGRPC handles gRPC requests.
func (pe *Engine) proxyGRPC(w http.ResponseWriter, r *http.Request, route *bastion.Route) {
	target, open := pe.selectTarget(r, route)
	if target == nil {
		pe.reject(r, unavailableReason(open))
		writeGRPCError(w, http.StatusServiceUnavailable, "no healthy upstream")

		return
	}

	defer pe.metrics.StreamStarted(observability.ProtocolGRPC)()

	gp := NewGRPCProxy(pe.config, pe.logger)
	gp.metrics = pe.metrics
	gp.ServeHTTP(w, r, route, target)
}

// proxySSE handles SSE requests.
func (pe *Engine) proxySSE(w http.ResponseWriter, r *http.Request, route *bastion.Route) {
	target, open := pe.selectTarget(r, route)
	if target == nil {
		pe.reject(r, unavailableReason(open))
		http.Error(w, `{"error":"no healthy upstream"}`, http.StatusServiceUnavailable)

		return
	}

	defer pe.metrics.StreamStarted(observability.ProtocolSSE)()

	proxySSE(w, r, route, target, pe.config, pe.logger, pe.metrics)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func isSSERequest(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

func isGRPCRequest(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc")
}

func isRedirect(statusCode int) bool {
	return statusCode == http.StatusMovedPermanently ||
		statusCode == http.StatusFound ||
		statusCode == http.StatusSeeOther ||
		statusCode == http.StatusTemporaryRedirect ||
		statusCode == http.StatusPermanentRedirect
}

// rewriteLocationHeader prepends the gateway route prefix back onto the
// upstream's Location header. When StripPrefix is enabled, the gateway
// strips e.g. "/twinos" before forwarding to the upstream. If the upstream
// issues a redirect (301/302/307/308), its Location header refers to its
// own path space (e.g., "/api/v1/queries"). The client needs the full
// gateway path (e.g., "/twinos/api/v1/queries") to follow the redirect
// through the gateway.
func (pe *Engine) rewriteLocationHeader(resp *http.Response, route *bastion.Route) {
	location := resp.Header.Get("Location")
	if location == "" {
		return
	}

	// Parse the Location to check if it's a relative path or same-host absolute.
	locURL, err := url.Parse(location)
	if err != nil {
		return
	}

	// Only rewrite same-host or relative redirects. If the upstream redirects
	// to a completely different host, leave it alone.
	if locURL.Host != "" && resp.Request != nil && locURL.Host != resp.Request.URL.Host {
		return
	}

	// Compute the prefix that was stripped.
	prefix := strings.TrimSuffix(route.Path, "/*")
	prefix = strings.TrimSuffix(prefix, "*")
	if prefix == "" || prefix == "/" {
		return
	}

	// If the AddPrefix was used, the upstream path already has that prefix.
	// We need to strip it before prepending the gateway prefix.
	upstreamPath := locURL.Path
	if route.AddPrefix != "" {
		upstreamPath = strings.TrimPrefix(upstreamPath, route.AddPrefix)
	}

	// Don't double-prefix if the location already starts with the prefix.
	if strings.HasPrefix(upstreamPath, prefix) {
		return
	}

	// Rebuild the Location with the gateway prefix.
	locURL.Path = singleJoiningSlash(prefix, upstreamPath)

	resp.Header.Set("Location", locURL.String())
}

func applyResponseHeaderPolicy(resp *http.Response, policy bastion.HeaderPolicy) {
	for k, v := range policy.Add {
		resp.Header.Add(k, v)
	}

	for k, v := range policy.Set {
		resp.Header.Set(k, v)
	}

	for _, k := range policy.Remove {
		resp.Header.Del(k)
	}
}

// proxyBufferPool wraps sync.Pool for httputil.BufferPool interface.
type proxyBufferPool struct {
	pool *sync.Pool
}

func (p *proxyBufferPool) Get() []byte {
	buf := p.pool.Get().(*[]byte)

	return *buf
}

func (p *proxyBufferPool) Put(buf []byte) {
	p.pool.Put(&buf)
}

// statusTrackingWriter wraps http.ResponseWriter to track whether headers
// have been sent to the client. This allows the error handler to avoid
// writing a second response after the proxy has already started streaming.
type statusTrackingWriter struct {
	http.ResponseWriter
	written bool
}

func (w *statusTrackingWriter) WriteHeader(code int) {
	w.written = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusTrackingWriter) Write(b []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(b)
}

// Flush delegates to the underlying writer if it supports http.Flusher.
func (w *statusTrackingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack delegates to the underlying writer if it supports http.Hijacker
// (needed for WebSocket upgrades).
func (w *statusTrackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("upstream ResponseWriter does not support hijacking")
}
