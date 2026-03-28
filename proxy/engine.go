package proxy

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/health"
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
	bufPool   sync.Pool
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
	startedAt     time.Time
	routeStats    map[string]*bastion.RouteStats
}

// NewStatsCollector creates a new stats collector.
func NewStatsCollector() *StatsCollector {
	return &StatsCollector{
		startedAt:  time.Now(),
		routeStats: make(map[string]*bastion.RouteStats),
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
		TotalRoutes:   len(routes),
		StartedAt:     sc.startedAt,
		Uptime:        int64(time.Since(sc.startedAt).Seconds()),
		RouteStats:    make(map[string]*bastion.RouteStats),
	}

	// Copy route stats
	for k, v := range sc.routeStats {
		stats.RouteStats[k] = &bastion.RouteStats{
			RouteID:       v.RouteID,
			Path:          v.Path,
			TotalRequests: v.TotalRequests,
			TotalErrors:   v.TotalErrors,
			AvgLatencyMs:  v.AvgLatencyMs,
			CacheHits:     v.CacheHits,
			CacheMisses:   v.CacheMisses,
			RateLimited:   v.RateLimited,
		}
	}

	// Count healthy upstreams
	for _, route := range routes {
		stats.TotalUpstreams += len(route.Targets)

		for _, t := range route.Targets {
			if t.Healthy {
				stats.HealthyUpstreams++
			}
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

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   config.Timeouts.Connect,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsConfig,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       config.Timeouts.Idle,
		ResponseHeaderTimeout: config.Timeouts.Read,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &Engine{
		config:    config,
		logger:    logger,
		lb:        lb,
		cbm:       cbm,
		hm:        hm,
		rm:        rm,
		rl:        rl,
		stats:     stats,
		hooks:     hooks,
		transport: transport,
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

	// Rate limiting
	if !pe.rl.Allow(r) {
		pe.stats.RecordRateLimited()
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)

		return
	}

	// Route matching
	route := pe.rm.MatchRoute(r.URL.Path, r.Method)
	if route == nil {
		http.Error(w, `{"error":"no matching route"}`, http.StatusNotFound)

		return
	}

	// Extension path blocking — reject requests to excluded extension paths
	if pe.isExtensionPathBlocked(route, r.URL.Path) {
		http.Error(w, `{"error":"extension path not allowed"}`, http.StatusForbidden)

		return
	}

	// Per-route rate limiting
	if route.RateLimit != nil && !pe.rl.AllowWithConfig(r, route.RateLimit) {
		pe.stats.RecordRateLimited()
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)

		return
	}

	pe.stats.RecordRequest(route.ID, route.Path)

	// Authentication check
	if pe.auth != nil {
		authCtx, authErr := pe.auth.Authenticate(r, route)
		if authErr != nil {
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
			pe.logger.Warn("on_request hook rejected request",
				forge.F("route_id", route.ID),
				forge.F("error", err),
			)

			http.Error(w, `{"error":"request rejected"}`, http.StatusForbidden)

			return
		}
	}

	// Protocol-specific handling
	switch {
	case isWebSocketUpgrade(r):
		pe.proxyWebSocket(w, r, route)

		return
	case isSSERequest(r):
		pe.proxySSE(w, r, route)

		return
	case isGRPCRequest(r):
		pe.proxyGRPC(w, r, route)

		return
	}

	// Select target via load balancer
	target := pe.selectTarget(r, route)
	if target == nil {
		pe.stats.RecordError(route.ID)
		http.Error(w, `{"error":"no healthy upstream"}`, http.StatusServiceUnavailable)

		return
	}

	// Circuit breaker check
	cb := pe.cbm.Get(target.ID)
	if !cb.Allow() {
		pe.stats.RecordCircuitBreak()
		http.Error(w, `{"error":"circuit breaker open"}`, http.StatusServiceUnavailable)

		return
	}

	// Build reverse proxy
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		pe.stats.RecordError(route.ID)
		http.Error(w, `{"error":"invalid upstream URL"}`, http.StatusBadGateway)

		return
	}

	proxy := &httputil.ReverseProxy{
		Director:       pe.director(r, route, targetURL),
		Transport:      pe.transport,
		ModifyResponse: pe.modifyResponse(route, target, start),
		ErrorHandler:   pe.errorHandler(route, target, cb),
		BufferPool:     &proxyBufferPool{pool: &pe.bufPool},
	}

	target.IncrConns()
	defer target.DecrConns()

	proxy.ServeHTTP(w, r)
}

