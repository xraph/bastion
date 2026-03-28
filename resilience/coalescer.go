package resilience

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// CoalescerConfig configures request coalescing.
type CoalescerConfig struct {
	// Enabled enables request coalescing for identical concurrent GET requests.
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// RequestCoalescer deduplicates identical concurrent GET requests
// to the same upstream using a singleflight-style pattern.
type RequestCoalescer struct {
	config CoalescerConfig

	mu     sync.Mutex
	flight map[string]*call
}

type call struct {
	wg  sync.WaitGroup
	val *CoalescedResponse
	err error
}

// CoalescedResponse holds a captured HTTP response for coalescing.
type CoalescedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// NewRequestCoalescer creates a new request coalescer.
func NewRequestCoalescer(config CoalescerConfig) *RequestCoalescer {
	return &RequestCoalescer{
		config: config,
		flight: make(map[string]*call),
	}
}

// Do coalesces identical GET requests. If a request with the same key
// is already in-flight, this blocks until that request completes and
// returns the same response. The doFn is only called once per unique key.
func (rc *RequestCoalescer) Do(key string, doFn func() (*http.Response, error)) (*CoalescedResponse, error) {
	if !rc.config.Enabled {
		resp, err := doFn()
		if err != nil {
			return nil, err
		}

		return captureResponse(resp)
	}

	rc.mu.Lock()
	if c, ok := rc.flight[key]; ok {
		rc.mu.Unlock()
		c.wg.Wait()

		return c.val, c.err
	}

	c := &call{}
	c.wg.Add(1)
	rc.flight[key] = c
	rc.mu.Unlock()

	resp, err := doFn()
	if err != nil {
		c.err = err
	} else {
		c.val, c.err = captureResponse(resp)
	}

	c.wg.Done()

	rc.mu.Lock()
	delete(rc.flight, key)
	rc.mu.Unlock()

	return c.val, c.err
}

// CoalesceKey generates a coalescing key from a request.
// Only GET and HEAD requests are coalesced.
func CoalesceKey(r *http.Request, targetURL string) (string, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return "", false
	}

	return r.Method + ":" + targetURL + r.URL.RequestURI(), true
}

func captureResponse(resp *http.Response) (*CoalescedResponse, error) {
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	cr := &CoalescedResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       body,
	}

	// Reset the body so the original response can still be used
	resp.Body = io.NopCloser(bytes.NewReader(body))

	return cr, nil
}

// WriteCoalescedResponse writes a coalesced response to an http.ResponseWriter.
func WriteCoalescedResponse(w http.ResponseWriter, cr *CoalescedResponse) {
	for k, vals := range cr.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}

	w.WriteHeader(cr.StatusCode)
	w.Write(cr.Body) //nolint:errcheck
}
