package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	bastion "github.com/xraph/bastion"
)

// errUpstreamWriteTimeout cancels an upstream request whose body took longer
// than the route's write timeout to send.
var errUpstreamWriteTimeout = errors.New("upstream write timeout")

// newHTTPTransport builds the transport the HTTP proxy sends requests
// through. Connect bounds the dial, Read bounds the wait for the upstream's
// response headers once the request is sent, and Idle is how long a pooled
// connection may sit unused. A zero value leaves that bound off.
func newHTTPTransport(t bastion.TimeoutConfig, tlsConfig *tls.Config) *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   t.Connect,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsConfig,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       t.Idle,
		ResponseHeaderTimeout: t.Read,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

// routeTimeouts returns the timeouts an HTTP request on route runs with. A
// field the route leaves at zero takes the gateway-wide value. ok is false
// when the route sets no timeout block, and the caller keeps the shared
// transport.
//
// Write is the exception: it comes from the route alone. The gateway-wide
// Write was never enforced, and it defaults to 30s, so falling back to it
// would start cutting off uploads on every route that only set a read
// timeout.
func routeTimeouts(route *bastion.Route, global bastion.TimeoutConfig) (bastion.TimeoutConfig, bool) {
	if route == nil || route.Timeout == nil {
		return global, false
	}

	rt := *route.Timeout
	eff := bastion.TimeoutConfig{
		Connect: global.Connect,
		Read:    global.Read,
		Idle:    global.Idle,
		Write:   rt.Write,
	}

	if rt.Connect > 0 {
		eff.Connect = rt.Connect
	}

	if rt.Read > 0 {
		eff.Read = rt.Read
	}

	if rt.Idle > 0 {
		eff.Idle = rt.Idle
	}

	return eff, true
}

// transportCache holds one transport per distinct set of transport-level
// timeouts, so routes that share timeouts share a connection pool.
type transportCache struct {
	mu    sync.Mutex
	byKey map[bastion.TimeoutConfig]*http.Transport
}

// transportFor returns the transport for an HTTP request on route. A route
// with no timeout block, or one whose timeouts match the gateway-wide ones,
// gets the shared transport, exactly as before per-route timeouts were read.
func (pe *Engine) transportFor(route *bastion.Route) *http.Transport {
	eff, ok := routeTimeouts(route, pe.config.Timeouts)
	if !ok {
		return pe.transport
	}

	// Write isn't a transport setting, so it doesn't split the pool.
	key := bastion.TimeoutConfig{Connect: eff.Connect, Read: eff.Read, Idle: eff.Idle}
	if key == (bastion.TimeoutConfig{Connect: pe.config.Timeouts.Connect, Read: pe.config.Timeouts.Read, Idle: pe.config.Timeouts.Idle}) {
		return pe.transport
	}

	pe.routeTransports.mu.Lock()
	defer pe.routeTransports.mu.Unlock()

	if tr, found := pe.routeTransports.byKey[key]; found {
		return tr
	}

	if pe.routeTransports.byKey == nil {
		pe.routeTransports.byKey = make(map[bastion.TimeoutConfig]*http.Transport)
	}

	tr := newHTTPTransport(key, pe.transport.TLSClientConfig)
	pe.routeTransports.byKey[key] = tr

	return tr
}

// withWriteTimeout bounds how long sending the request body upstream may
// take. If the body hasn't been read to the end, or closed, within d, the
// upstream request is cancelled and the body stops returning data. Requests
// without a body, and a zero d, are returned unchanged. The returned stop
// must be called once the request is done.
func withWriteTimeout(r *http.Request, d time.Duration) (*http.Request, func()) {
	if d <= 0 || r.Body == nil || r.Body == http.NoBody {
		return r, func() {}
	}

	ctx, cancel := context.WithCancelCause(r.Context())
	expired := make(chan struct{})

	timer := time.AfterFunc(d, func() {
		close(expired)
		cancel(errUpstreamWriteTimeout)
	})

	out := r.WithContext(ctx)
	out.Body = &sentBody{rc: r.Body, expired: expired, done: func() { timer.Stop() }}

	return out, func() {
		timer.Stop()
		cancel(nil)
	}
}

// sentBody is the request body on its way upstream. It reports when the
// body has been fully read or closed, which is when the proxy has finished
// sending it, and it gives up once the write timeout expires.
//
// Giving up needs care. Go's transport doesn't return from RoundTrip while
// it is still reading the request body, even after the request's context
// is cancelled, so a client that stalls mid-upload would hold the proxy as
// long as it liked. Each Read therefore runs the underlying read in its own
// goroutine, into a buffer of its own, and stops waiting for it when the
// timeout expires. An abandoned read finishes, or fails, when the client
// sends more or the server closes the connection; its data is dropped.
type sentBody struct {
	rc      io.ReadCloser
	expired <-chan struct{}

	once sync.Once
	done func()

	// pending is the read in flight, kept across calls only after the
	// timeout expired, when it is never read from again.
	pending chan readResult
	// buf is reused while every read completes. A read that was abandoned
	// keeps its buffer, but by then Read only returns the timeout error.
	buf []byte
}

type readResult struct {
	buf []byte
	err error
}

func (b *sentBody) Read(p []byte) (int, error) {
	select {
	case <-b.expired:
		return 0, errUpstreamWriteTimeout
	default:
	}

	if b.pending == nil {
		ch := make(chan readResult, 1)
		b.pending = ch

		if cap(b.buf) < len(p) {
			b.buf = make([]byte, len(p))
		}

		buf := b.buf[:len(p)]

		go func() {
			n, err := b.rc.Read(buf)
			ch <- readResult{buf: buf[:n], err: err}
		}()
	}

	select {
	case res := <-b.pending:
		b.pending = nil
		n := copy(p, res.buf)

		if res.err != nil {
			b.once.Do(b.done)
		}

		return n, res.err
	case <-b.expired:
		return 0, errUpstreamWriteTimeout
	}
}

// Close closes the client's body. After the timeout it does so in the
// background, since an abandoned read may still hold the body and closing
// would wait for it.
func (b *sentBody) Close() error {
	b.once.Do(b.done)

	select {
	case <-b.expired:
		go func() { _ = b.rc.Close() }()

		return nil
	default:
		return b.rc.Close()
	}
}
