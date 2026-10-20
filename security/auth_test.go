package security

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xraph/forge"
	"github.com/xraph/go-utils/log"
)

// lifecycleProvider counts how many of its kind are running, so a test can
// tell whether anything outlived Stop.
type lifecycleProvider struct {
	name string
	live *atomic.Int64

	mu      sync.Mutex
	running bool
}

func (p *lifecycleProvider) Name() string { return p.name }

func (p *lifecycleProvider) Authenticate(context.Context, *http.Request) (*AuthContext, error) {
	return nil, errors.New("not used")
}

func (p *lifecycleProvider) Start(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		p.running = true
		p.live.Add(1)
	}

	return nil
}

func (p *lifecycleProvider) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		p.running = false
		p.live.Add(-1)
	}

	return nil
}

// Start, Stop and RegisterProvider racing each other must leave exactly
// the registered provider running when the handler is started, and nothing
// running once it's stopped: no provider started after Stop, and none left
// running after it was replaced.
func TestAuth_LifecycleRacesLeaveNothingRunning(t *testing.T) {
	for range 200 {
		var live atomic.Int64

		ga := NewAuth(AuthConfig{Enabled: true, Providers: []string{"p"}}, forge.NewNoopLogger())
		ga.RegisterProvider(&lifecycleProvider{name: "p", live: &live})

		var wg sync.WaitGroup

		for range 4 {
			wg.Go(func() { ga.RegisterProvider(&lifecycleProvider{name: "p", live: &live}) })
		}

		wg.Go(func() { ga.Start(context.Background()) })
		wg.Go(func() { ga.Stop(context.Background()) })
		wg.Go(func() { ga.Start(context.Background()) })

		wg.Wait()

		ga.lifeMu.Lock()
		started := ga.started
		ga.lifeMu.Unlock()

		want := int64(0)
		if started {
			want = 1
		}

		if got := live.Load(); got != want {
			t.Fatalf("handler started %v with %d providers running, want %d", started, got, want)
		}

		ga.Stop(context.Background())

		if got := live.Load(); got != 0 {
			t.Fatalf("%d providers still running after Stop", got)
		}
	}
}

func spoofedRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Auth-Subject", "root")
	r.Header.Set("X-Auth-Provider", "spoofed")
	r.Header.Set("X-Auth-Scopes", "admin")
	r.Header.Set("X-Auth-Role", "admin")
	r.Header.Set("X-Auth-Email", "root@example.com")
	r.Header.Set("X-Auth-Token", "kept")

	return r
}

func TestDropAuthHeaders_RemovesOnlyTheFiveItSets(t *testing.T) {
	r := spoofedRequest()
	DropAuthHeaders(r.Header)

	for _, h := range forwardedAuthHeaders {
		if v := r.Header.Get(h); v != "" {
			t.Errorf("%s = %q after DropAuthHeaders", h, v)
		}
	}

	if r.Header.Get("X-Auth-Token") != "kept" {
		t.Error("DropAuthHeaders removed X-Auth-Token")
	}
}

func TestForwardAuthHeaders_NeverKeepsAClientValue(t *testing.T) {
	authCtx := &AuthContext{Subject: "alice", ProviderName: "jwt", Claims: map[string]any{}}

	t.Run("claims the token lacks", func(t *testing.T) {
		ga := NewAuth(AuthConfig{Enabled: true, ForwardHeaders: true}, forge.NewNoopLogger())
		r := spoofedRequest()
		ga.ForwardAuthHeaders(r, authCtx)

		if r.Header.Get("X-Auth-Subject") != "alice" || r.Header.Get("X-Auth-Provider") != "jwt" {
			t.Errorf("subject %q provider %q", r.Header.Get("X-Auth-Subject"), r.Header.Get("X-Auth-Provider"))
		}

		for _, h := range []string{"X-Auth-Scopes", "X-Auth-Role", "X-Auth-Email"} {
			if v := r.Header.Get(h); v != "" {
				t.Errorf("%s = %q, from the request rather than the token", h, v)
			}
		}
	})

	t.Run("forwarding off", func(t *testing.T) {
		ga := NewAuth(AuthConfig{Enabled: true}, forge.NewNoopLogger())
		r := spoofedRequest()
		ga.ForwardAuthHeaders(r, authCtx)

		for _, h := range forwardedAuthHeaders {
			if v := r.Header.Get(h); v != "" {
				t.Errorf("%s = %q with forwarding off", h, v)
			}
		}
	})
}

func TestAuth_WarnsWhenAJWKSIsFetchedOverPlainHTTP(t *testing.T) {
	logger := log.NewTestLogger()

	p := NewJWTAuthProvider(JWTAuthConfig{Name: "jwt", JWKSURL: "http://idp.example.com/jwks"})
	p.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("no network in tests")
	})

	ga := NewAuth(AuthConfig{Enabled: true, Providers: []string{"jwt"}}, logger)
	ga.RegisterProvider(p)
	ga.Start(context.Background())
	t.Cleanup(func() { ga.Stop(context.Background()) })

	var warned int

	for _, e := range logger.(*log.TestLogger).GetLogs() {
		if strings.Contains(e.Message, "plain http") {
			warned++
		}
	}

	if warned != 1 {
		t.Errorf("logged the plain http warning %d times, want 1", warned)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
