package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
)

const (
	// maxMirrorsInFlight caps the mirror copies being sent at once. When
	// the mirror is slower than live traffic, copies past the cap are
	// dropped rather than queued.
	maxMirrorsInFlight = 64

	// mirrorTimeout bounds one mirror copy, response included.
	mirrorTimeout = 30 * time.Second
)

// hopHeaders aren't copied onto a mirror request, as for any proxied one.
var hopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// SetTrafficRouter sets the router that applies a route's TrafficPolicy:
// which targets a request may go to, and where to mirror it. Without one
// every target of the route is a candidate and nothing is mirrored.
func (pe *Engine) SetTrafficRouter(t bastion.TrafficRouter) { pe.traffic = t }

// routeTargets returns the targets a request on route may go to. The
// traffic policy narrows them (a canary, a blue-green colour, an A/B
// group). When it leaves none, every target of the route stays a
// candidate, so a policy naming tags no target carries doesn't take the
// route down.
func (pe *Engine) routeTargets(r *http.Request, route *bastion.Route) []*bastion.Target {
	if pe.traffic == nil {
		return route.Targets
	}

	if targets := pe.traffic.FilterTargets(r, route); len(targets) > 0 {
		return targets
	}

	return route.Targets
}

// mirrorTarget returns the parsed mirror URL for route, or nil when the
// route doesn't mirror.
func (pe *Engine) mirrorTarget(route *bastion.Route) *url.URL {
	if pe.traffic == nil {
		return nil
	}

	raw := pe.traffic.ShouldMirror(route)
	if raw == "" {
		return nil
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		pe.logger.Warn("invalid mirror target, not mirroring",
			forge.F("route_id", route.ID),
			forge.F("mirror_target", raw),
		)

		return nil
	}

	return u
}

// mirror sends a copy of r to the mirror target in the background and
// returns at once. The copy gets the same path, query and headers the
// real upstream gets, and body, its own connection and a timeout of its
// own. Its response is read and dropped, and nothing about it reaches the
// client, the breakers, health or stats.
func (pe *Engine) mirror(r *http.Request, route *bastion.Route, target *url.URL, body []byte) {
	select {
	case pe.mirrorSlots <- struct{}{}:
	default:
		pe.logger.Debug("mirror busy, dropping copy",
			forge.F("route_id", route.ID),
		)

		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), mirrorTimeout)

	req := r.Clone(ctx)
	req.RequestURI = ""
	req.Body = http.NoBody
	req.ContentLength = 0
	req.GetBody = nil

	if len(body) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}

	for _, h := range hopHeaders {
		req.Header.Del(h)
	}

	pe.director(r, route, target)(req)

	go func() {
		defer func() { <-pe.mirrorSlots }()
		defer cancel()

		// #nosec G704 -- the mirror target comes from the route's
		// configured traffic policy; the client only shapes the path.
		resp, err := pe.transport.RoundTrip(req)
		if err != nil {
			pe.logger.Debug("mirror copy failed",
				forge.F("route_id", route.ID),
				forge.F("error", err),
			)

			return
		}

		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()
}
