package proxy

import (
	"net/http"
	"strings"

	bastion "github.com/xraph/bastion"
)

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
