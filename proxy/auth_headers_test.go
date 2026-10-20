package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	bastion "github.com/xraph/bastion"
)

// Every protocol builds its upstream request from the inbound one, and the
// gRPC path copies all of its headers. None of them may carry a client's
// identity headers upstream.
func TestEngine_DropsClientIdentityHeadersOnEveryProtocol(t *testing.T) {
	for _, tc := range []struct {
		protocol bastion.RouteProtocol
		method   string
		tls      bool
		prepare  func(*http.Request)
	}{
		{bastion.ProtocolHTTP, http.MethodGet, false, nil},
		{bastion.ProtocolSSE, http.MethodGet, false, func(r *http.Request) { r.Header.Set("Accept", "text/event-stream") }},
		{bastion.ProtocolGRPC, http.MethodPost, true, func(r *http.Request) { r.Header.Set("Content-Type", "application/grpc") }},
	} {
		t.Run(string(tc.protocol), func(t *testing.T) {
			var (
				mu   sync.Mutex
				seen http.Header
			)

			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = r.Header.Clone()
				mu.Unlock()

				if tc.protocol == bastion.ProtocolGRPC {
					w.Header().Set("Content-Type", "application/grpc")
					w.Header().Set("Trailer", "Grpc-Status")
					w.WriteHeader(http.StatusOK)
					w.Header().Set("Grpc-Status", "0")
				}
			}))

			cfg := bastion.Config{}

			if tc.tls {
				up.EnableHTTP2 = true
				up.StartTLS()

				cfg.TLS.InsecureSkipVerify = true
			} else {
				up.Start()
			}

			t.Cleanup(up.Close)

			tr := newTraced(t, cfg, &bastion.Route{
				ID:       "r-" + string(tc.protocol),
				Path:     "/svc/*",
				Targets:  []*bastion.Target{{ID: "t", URL: up.URL, Weight: 1, Healthy: true}},
				Protocol: tc.protocol,
			})

			req := httptest.NewRequest(tc.method, "/svc/x", strings.NewReader("\x00\x00\x00\x00\x00"))
			for _, h := range []string{"X-Auth-Subject", "X-Auth-Provider", "X-Auth-Scopes", "X-Auth-Role", "X-Auth-Email"} {
				req.Header.Set(h, "spoofed")
			}

			if tc.prepare != nil {
				tc.prepare(req)
			}

			tr.serve(httptest.NewRecorder(), req)

			mu.Lock()
			h := seen
			mu.Unlock()

			if h == nil {
				t.Fatal("the request never reached the upstream")
			}

			for name, values := range h {
				for _, v := range values {
					if v == "spoofed" {
						t.Errorf("upstream got %s: %s", name, v)
					}
				}
			}
		})
	}
}
