package proxy

import (
	"net/http"
	"net/url"
	"strings"

	bastion "github.com/xraph/bastion"
)

// forwardPath returns the request path to send upstream, with any
// trailing slash the client sent put back.
//
// Forge's router trims trailing slashes from r.URL.Path before Bastion
// sees the request (so "/docs/" matches a route registered as "/docs").
// Routing on the trimmed path is fine, but the upstream should receive
// exactly what the client asked for: plenty of servers treat "/docs" and
// "/docs/" differently, and some redirect one to the other. RequestURI is
// left untouched by Forge, so the slashes are recovered from there.
//
// The path is only extended when the trimmed RequestURI path still ends
// with r.URL.Path, so a request whose path something else rewrote is
// forwarded as it stands.
func forwardPath(r *http.Request) string {
	p := r.URL.Path
	if r.RequestURI == "" || strings.HasSuffix(p, "/") {
		return p
	}

	raw := r.RequestURI
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}

	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return p
	}

	orig := u.Path
	trimmed := strings.TrimRight(orig, "/")

	if trimmed == orig || !strings.HasSuffix(trimmed, p) {
		return p
	}

	return p + orig[len(trimmed):]
}

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")

	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}

	return a + b
}

// schemeFromRequest determines the request scheme (http or https).
func schemeFromRequest(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}

	return "http"
}

// applyHeaderPolicy applies a header policy to an HTTP request.
func applyHeaderPolicy(req *http.Request, policy bastion.HeaderPolicy) {
	for k, v := range policy.Add {
		req.Header.Add(k, v)
	}

	for k, v := range policy.Set {
		req.Header.Set(k, v)
	}

	for _, k := range policy.Remove {
		req.Header.Del(k)
	}
}
