package proxy

import (
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
)

// TransportPoolConfig configures per-upstream HTTP transport pools.
type TransportPoolConfig struct {
	// MaxIdleConns is the maximum number of idle connections across all hosts.
	MaxIdleConns int `json:"maxIdleConns" yaml:"max_idle_conns"`

	// MaxIdleConnsPerHost is the maximum idle connections per upstream host.
	MaxIdleConnsPerHost int `json:"maxIdleConnsPerHost" yaml:"max_idle_conns_per_host"`

	// MaxConnsPerHost limits total connections per host (0 = unlimited).
	MaxConnsPerHost int `json:"maxConnsPerHost" yaml:"max_conns_per_host"`

	// IdleConnTimeout is how long idle connections stay in the pool.
	IdleConnTimeout time.Duration `json:"idleConnTimeout" yaml:"idle_conn_timeout"`

	// ConnectTimeout is the timeout for establishing new connections.
	ConnectTimeout time.Duration `json:"connectTimeout" yaml:"connect_timeout"`

	// ResponseHeaderTimeout is the timeout for reading response headers.
	ResponseHeaderTimeout time.Duration `json:"responseHeaderTimeout" yaml:"response_header_timeout"`

	// ForceHTTP2 enables HTTP/2 for upstream connections.
	ForceHTTP2 bool `json:"forceHttp2" yaml:"force_http2"`
}

// TransportPool manages per-upstream HTTP transports with configurable connection pooling.
type TransportPool struct {
	mu         sync.RWMutex
	transports map[string]*http.Transport
	defaults   TransportPoolConfig
	tlsConfig  *tls.Config
}

// NewTransportPool creates a new transport pool with defaults.
func NewTransportPool(config TransportPoolConfig, tlsCfg *tls.Config) *TransportPool {
	if config.MaxIdleConns <= 0 {
		config.MaxIdleConns = 200
	}

	if config.MaxIdleConnsPerHost <= 0 {
		config.MaxIdleConnsPerHost = 50
	}

	if config.IdleConnTimeout <= 0 {
		config.IdleConnTimeout = 90 * time.Second
	}

	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 10 * time.Second
	}

	if config.ResponseHeaderTimeout <= 0 {
		config.ResponseHeaderTimeout = 30 * time.Second
	}

	return &TransportPool{
		transports: make(map[string]*http.Transport),
		defaults:   config,
		tlsConfig:  tlsCfg,
	}
}

// Get returns an HTTP transport for the given target, creating one if necessary.
// Each target gets its own transport with independent connection pool settings.
func (tp *TransportPool) Get(targetID string) *http.Transport {
	tp.mu.RLock()
	t, ok := tp.transports[targetID]
	tp.mu.RUnlock()

	if ok {
		return t
	}

	return tp.create(targetID)
}

// Default returns the default shared transport.
func (tp *TransportPool) Default() *http.Transport {
	return tp.Get("_default")
}

// Remove closes idle connections and removes the transport for a target.
func (tp *TransportPool) Remove(targetID string) {
	tp.mu.Lock()
	t, ok := tp.transports[targetID]

	if ok {
		delete(tp.transports, targetID)
	}

	tp.mu.Unlock()

	if ok {
		t.CloseIdleConnections()
	}
}

// CloseAll closes all transports and their idle connections.
func (tp *TransportPool) CloseAll() {
	tp.mu.Lock()
	transports := tp.transports
	tp.transports = make(map[string]*http.Transport)
	tp.mu.Unlock()

	for _, t := range transports {
		t.CloseIdleConnections()
	}
}

func (tp *TransportPool) create(targetID string) *http.Transport {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	// Double-check
	if t, ok := tp.transports[targetID]; ok {
		return t
	}

	var tlsCfg *tls.Config
	if tp.tlsConfig != nil {
		tlsCfg = tp.tlsConfig.Clone()
	} else {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	t := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   tp.defaults.ConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsCfg,
		MaxIdleConns:          tp.defaults.MaxIdleConns,
		MaxIdleConnsPerHost:   tp.defaults.MaxIdleConnsPerHost,
		MaxConnsPerHost:       tp.defaults.MaxConnsPerHost,
		IdleConnTimeout:       tp.defaults.IdleConnTimeout,
		ResponseHeaderTimeout: tp.defaults.ResponseHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     tp.defaults.ForceHTTP2,
	}

	tp.transports[targetID] = t

	return t
}
