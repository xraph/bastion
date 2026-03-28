package security

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// newWAFRequest creates a request with the raw query string set directly,
// bypassing httptest.NewRequest's URL parser which panics on spaces.
func newWAFRequest(method, path, rawQuery string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.URL.RawQuery = rawQuery

	return req
}

func TestWAF_SQLInjection(t *testing.T) {
	waf := NewWAF(WAFConfig{Enabled: true, BlockSQLi: true})

	tests := []struct {
		name    string
		path    string
		query   string // will be URL-encoded
		blocked bool
	}{
		{"normal request", "/api/users", "id=1", false},
		{"union select", "/api/users", "id=1 UNION SELECT * FROM users", true},
		{"or 1=1", "/api/users", "id=' OR '1'='1", true},
		{"drop table", "/api/users", "id=1; DROP TABLE users", true},
		{"comment injection", "/api/users", "id=1-- ", true},
		{"normal with numbers", "/api/users", "page=1&limit=10", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newWAFRequest("GET", tt.path, url.PathEscape(tt.query))
			v := waf.Check(req)

			if tt.blocked && v == nil {
				t.Error("expected request to be blocked")
			}
			if !tt.blocked && v != nil {
				t.Errorf("expected request to pass, got: %s", v.Message)
			}
		})
	}
}

func TestWAF_XSS(t *testing.T) {
	waf := NewWAF(WAFConfig{Enabled: true, BlockXSS: true})

	tests := []struct {
		name    string
		query   string
		blocked bool
	}{
		{"normal", "name=john", false},
		{"script tag", "name=<script>alert(1)</script>", true},
		{"javascript:", "url=javascript:alert(1)", true},
		{"onerror", "img=x onerror=alert(1)", true},
		{"document.cookie", "x=document.cookie", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newWAFRequest("GET", "/api", url.PathEscape(tt.query))
			v := waf.Check(req)

			if tt.blocked && v == nil {
				t.Error("expected request to be blocked")
			}
			if !tt.blocked && v != nil {
				t.Errorf("expected request to pass, got: %s", v.Message)
			}
		})
	}
}

func TestWAF_PathTraversal(t *testing.T) {
	waf := NewWAF(WAFConfig{Enabled: true, BlockPathTraversal: true})

	tests := []struct {
		name    string
		path    string
		blocked bool
	}{
		{"normal path", "/api/users/123", false},
		{"dot dot slash", "/api/../../../etc/passwd", true},
		{"encoded traversal", "/api/%2e%2e/secrets", true},
		{"etc passwd", "/etc/passwd", true},
		{"proc self", "/proc/self/environ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			v := waf.Check(req)

			if tt.blocked && v == nil {
				t.Error("expected request to be blocked")
			}
			if !tt.blocked && v != nil {
				t.Errorf("expected request to pass, got: %s", v.Message)
			}
		})
	}
}

func TestWAF_BodySizeLimit(t *testing.T) {
	waf := NewWAF(WAFConfig{Enabled: true, MaxBodySize: 1024})

	req := httptest.NewRequest("POST", "/api/upload", nil)
	req.ContentLength = 2048

	v := waf.Check(req)
	if v == nil {
		t.Error("expected body size violation")
	}

	if v.Rule != "body-size" {
		t.Errorf("expected rule 'body-size', got %q", v.Rule)
	}
}

func TestWAF_CustomRules(t *testing.T) {
	waf := NewWAF(WAFConfig{
		Enabled: true,
		CustomRules: []WAFRule{
			{
				Name:    "block-admin",
				Pattern: `(?i)/admin`,
				Target:  "path",
				Action:  "block",
			},
			{
				Name:    "block-debug-header",
				Pattern: `(?i)^true$`,
				Target:  "header:X-Debug",
				Action:  "block",
			},
		},
	})

	// Path rule
	req := httptest.NewRequest("GET", "/admin/settings", nil)
	v := waf.Check(req)
	if v == nil || v.Rule != "block-admin" {
		t.Error("expected admin path to be blocked")
	}

	// Header rule
	req2 := httptest.NewRequest("GET", "/api/users", nil)
	req2.Header.Set("X-Debug", "true")
	v2 := waf.Check(req2)
	if v2 == nil || v2.Rule != "block-debug-header" {
		t.Error("expected debug header to be blocked")
	}

	// Normal request
	req3 := httptest.NewRequest("GET", "/api/users", nil)
	v3 := waf.Check(req3)
	if v3 != nil {
		t.Errorf("expected normal request to pass, got: %s", v3.Message)
	}
}

func TestWAF_Disabled(t *testing.T) {
	waf := NewWAF(WAFConfig{Enabled: false, BlockSQLi: true})

	req := newWAFRequest("GET", "/api", url.PathEscape("id=1 UNION SELECT * FROM users"))
	v := waf.Check(req)

	if v != nil {
		t.Error("disabled WAF should not block")
	}
}

func TestWAFPlugin_CheckRequest(t *testing.T) {
	p := NewWAFPlugin(WAFConfig{
		Enabled:   true,
		BlockSQLi: true,
	})

	req := newWAFRequest("GET", "/api", url.PathEscape("id=1 UNION SELECT * FROM users"))

	err := p.CheckRequest(req)
	if err == nil {
		t.Error("expected WAF plugin to reject SQL injection")
	}
}
