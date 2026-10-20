package gateway_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/xraph/forge"
	forgetesting "github.com/xraph/forge/testing"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/gateway"
	"github.com/xraph/bastion/security"
)

var b64 = base64.RawURLEncoding.EncodeToString

// es256Token signs claims with key the way JWS does, R then S.
func es256Token(t *testing.T, key *ecdsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()

	input := b64(marshal(t, header)) + "." + b64(marshal(t, claims))
	digest := sha256.Sum256([]byte(input))

	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return input + "." + b64(sig)
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func newES256Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	return key
}

// The JWT provider registered the way the README shows: it has its keys
// once the app has started, only a verified token reaches the upstream with
// X-Auth headers, and stopping the gateway ends the refresh.
func TestJWTProvider_ForwardsHeadersOnlyAfterVerification(t *testing.T) {
	key := newES256Key(t)

	point, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu       sync.Mutex
		fetches  int
		upstream []http.Header
	)

	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		fetches++
		mu.Unlock()

		_, _ = w.Write(marshal(t, map[string]any{"keys": []map[string]any{{
			"kty": "EC", "crv": "P-256", "kid": "k1", "x": b64(point[1:33]), "y": b64(point[33:]),
		}}}))
	}))
	t.Cleanup(jwks.Close)

	// Only proxied requests count; the health monitor probes the target
	// on its own path.
	up := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/svc/x" {
			return
		}

		mu.Lock()
		upstream = append(upstream, r.Header.Clone())
		mu.Unlock()
	}))
	t.Cleanup(up.Close)

	fetchCount := func() int {
		mu.Lock()
		defer mu.Unlock()

		return fetches
	}

	app := forgetesting.NewTestApp("gateway-jwt", "0.0.0")
	gw := gateway.New(routeTo(up.URL), bastion.WithDiscoveryEnabled(false), bastion.WithAuth(bastion.AuthConfig{
		Enabled:        true,
		Providers:      []string{"jwt"},
		ForwardHeaders: true,
	}))

	if err := app.RegisterExtension(gw); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}

	if err := app.RegisterHookFn(forge.PhaseAfterRegister, "bastion-jwt", func(context.Context, forge.App) error {
		gw.Auth().RegisterProvider(security.NewJWTAuthProvider(security.JWTAuthConfig{
			Name:            "jwt",
			JWKSURL:         jwks.URL,
			RefreshInterval: 10 * time.Millisecond,
		}))

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	if fetchCount() == 0 {
		t.Fatal("the gateway started without the JWT provider fetching its keys")
	}

	claims := map[string]any{"sub": "alice", "role": "admin", "email": "alice@example.com", "scope": "read"}

	get := func(token string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/svc/x", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		app.Router().ServeHTTP(rec, req)

		return rec.Code
	}

	for name, token := range map[string]string{
		"alg none":         b64(marshal(t, map[string]any{"alg": "none"})) + "." + b64(marshal(t, claims)) + ".",
		"another key":      es256Token(t, newES256Key(t), map[string]any{"alg": "ES256", "kid": "k1"}, claims),
		"no signature":     b64(marshal(t, map[string]any{"alg": "ES256", "kid": "k1"})) + "." + b64(marshal(t, claims)) + ".",
		"unknown kid":      es256Token(t, key, map[string]any{"alg": "ES256", "kid": "k2"}, claims),
		"not a JWT at all": "x." + b64(marshal(t, claims)) + ".y",
	} {
		if code := get(token); code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, code)
		}
	}

	mu.Lock()
	reached := len(upstream)
	mu.Unlock()

	if reached != 0 {
		t.Fatalf("%d unverified requests reached the upstream", reached)
	}

	if code := get(es256Token(t, key, map[string]any{"alg": "ES256", "kid": "k1"}, claims)); code != http.StatusOK {
		t.Fatalf("signed token: status %d, want 200", code)
	}

	mu.Lock()
	h := upstream[0]
	mu.Unlock()

	for header, want := range map[string]string{
		"X-Auth-Subject":  "alice",
		"X-Auth-Provider": "jwt",
		"X-Auth-Role":     "admin",
		"X-Auth-Email":    "alice@example.com",
		"X-Auth-Scopes":   "read",
	} {
		if got := h.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	// No requests from here on, so only the refresh loop can fetch. Prove
	// it runs before checking that Stop ends it. A request may have left
	// one background refresh in flight, hence three.
	before := fetchCount()
	deadline := time.Now().Add(5 * time.Second)

	for fetchCount() < before+3 {
		if time.Now().After(deadline) {
			t.Fatalf("the refresh loop never ran: %d fetches, want at least %d", fetchCount(), before+3)
		}

		time.Sleep(5 * time.Millisecond)
	}

	if err := gw.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A fetch already on the wire when Stop ran may still land, so let it
	// settle first. A refresh still running every 10ms would fetch several
	// more times in the window after.
	time.Sleep(30 * time.Millisecond)

	stopped := fetchCount()

	time.Sleep(80 * time.Millisecond)

	if n := fetchCount(); n != stopped {
		t.Errorf("JWKS fetched %d more times after Stop", n-stopped)
	}
}
