package proxy

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/xraph/forge"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/internal/telemetry"
	"github.com/xraph/bastion/observability"
)

// ProxyWebSocket proxies a WebSocket connection to an upstream target.
func ProxyWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	route *bastion.Route,
	target *bastion.Target,
	config bastion.WebSocketConfig,
	logger forge.Logger,
) {
	proxyWebSocket(w, r, route, target, config, logger, nil)
}

// proxyWebSocket is ProxyWebSocket recording into m, which may be nil.
func proxyWebSocket(
	w http.ResponseWriter,
	r *http.Request,
	route *bastion.Route,
	target *bastion.Target,
	config bastion.WebSocketConfig,
	logger forge.Logger,
	m *observability.Metrics,
) {
	// Parse upstream URL
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		logger.Warn("invalid upstream URL for websocket",
			forge.F("target_url", target.URL),
			forge.F("error", err),
		)

		http.Error(w, `{"error":"invalid upstream URL"}`, http.StatusBadGateway)

		return
	}

	// Build upstream WebSocket URL
	wsScheme := "ws"
	if targetURL.Scheme == "https" {
		wsScheme = "wss"
	}

	// strip_prefix, then add_prefix, then rewrite_path.
	upstreamPath := bastion.RewritePath(forwardPath(r), route)

	upstreamURL := wsScheme + "://" + targetURL.Host + singleJoiningSlash(targetURL.Path, upstreamPath)
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// Upgrade client connection
	clientConn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		logger.Warn("websocket client upgrade failed",
			forge.F("error", err),
		)

		return
	}

	defer func() { _ = clientConn.Close() }()

	// Connect to upstream
	header := http.Header{
		"X-Forwarded-For":   {r.RemoteAddr},
		"X-Forwarded-Host":  {r.Host},
		"X-Forwarded-Proto": {schemeFromRequest(r)},
	}

	upstreamConn, err := dialWebSocket(r, upstreamURL, header, config, route, m)
	if err != nil {
		logger.Warn("websocket upstream connect failed",
			forge.F("upstream_url", upstreamURL),
			forge.F("error", err),
		)

		// Send close frame to client
		_ = ws.WriteFrame(clientConn, ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusProtocolError, "upstream unavailable")))

		return
	}

	defer func() { _ = upstreamConn.Close() }()

	target.IncrConns()
	defer target.DecrConns()

	logger.Debug("websocket connection established",
		forge.F("route_id", route.ID),
		forge.F("upstream_url", upstreamURL),
	)

	// Bidirectional copy
	var wg sync.WaitGroup
	wg.Add(2)

	errCh := make(chan error, 2)

	// Client -> Upstream
	go func() {
		defer wg.Done()

		err := copyWebSocket(upstreamConn, clientConn, ws.StateServerSide)
		errCh <- err
	}()

	// Upstream -> Client
	go func() {
		defer wg.Done()

		err := copyWebSocket(clientConn, upstreamConn, ws.StateClientSide)
		errCh <- err
	}()

	// Wait for either direction to finish
	<-errCh

	// Close both connections to unblock the other goroutine
	_ = clientConn.Close()
	_ = upstreamConn.Close()

	wg.Wait()

	logger.Debug("websocket connection closed",
		forge.F("route_id", route.ID),
	)
}

// copyWebSocket copies WebSocket frames from src to dst.
func copyWebSocket(dst net.Conn, src net.Conn, state ws.State) error {
	for {
		header, err := ws.ReadHeader(src)
		if err != nil {
			return err
		}

		// Handle close frames
		if header.OpCode == ws.OpClose {
			// Forward close frame
			payload := make([]byte, header.Length)
			if header.Length > 0 {
				if _, err := io.ReadFull(src, payload); err != nil {
					return err
				}

				if header.Masked {
					ws.Cipher(payload, header.Mask, 0)
				}
			}

			// Send close frame to the other end
			closeFrame := ws.NewCloseFrame(ws.NewCloseFrameBody(ws.StatusNormalClosure, ""))
			if state == ws.StateServerSide {
				closeFrame = ws.MaskFrameInPlace(closeFrame)
			}

			_ = ws.WriteFrame(dst, closeFrame)

			return nil
		}

		// Handle ping/pong
		if header.OpCode == ws.OpPing {
			payload := make([]byte, header.Length)
			if header.Length > 0 {
				if _, err := io.ReadFull(src, payload); err != nil {
					return err
				}

				if header.Masked {
					ws.Cipher(payload, header.Mask, 0)
				}
			}

			// Forward ping as pong
			frame := ws.NewPongFrame(payload)
			if state == ws.StateServerSide {
				frame = ws.MaskFrameInPlace(frame)
			}

			if err := ws.WriteFrame(dst, frame); err != nil {
				return err
			}

			continue
		}

		if header.OpCode == ws.OpPong {
			// Discard pong payload
			if header.Length > 0 {
				if _, err := io.CopyN(io.Discard, src, header.Length); err != nil {
					return err
				}
			}

			continue
		}

		// Forward data frames
		payload := make([]byte, header.Length)
		if header.Length > 0 {
			if _, err := io.ReadFull(src, payload); err != nil {
				return err
			}

			if header.Masked {
				ws.Cipher(payload, header.Mask, 0)
			}
		}

		// Write frame to destination
		msg := wsutil.Message{
			OpCode:  header.OpCode,
			Payload: payload,
		}

		if state == ws.StateServerSide {
			err = wsutil.WriteClientMessage(dst, msg.OpCode, msg.Payload)
		} else {
			err = wsutil.WriteServerMessage(dst, msg.OpCode, msg.Payload)
		}

		if err != nil {
			return err
		}
	}
}

// dialWebSocket opens the upstream WebSocket. The handshake gets a client
// span, child of the request's server span, and its trace context goes
// upstream in the handshake headers. The span ends with the handshake, not
// the connection, the same way Forge ends a stream's server span.
func dialWebSocket(
	r *http.Request,
	upstreamURL string,
	header http.Header,
	config bastion.WebSocketConfig,
	route *bastion.Route,
	m *observability.Metrics,
) (net.Conn, error) {
	ctx, span := telemetry.Start(r.Context(), spanProxyRequest, trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	if span.IsRecording() {
		span.SetAttributes(routeAttributes(route, observability.ProtocolWebSocket)...)
		span.SetAttributes(attribute.Int64("bastion.proxy.attempt", 1))

		if u, parseErr := url.Parse(upstreamURL); parseErr == nil {
			span.SetAttributes(telemetry.URLAttributes(http.MethodGet, u)...)
		}
	}

	propagateTraceContext(ctx, header)

	dialer := ws.Dialer{
		Timeout: config.HandshakeTimeout,
		Header:  ws.HandshakeHeaderHTTP(header),
	}

	start := time.Now()
	conn, _, _, err := dialer.Dial(ctx, upstreamURL)
	elapsed := time.Since(start)

	var statusErr ws.StatusError

	switch {
	case err == nil:
		m.ObserveUpstream(observability.ProtocolWebSocket, observability.StatusClass(http.StatusSwitchingProtocols, nil), elapsed)
		telemetry.RecordStatus(span, http.StatusSwitchingProtocols)
	case errors.As(err, &statusErr):
		// The upstream answered the handshake with something other than 101.
		m.ObserveUpstream(observability.ProtocolWebSocket, observability.StatusClass(int(statusErr), nil), elapsed)
		telemetry.RecordStatus(span, int(statusErr))
	default:
		m.ObserveUpstream(observability.ProtocolWebSocket, observability.StatusClass(0, err), elapsed)
		telemetry.RecordRoundTripError(span, err)
	}

	return conn, err
}
