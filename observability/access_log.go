package observability

import (
	"net/http"
	"strings"
	"time"

	"github.com/xraph/forge"
)

// AccessLogConfig holds access logging settings.
type AccessLogConfig struct {
	Enabled        bool     `json:"enabled" yaml:"enabled"`
	RedactHeaders  []string `json:"redactHeaders,omitempty" yaml:"redact_headers"`
	IncludeBody    bool     `json:"includeBody" yaml:"include_body"`
	MaxBodyLogSize int      `json:"maxBodyLogSize" yaml:"max_body_log_size"`
}

// RouteInfo holds the route metadata needed for access logging.
type RouteInfo struct {
	ID       string
	Path     string
	Protocol string
	Source   string
}

// TargetInfo holds the target metadata needed for access logging.
type TargetInfo struct {
	ID  string
	URL string
}

// AccessLogger provides structured access logging for gateway requests.
type AccessLogger struct {
	config AccessLogConfig
	logger forge.Logger
}

// NewAccessLogger creates a new access logger.
func NewAccessLogger(config AccessLogConfig, logger forge.Logger) *AccessLogger {
	return &AccessLogger{
		config: config,
		logger: logger,
	}
}

// Log logs a gateway request.
func (al *AccessLogger) Log(r *http.Request, statusCode int, latency time.Duration, route *RouteInfo, target *TargetInfo) {
	if !al.config.Enabled {
		return
	}

	fields := []forge.Field{
		forge.F("method", r.Method),
		forge.F("path", r.URL.Path),
		forge.F("status", statusCode),
		forge.F("latency_ms", float64(latency.Nanoseconds())/1e6),
		forge.F("client_ip", extractClientIP(r)),
		forge.F("user_agent", r.UserAgent()),
	}

	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		fields = append(fields, forge.F("request_id", requestID))
	}

	if route != nil {
		fields = append(fields,
			forge.F("route_id", route.ID),
			forge.F("route_path", route.Path),
			forge.F("route_protocol", route.Protocol),
			forge.F("route_source", route.Source),
		)
	}

	if target != nil {
		fields = append(fields,
			forge.F("upstream_url", target.URL),
			forge.F("upstream_id", target.ID),
		)
	}

	// Add query string (truncated)
	if query := r.URL.RawQuery; query != "" {
		if len(query) > 200 {
			query = query[:200] + "..."
		}

		fields = append(fields, forge.F("query", query))
	}

	// Add select headers (redacted as needed)
	al.addSafeHeaders(r, &fields)

	// Log at appropriate level based on status code
	switch {
	case statusCode >= 500:
		al.logger.Error("gateway request", fields...)
	case statusCode >= 400:
		al.logger.Warn("gateway request", fields...)
	default:
		al.logger.Info("gateway request", fields...)
	}
}

// LogAdminAction logs an admin API action.
func (al *AccessLogger) LogAdminAction(action, resource, result string, r *http.Request) {
	if !al.config.Enabled {
		return
	}

	al.logger.Info("gateway admin action",
		forge.F("action", action),
		forge.F("resource", resource),
		forge.F("result", result),
		forge.F("client_ip", extractClientIP(r)),
		forge.F("user_agent", r.UserAgent()),
	)
}

func (al *AccessLogger) addSafeHeaders(r *http.Request, fields *[]forge.Field) {
	safeHeaders := []string{"Accept", "Content-Type", "Content-Length", "Referer", "Origin"}

	for _, h := range safeHeaders {
		if v := r.Header.Get(h); v != "" {
			*fields = append(*fields, forge.F("header_"+strings.ToLower(h), v))
		}
	}

	// Add redacted versions of sensitive headers
	for _, h := range al.config.RedactHeaders {
		if r.Header.Get(h) != "" {
			*fields = append(*fields, forge.F("header_"+strings.ToLower(h), "[REDACTED]"))
		}
	}
}

func extractClientIP(r *http.Request) string {
	// Check X-Forwarded-For first
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)

		return strings.TrimSpace(parts[0])
	}

	// Check X-Real-IP
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	host, _, _ := splitHostPort(r.RemoteAddr)

	return host
}

func splitHostPort(addr string) (string, string, error) {
	if strings.Contains(addr, ":") {
		parts := strings.SplitN(addr, ":", 2)

		return parts[0], parts[1], nil
	}

	return addr, "", nil
}
