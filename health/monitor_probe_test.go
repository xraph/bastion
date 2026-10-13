package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xraph/forge"
)

type probeTarget struct {
	id, url, path string
	healthy       bool
}

func (t *probeTarget) GetID() string      { return t.id }
func (t *probeTarget) GetURL() string     { return t.url }
func (t *probeTarget) IsHealthy() bool    { return t.healthy }
func (t *probeTarget) SetHealthy(h bool)  { t.healthy = h }
func (t *probeTarget) HealthPath() string { return t.path }

// upstream answers 200 on /healthz, 503 on /down and 404 on everything
// else, including DefaultPath. It records the last path it was asked for.
func upstream(t *testing.T, seen *string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = r.URL.Path

		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/down":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestMonitor_ProbeHealthPath(t *testing.T) {
	cases := []struct {
		name       string
		configPath string
		targetPath string
		wantPath   string
		wantUp     bool
	}{
		{
			name:     "default path, upstream does not serve it",
			wantPath: DefaultPath,
			wantUp:   true,
		},
		{
			name:       "explicit config path that 404s",
			configPath: DefaultPath,
			wantPath:   DefaultPath,
			wantUp:     false,
		},
		{
			name:       "explicit config path that answers",
			configPath: "/healthz",
			wantPath:   "/healthz",
			wantUp:     true,
		},
		{
			name:       "target path that 404s",
			targetPath: "/missing",
			wantPath:   "/missing",
			wantUp:     false,
		},
		{
			name:       "target path wins over config path",
			configPath: "/missing",
			targetPath: "/healthz",
			wantPath:   "/healthz",
			wantUp:     true,
		},
		{
			name:       "explicit path reporting 503",
			configPath: "/down",
			wantPath:   "/down",
			wantUp:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			srv := upstream(t, &seen)

			hm := NewMonitor(Config{
				Enabled:          true,
				Interval:         time.Second,
				Timeout:          time.Second,
				Path:             tc.configPath,
				FailureThreshold: 3,
				SuccessThreshold: 1,
			}, forge.NewNoopLogger())

			target := &probeTarget{id: "t", url: srv.URL, path: tc.targetPath, healthy: true}
			hm.Register("route", target)

			mt := hm.targets["t"]
			for range 3 {
				hm.checkTarget(context.Background(), mt)
			}

			if seen != tc.wantPath {
				t.Errorf("probed %q, want %q", seen, tc.wantPath)
			}
			if target.IsHealthy() != tc.wantUp {
				t.Errorf("healthy after 3 probes = %v, want %v", target.IsHealthy(), tc.wantUp)
			}
		})
	}
}

// A 5xx on the implicit path still takes the target down: a Forge service
// that serves DefaultPath and reports 503 is genuinely unhealthy.
func TestProbeHealthy_ImplicitPathFailsOn5xx(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		if probeHealthy(status, false) {
			t.Errorf("probeHealthy(%d, implicit) = true, want false", status)
		}
	}

	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusNotFound, http.StatusMethodNotAllowed} {
		if !probeHealthy(status, false) {
			t.Errorf("probeHealthy(%d, implicit) = false, want true", status)
		}
	}
}

// A connection that is refused is a failure whatever the path.
func TestMonitor_ProbeUnreachableDefaultPath(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	hm := NewMonitor(Config{
		Enabled:          true,
		Interval:         time.Second,
		Timeout:          time.Second,
		FailureThreshold: 3,
		SuccessThreshold: 1,
	}, forge.NewNoopLogger())

	target := &probeTarget{id: "t", url: url, healthy: true}
	hm.Register("route", target)

	for range 3 {
		hm.checkTarget(context.Background(), hm.targets["t"])
	}

	if target.IsHealthy() {
		t.Fatal("unreachable target still healthy after 3 failed probes")
	}
}
