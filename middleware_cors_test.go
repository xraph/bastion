package bastion

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayMiddleware_CORSCacheVariesByOrigin(t *testing.T) {
	config := Config{CORS: CORSConfig{
		Enabled:      true,
		AllowOrigins: []string{"http://localhost:3000", "http://localhost:3330"},
		AllowMethods: []string{http.MethodGet, http.MethodOptions},
		AllowHeaders: []string{"Authorization"},
		AllowCreds:   true,
	}}

	for _, method := range []string{http.MethodGet, http.MethodOptions} {
		for _, tc := range []struct {
			name        string
			origin      string
			allowOrigin string
		}{
			{"app", "http://localhost:3000", "http://localhost:3000"},
			{"studio", "http://localhost:3330", "http://localhost:3330"},
			{"no_origin", "", ""},
			{"disallowed_origin", "https://other.example", ""},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				handler := GatewayMiddleware(config, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodOptions {
						t.Error("preflight must not reach the tile handler")
					}
					w.Header().Set("Content-Type", "image/png")
					w.Header().Set("Cache-Control", "public, max-age=86400, must-revalidate")
					w.WriteHeader(http.StatusOK)
				}))
				request := httptest.NewRequest(method, "/twinos/api/v1/atlas/proxy/asset/2/2/1.png", nil)
				if tc.origin != "" {
					request.Header.Set("Origin", tc.origin)
				}
				if method == http.MethodOptions {
					request.Header.Set("Access-Control-Request-Method", http.MethodGet)
					request.Header.Set("Access-Control-Request-Headers", "Authorization")
				}
				response := httptest.NewRecorder()
				response.Header().Add("Vary", "Accept-Language")
				response.Header().Add("Vary", "X-Locale")

				handler.ServeHTTP(response, request)

				wantStatus := http.StatusOK
				if method == http.MethodOptions {
					wantStatus = http.StatusNoContent
				} else if got := response.Header().Get("Cache-Control"); got != "public, max-age=86400, must-revalidate" {
					t.Errorf("tile cache policy = %q", got)
				}
				if response.Code != wantStatus {
					t.Errorf("status = %d, want %d", response.Code, wantStatus)
				}
				if got := response.Header().Get("Access-Control-Allow-Origin"); got != tc.allowOrigin {
					t.Errorf("allowed origin = %q, want %q", got, tc.allowOrigin)
				}
				if tc.allowOrigin != "" {
					if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
						t.Errorf("allow credentials = %q, want true", got)
					}
					if method == http.MethodOptions && response.Header().Get("Access-Control-Allow-Headers") != "Authorization" {
						t.Error("preflight must continue allowing Authorization")
					}
				}

				vary := make(map[string]bool)
				for _, line := range response.Header().Values("Vary") {
					for field := range strings.SplitSeq(line, ",") {
						vary[strings.ToLower(strings.TrimSpace(field))] = true
					}
				}
				for _, field := range []string{"origin", "accept-language", "x-locale"} {
					if !vary[field] {
						t.Errorf("Vary = %v; missing %s permits cache reuse across that request header", response.Header().Values("Vary"), field)
					}
				}
			})
		}
	}
}
