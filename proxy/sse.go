package proxy

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
)

// sseTransport is a shared transport for SSE upstream connections.
// Keep-alives are enabled (the default) because DisableKeepAlives causes
// the transport to send "Connection: close", which makes many upstream
// servers close the connection immediately after responding — terminating
// the SSE stream. Instead, we set a short IdleConnTimeout so that
// connections are cleaned up quickly once the SSE stream ends.
var sseTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second,
	IdleConnTimeout:     5 * time.Second,
	MaxIdleConnsPerHost: 2,
	// No ResponseHeaderTimeout — SSE upstreams may take time before
	// sending the first event.
}

// ProxySSE proxies a Server-Sent Events connection to an upstream target.
func ProxySSE(
	w http.ResponseWriter,
	r *http.Request,
	route *bastion.Route,
	target *bastion.Target,
	config bastion.Config,
	logger forge.Logger,
) {
	// Check if the response writer supports flushing
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming unsupported"}`, http.StatusInternalServerError)

		return
	}

	// Build upstream URL
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		http.Error(w, `{"error":"invalid upstream URL"}`, http.StatusBadGateway)

		return
	}

	upstreamPath := r.URL.Path
	if route.StripPrefix && route.Path != "" {
		prefix := strings.TrimSuffix(route.Path, "/*")
		upstreamPath = strings.TrimPrefix(upstreamPath, prefix)

		if upstreamPath == "" {
			upstreamPath = "/"
		}
	}

	if route.AddPrefix != "" {
		upstreamPath = route.AddPrefix + upstreamPath
	}

	upstreamURL := targetURL.Scheme + "://" + targetURL.Host + singleJoiningSlash(targetURL.Path, upstreamPath)
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// Create upstream request preserving the original method and body.
	// SSE endpoints may use POST (e.g., to send a subscription payload)
	// rather than GET.
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to create upstream request"}`, http.StatusBadGateway)

		return
	}

	// Copy relevant headers from the original request.
	// Preserve Content-Type for POST bodies (e.g., application/json).
	if ct := r.Header.Get("Content-Type"); ct != "" {
		upstreamReq.Header.Set("Content-Type", ct)
	}

	if cl := r.Header.Get("Content-Length"); cl != "" {
		upstreamReq.Header.Set("Content-Length", cl)
	}

	upstreamReq.Header.Set("Accept", "text/event-stream")
	upstreamReq.Header.Set("Cache-Control", "no-cache")

	if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		upstreamReq.Header.Set("Last-Event-ID", lastEventID)
	}

	// Copy auth headers from the original request
	if auth := r.Header.Get("Authorization"); auth != "" {
		upstreamReq.Header.Set("Authorization", auth)
	}

	// Proxy headers
	if clientIP, _, splitErr := net.SplitHostPort(r.RemoteAddr); splitErr == nil {
		upstreamReq.Header.Set("X-Forwarded-For", clientIP)
		upstreamReq.Header.Set("X-Real-IP", clientIP)
	}

	upstreamReq.Header.Set("X-Forwarded-Host", r.Host)
	upstreamReq.Header.Set("X-Forwarded-Proto", schemeFromRequest(r))

	// Apply header policy
	applyHeaderPolicy(upstreamReq, route.Headers)

	// Make upstream request using the shared SSE transport.
	// Timeout=0 because SSE connections are long-lived streams.
	client := &http.Client{
		Timeout:   0,
		Transport: sseTransport,
	}

	resp, err := client.Do(upstreamReq)
	if err != nil {
		logger.Warn("SSE upstream connect failed",
			forge.F("upstream_url", upstreamURL),
			forge.F("error", err),
		)

		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusBadGateway)

		return
	}

	defer func() {
		_ = resp.Body.Close()
		// Eagerly close idle connections after the stream ends so transport
		// goroutines (readLoop/writeLoop) don't linger until IdleConnTimeout.
		sseTransport.CloseIdleConnections()
	}()

	target.IncrConns()
	defer target.DecrConns()

	// Set SSE response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Gateway-Route", route.ID)

	// Copy any upstream headers we want to forward
	if retryHeader := resp.Header.Get("Retry"); retryHeader != "" {
		w.Header().Set("Retry", retryHeader)
	}

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	logger.Debug("SSE connection established",
		forge.F("route_id", route.ID),
		forge.F("upstream_url", upstreamURL),
	)

	// Stream data from upstream to client with periodic flushing
	flushInterval := config.SSE.FlushInterval
	if flushInterval <= 0 {
		flushInterval = 100 * time.Millisecond
	}

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	buf := make([]byte, 4096)
	needsFlush := false

	for {
		select {
		case <-r.Context().Done():
			// Client disconnected
			logger.Debug("SSE client disconnected",
				forge.F("route_id", route.ID),
			)

			return

		case <-ticker.C:
			if needsFlush {
				flusher.Flush()
				needsFlush = false
			}

		default:
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, writeErr := w.Write(buf[:n]); writeErr != nil {
					return
				}

				needsFlush = true
			}

			if readErr != nil {
				if readErr != io.EOF {
					logger.Warn("SSE upstream read error",
						forge.F("route_id", route.ID),
						forge.F("error", readErr),
					)
				}

				// Final flush
				flusher.Flush()

				return
			}
		}
	}
}