func (pe *Engine) director(origReq *http.Request, route *bastion.Route, target *url.URL) func(req *http.Request) {
	return func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host

		// Path rewriting
		path := origReq.URL.Path

		if route.StripPrefix && route.Path != "" {
			prefix := strings.TrimSuffix(route.Path, "/*")
			prefix = strings.TrimSuffix(prefix, "/*")
			path = strings.TrimPrefix(path, prefix)

			if path == "" {
				path = "/"
			}
		}

		if route.AddPrefix != "" {
			path = route.AddPrefix + path
		}

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

func (pe *Engine) modifyResponse(route *bastion.Route, target *bastion.Target, start time.Time) func(*http.Response) error {
	return func(resp *http.Response) error {
		latency := time.Since(start)
		isError := resp.StatusCode >= 500
		target.RecordRequest(latency, isError)

		if isError {
			pe.stats.RecordError(route.ID)
			pe.hm.RecordPassiveFailure(target.ID)
		} else {
			pe.hm.RecordPassiveSuccess(target.ID)
		}

		// Apply response header transforms
		if route.Transform != nil {
			applyResponseHeaderPolicy(resp, route.Transform.ResponseHeaders)
		}

		// Add gateway headers
		resp.Header.Set("X-Gateway-Route", route.ID)

		// Run OnResponse hooks
		if pe.hooks != nil {
			pe.hooks.RunOnResponse(resp, route)
		}

		return nil
	}
}

func (pe *Engine) errorHandler(route *bastion.Route, target *bastion.Target, cb bastion.Breaker) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, _ *http.Request, err error) {
		pe.logger.Warn("upstream error",
			forge.F("route_id", route.ID),
			forge.F("target_url", target.URL),
			forge.F("error", err),
		)

		cb.RecordFailure()
		pe.stats.RecordError(route.ID)
		pe.hm.RecordPassiveFailure(target.ID)

		if pe.hooks != nil {
			pe.hooks.RunOnError(err, route, w)
		}

		http.Error(w, `{"error":"upstream error"}`, http.StatusBadGateway)
	}
}

func (pe *Engine) selectTarget(r *http.Request, route *bastion.Route) *bastion.Target {
	if len(route.Targets) == 0 {
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

	return pe.lb.Select(route.Targets, key)
}

// proxyWebSocket handles WebSocket upgrade requests.
func (pe *Engine) proxyWebSocket(w http.ResponseWriter, r *http.Request, route *bastion.Route) {
	target := pe.selectTarget(r, route)
	if target == nil {
		http.Error(w, `{"error":"no healthy upstream"}`, http.StatusServiceUnavailable)

		return
	}

	ProxyWebSocket(w, r, route, target, pe.config.WebSocket, pe.logger)
}

// proxyGRPC handles gRPC requests.
func (pe *Engine) proxyGRPC(w http.ResponseWriter, r *http.Request, route *bastion.Route) {
	target := pe.selectTarget(r, route)
	if target == nil {
		writeGRPCError(w, http.StatusServiceUnavailable, "no healthy upstream")

		return
	}

	ProxyGRPC(w, r, route, target, pe.config, pe.logger)
}

// proxySSE handles SSE requests.
func (pe *Engine) proxySSE(w http.ResponseWriter, r *http.Request, route *bastion.Route) {
	target := pe.selectTarget(r, route)
	if target == nil {
		http.Error(w, `{"error":"no healthy upstream"}`, http.StatusServiceUnavailable)

		return
	}

	ProxySSE(w, r, route, target, pe.config, pe.logger)
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
