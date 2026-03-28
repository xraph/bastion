package security

import (
	"crypto/subtle"
	"net/http"
)

// AdminAuthConfig configures authentication for admin API endpoints.
type AdminAuthConfig struct {
	// Enabled enables admin API authentication.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// APIKeys is a list of valid API keys for admin access.
	APIKeys []string `json:"apiKeys,omitempty" yaml:"api_keys"`

	// Header is the header name containing the API key.
	// Defaults to "X-Admin-Key".
	Header string `json:"header,omitempty" yaml:"header"`
}

// AdminAuthMiddleware protects admin API endpoints with API key authentication.
type AdminAuthMiddleware struct {
	config AdminAuthConfig
}

// NewAdminAuthMiddleware creates a new admin auth middleware.
func NewAdminAuthMiddleware(config AdminAuthConfig) *AdminAuthMiddleware {
	if config.Header == "" {
		config.Header = "X-Admin-Key"
	}

	return &AdminAuthMiddleware{config: config}
}

// Wrap returns an http.Handler that requires admin authentication.
func (m *AdminAuthMiddleware) Wrap(next http.Handler) http.Handler {
	if !m.config.Enabled || len(m.config.APIKeys) == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(m.config.Header)
		if key == "" {
			http.Error(w, `{"error":"admin authentication required"}`, http.StatusUnauthorized)
			return
		}

		if !m.validateKey(key) {
			http.Error(w, `{"error":"invalid admin key"}`, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// WrapFunc is a convenience wrapper for http.HandlerFunc.
func (m *AdminAuthMiddleware) WrapFunc(next http.HandlerFunc) http.Handler {
	return m.Wrap(next)
}

func (m *AdminAuthMiddleware) validateKey(key string) bool {
	for _, valid := range m.config.APIKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(valid)) == 1 {
			return true
		}
	}

	return false
}
