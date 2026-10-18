package bastion

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xraph/bastion/observability"
)

// PathRewriter rewrites request paths according to route configuration.
type PathRewriter struct{}

// RewritePath returns the path to send upstream for a request on route.
// It applies the route's rules in this order: StripPrefix removes the
// route's path prefix, AddPrefix is put in front, and then RewritePath, a
// "pattern:replacement" regex rule, runs on the result. The rule splits at
// the first colon, so the pattern can't contain one. An invalid rule
// leaves the path as it is.
func RewritePath(path string, route *Route) string {
	result := path

	// Strip prefix
	if route.StripPrefix && route.Path != "" {
		prefix := strings.TrimSuffix(route.Path, "/*")
		prefix = strings.TrimSuffix(prefix, "/*")
		result = strings.TrimPrefix(result, prefix)

		if result == "" {
			result = "/"
		}
	}

	// Add prefix
	if route.AddPrefix != "" {
		result = route.AddPrefix + result
	}

	// Regex rewrite
	if route.RewritePath != "" {
		result = applyRegexRewrite(result, route.RewritePath)
	}

	return result
}

// rewriteRules caches compiled rewrite patterns by rule, since the same
// few rules run on every request. A rule whose pattern doesn't compile is
// cached as nil.
var rewriteRules sync.Map // rule string -> *regexp.Regexp

// applyRegexRewrite applies a regex rewrite rule.
// Format: "pattern:replacement" (e.g., "/v1/(.*):/$1")
func applyRegexRewrite(path, rule string) string {
	parts := strings.SplitN(rule, ":", 2)
	if len(parts) != 2 {
		return path
	}

	var re *regexp.Regexp

	if cached, ok := rewriteRules.Load(rule); ok {
		re, _ = cached.(*regexp.Regexp)
	} else {
		re, _ = regexp.Compile(parts[0])
		rewriteRules.Store(rule, re)
	}

	if re == nil {
		return path
	}

	return re.ReplaceAllString(path, parts[1])
}

// GatewayMiddleware wraps http.Handler with gateway-level middleware.
func GatewayMiddleware(config Config, next http.Handler) http.Handler {
	return gatewayMiddleware(config, next, nil)
}

// gatewayMiddleware is GatewayMiddleware reporting the requests it turns
// away to onReject, which may be nil.
func gatewayMiddleware(config Config, next http.Handler, onReject func(*http.Request, observability.RejectReason)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS handling
		if config.CORS.Enabled {
			handleCORS(w, r, config.CORS)

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)

				return
			}
		}

		// IP filtering
		if config.IPFilter.Enabled {
			if !allowIP(r.RemoteAddr, config.IPFilter) {
				if onReject != nil {
					onReject(r, observability.RejectIPFiltered)
				}

				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)

				return
			}
		}

		// Request ID injection
		if r.Header.Get("X-Request-ID") == "" {
			// Generate a simple request ID if not present
			r.Header.Set("X-Request-ID", generateRequestID())
		}

		w.Header().Set("X-Request-ID", r.Header.Get("X-Request-ID"))

		next.ServeHTTP(w, r)
	})
}

func handleCORS(w http.ResponseWriter, r *http.Request, config CORSConfig) {
	// Even absent or disallowed origins produce a different CORS response.
	// Append so other middleware's cache dimensions remain intact.
	w.Header().Add("Vary", "Origin")

	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}

	allowed := false

	for _, o := range config.AllowOrigins {
		if o == "*" || o == origin {
			allowed = true

			break
		}
	}

	if !allowed {
		return
	}

	if config.AllowCreds {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	} else {
		w.Header().Set("Access-Control-Allow-Origin", strings.Join(config.AllowOrigins, ","))
	}

	w.Header().Set("Access-Control-Allow-Methods", strings.Join(config.AllowMethods, ","))
	w.Header().Set("Access-Control-Allow-Headers", strings.Join(config.AllowHeaders, ","))

	if len(config.ExposeHeaders) > 0 {
		w.Header().Set("Access-Control-Expose-Headers", strings.Join(config.ExposeHeaders, ","))
	}

	if config.AllowCreds {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}

	if config.MaxAge > 0 {
		w.Header().Set("Access-Control-Max-Age", strconv.Itoa(config.MaxAge))
	}
}

func allowIP(remoteAddr string, config IPFilterConfig) bool {
	host, _, err := splitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	// Check deny list first
	for _, denied := range config.DenyIPs {
		if matchIPPattern(host, denied) {
			return false
		}
	}

	// If allow list is set, must be in it
	if len(config.AllowIPs) > 0 {
		for _, allowed := range config.AllowIPs {
			if matchIPPattern(host, allowed) {
				return true
			}
		}

		return false
	}

	return true
}

func matchIPPattern(ip, pattern string) bool {
	// Exact match
	if ip == pattern {
		return true
	}

	// CIDR match
	if strings.Contains(pattern, "/") {
		// Simple prefix check for CIDR-like patterns
		prefix := strings.Split(pattern, "/")[0]

		return strings.HasPrefix(ip, prefix)
	}

	// Wildcard match
	if strings.Contains(pattern, "*") {
		pattern = strings.ReplaceAll(pattern, ".", "\\.")
		pattern = strings.ReplaceAll(pattern, "*", ".*")

		re, err := regexp.Compile("^" + pattern + "$")
		if err != nil {
			return false
		}

		return re.MatchString(ip)
	}

	return false
}

func splitHostPort(addr string) (string, string, error) {
	if strings.Contains(addr, ":") {
		parts := strings.SplitN(addr, ":", 2)

		return parts[0], parts[1], nil
	}

	return addr, "", nil
}

func generateRequestID() string {
	return strings.ReplaceAll(
		time.Now().Format("20060102150405.000000000"),
		".", "")
}
