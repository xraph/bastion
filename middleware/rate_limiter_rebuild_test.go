package middleware

import (
	"net/http/httptest"
	"testing"
)

func TestRateLimiter_AllowWithConfig_RebuildsWhenTheConfigChanges(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{Enabled: true, RequestsPerSec: 100, Burst: 50})
	req := httptest.NewRequest("GET", "/api/test", nil)

	tight := &RateLimitConfig{Enabled: true, RequestsPerSec: 1, Burst: 1}
	if !rl.AllowWithConfig(req, tight) {
		t.Fatal("first request should be allowed")
	}

	if rl.AllowWithConfig(req, tight) {
		t.Fatal("second request should be limited")
	}

	loose := &RateLimitConfig{Enabled: true, RequestsPerSec: 1000, Burst: 1000}
	if !rl.AllowWithConfig(req, loose) {
		t.Error("an edited rate limit did not take effect")
	}
}
