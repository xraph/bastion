package proxy

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestReverseProxy creates a reverse proxy mimicking bastion's Engine
// setup with FlushInterval: -1 for SSE streaming support.
func newTestReverseProxy(targetURL string) http.Handler {
	target, _ := url.Parse(targetURL)
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		FlushInterval: -1,
	}
}

// newTestReverseProxyFull creates a reverse proxy that exactly mirrors
// bastion's Engine setup, including statusTrackingWriter, modifyResponse
// (which strips Content-Length), and error handler.
func newTestReverseProxyFull(targetURL string) http.Handler {
	target, _ := url.Parse(targetURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &statusTrackingWriter{ResponseWriter: w}
		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = target.Scheme
				req.URL.Host = target.Host
				req.Host = target.Host
			},
			ModifyResponse: func(resp *http.Response) error {
				// Mirror bastion's modifyResponse: strip Content-Length
				if resp.ContentLength > 0 {
					resp.Header.Del("Content-Length")
					resp.ContentLength = -1
				}
				return nil
			},
			ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
				if tw.written {
					return
				}
				http.Error(rw, `{"error":"upstream error"}`, http.StatusBadGateway)
			},
			FlushInterval: -1,
			BufferPool:    nil,
		}
		proxy.ServeHTTP(tw, r)
	})
}

// TestSSEThroughReverseProxy verifies that SSE streams work correctly when
// proxied through httputil.ReverseProxy with FlushInterval: -1.
func TestSSEThroughReverseProxy(t *testing.T) {
	eventCount := 3
	var upstreamHit bool

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for i := 0; i < eventCount; i++ {
			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"n\":%d}\n\n", i)
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(newTestReverseProxy(upstream.URL))
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !upstreamHit {
		t.Fatal("upstream was never reached")
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	var events []string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			events = append(events, line)
		}
	}

	if len(events) != eventCount {
		t.Fatalf("expected %d events, got %d: %v", eventCount, len(events), events)
	}

	t.Logf("received %d SSE events through reverse proxy", len(events))
}

// TestSSEPostThroughReverseProxy verifies that POST-based SSE endpoints
// work correctly (like twinos live query SSE).
func TestSSEPostThroughReverseProxy(t *testing.T) {
	var receivedBody string
	var receivedMethod string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method

		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		_, _ = fmt.Fprintf(w, "event: snapshot\ndata: {\"type\":\"snapshot\",\"body\":%q}\n\n", receivedBody)
		flusher.Flush()
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(newTestReverseProxy(upstream.URL))
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	body := `{"query":{"select":"*"},"project_id":"test"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/api/v1/query/live/sse", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if receivedMethod != http.MethodPost {
		t.Fatalf("upstream received %s, expected POST", receivedMethod)
	}

	if receivedBody != body {
		t.Fatalf("upstream received body %q, expected %q", receivedBody, body)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var events []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			events = append(events, line)
		}
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	t.Logf("POST SSE: received event: %s", events[0])
}

// TestSSEStreamingLatency verifies events are flushed immediately, not buffered.
func TestSSEStreamingLatency(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for i := 0; i < 3; i++ {
			_, _ = fmt.Fprintf(w, "event: tick\ndata: %d\n\n", i)
			flusher.Flush()
			if i < 2 {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(newTestReverseProxy(upstream.URL))
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxy.URL+"/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	var timestamps []time.Time
	var mu sync.Mutex

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			mu.Lock()
			timestamps = append(timestamps, time.Now())
			mu.Unlock()
		}
	}

	if len(timestamps) != 3 {
		t.Fatalf("expected 3 events, got %d", len(timestamps))
	}

	// If buffered, all 3 events arrive within <50ms. With streaming, ~200ms gap.
	gap1 := timestamps[1].Sub(timestamps[0])
	gap2 := timestamps[2].Sub(timestamps[1])

	if gap1 < 100*time.Millisecond {
		t.Errorf("events 0→1 arrived too fast (%v), expected ~200ms — likely buffered", gap1)
	}
	if gap2 < 100*time.Millisecond {
		t.Errorf("events 1→2 arrived too fast (%v), expected ~200ms — likely buffered", gap2)
	}

	t.Logf("event gaps: %v, %v (expected ~200ms each)", gap1, gap2)
}

// TestSSEWithFullProxyStack tests SSE through the complete bastion proxy
// pipeline including statusTrackingWriter and modifyResponse.
func TestSSEWithFullProxyStack(t *testing.T) {
	eventCount := 3
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for i := 0; i < eventCount; i++ {
			_, _ = fmt.Fprintf(w, "event: message\ndata: {\"n\":%d}\n\n", i)
			flusher.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(newTestReverseProxyFull(upstream.URL))
	defer proxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// POST request like twinos live query SSE
	body := `{"query":{"select":"*"}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/api/v1/query/live/sse", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	// Verify Content-Length was stripped (chunked transfer)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		t.Errorf("Content-Length should be stripped for streaming, got %q", cl)
	}

	scanner := bufio.NewScanner(resp.Body)
	var events []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			events = append(events, line)
		}
	}

	if len(events) != eventCount {
		t.Fatalf("expected %d events, got %d: %v", eventCount, len(events), events)
	}

	t.Logf("full proxy stack: received %d SSE events", len(events))
}
