package security

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xraph/forge"
)

var b64 = base64.RawURLEncoding.EncodeToString

// rsaKeys are generated once: RSA key generation is slow, more so under
// -race.
var rsaKeys = sync.OnceValues(func() ([]*rsa.PrivateKey, error) {
	keys := make([]*rsa.PrivateKey, 2)

	for i := range keys {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}

		keys[i] = k
	}

	return keys, nil
})

// signer signs tokens with one private key and publishes its public half.
type signer struct {
	alg string
	kid string
	ec  *ecdsa.PrivateKey
	rsa *rsa.PrivateKey
}

func ecSigner(t *testing.T, alg, kid string) signer {
	t.Helper()

	curves := map[string]elliptic.Curve{"ES256": elliptic.P256(), "ES384": elliptic.P384(), "ES512": elliptic.P521()}

	k, err := ecdsa.GenerateKey(curves[alg], rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	return signer{alg: alg, kid: kid, ec: k}
}

func rsaSigner(t *testing.T, alg, kid string, n int) signer {
	t.Helper()

	keys, err := rsaKeys()
	if err != nil {
		t.Fatal(err)
	}

	return signer{alg: alg, kid: kid, rsa: keys[n]}
}

// jwk is the public key as a JWKS entry.
func (s signer) jwk(t *testing.T) map[string]any {
	t.Helper()

	m := map[string]any{}
	if s.kid != "" {
		m["kid"] = s.kid
	}

	if s.ec == nil {
		m["kty"] = "RSA"
		m["n"] = b64(s.rsa.N.Bytes())
		m["e"] = b64(big.NewInt(int64(s.rsa.E)).Bytes())

		return m
	}

	point, err := s.ec.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	size := (len(point) - 1) / 2
	m["kty"] = "EC"
	m["crv"] = s.ec.Curve.Params().Name
	m["x"] = b64(point[1 : 1+size])
	m["y"] = b64(point[1+size:])

	return m
}

// token signs claims under a header naming s's algorithm and kid.
func (s signer) token(t *testing.T, claims map[string]any) string {
	t.Helper()

	header := map[string]any{"alg": s.alg, "typ": "JWT"}
	if s.kid != "" {
		header["kid"] = s.kid
	}

	return s.sign(t, header, claims)
}

func (s signer) sign(t *testing.T, header, claims map[string]any) string {
	t.Helper()

	input := b64(mustJSON(t, header)) + "." + b64(mustJSON(t, claims))

	return input + "." + b64(s.signature(t, input))
}

// signature signs input the way JWS does: PKCS #1 v1.5 for RS, and R then
// S, each padded to the curve size, for ES.
func (s signer) signature(t *testing.T, input string) []byte {
	t.Helper()

	hash := allowedAlgs(nil)[s.alg].hash
	h := hash.New()
	h.Write([]byte(input))
	digest := h.Sum(nil)

	if s.ec == nil {
		sig, err := rsa.SignPKCS1v15(rand.Reader, s.rsa, hash, digest)
		if err != nil {
			t.Fatal(err)
		}

		return sig
	}

	r, ss, err := ecdsa.Sign(rand.Reader, s.ec, digest)
	if err != nil {
		t.Fatal(err)
	}

	size := (s.ec.Curve.Params().BitSize + 7) / 8
	sig := make([]byte, 2*size)
	r.FillBytes(sig[:size])
	ss.FillBytes(sig[size:])

	return sig
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func claims() map[string]any {
	return map[string]any{
		"sub":   "alice",
		"scope": "read write",
		"role":  "admin",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
}

// unsigned is a token with no signature at all.
func unsigned(t *testing.T, header map[string]any) string {
	t.Helper()

	return b64(mustJSON(t, header)) + "." + b64(mustJSON(t, claims())) + "."
}

// jwksServer serves a JWKS the test can change, stall or break.
type jwksServer struct {
	srv *httptest.Server

	mu     sync.Mutex
	status int
	body   []byte
	gate   chan struct{}
	hits   int
}

func newJWKS(t *testing.T, keys ...map[string]any) *jwksServer {
	t.Helper()

	j := &jwksServer{}
	j.set(t, http.StatusOK, keys...)

	j.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		j.hits++
		status, body, gate := j.status, j.body, j.gate
		j.mu.Unlock()

		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}

		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(j.srv.Close)

	return j
}

func (j *jwksServer) set(t *testing.T, status int, keys ...map[string]any) {
	t.Helper()

	if keys == nil {
		keys = []map[string]any{}
	}

	j.setBody(status, mustJSON(t, map[string]any{"keys": keys}))
}

func (j *jwksServer) setBody(status int, body []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.status, j.body = status, body
}

// hold stalls every fetch until the returned func is called.
func (j *jwksServer) hold(t *testing.T) func() {
	t.Helper()

	gate := make(chan struct{})

	j.mu.Lock()
	j.gate = gate
	j.mu.Unlock()

	var once sync.Once

	release := func() {
		once.Do(func() {
			j.mu.Lock()
			j.gate = nil
			j.mu.Unlock()

			close(gate)
		})
	}
	t.Cleanup(release)

	return release
}

func (j *jwksServer) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()

	return j.hits
}

// newProvider builds a provider on url that may refetch on demand right
// away, so tests don't wait out the retry gap.
func newProvider(t *testing.T, url string, mods ...func(*JWTAuthConfig)) *JWTAuthProvider {
	t.Helper()

	cfg := JWTAuthConfig{Name: "jwt", JWKSURL: url}
	for _, m := range mods {
		m(&cfg)
	}

	p := NewJWTAuthProvider(cfg)
	p.retryAfter = time.Millisecond

	t.Cleanup(func() { _ = p.Stop(context.Background()) })

	return p
}

func authenticate(ctx context.Context, p *JWTAuthProvider, token string) (*AuthContext, error) {
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	return p.Authenticate(ctx, r)
}

func mustAccept(t *testing.T, p *JWTAuthProvider, token string) *AuthContext {
	t.Helper()

	authCtx, err := authenticate(context.Background(), p, token)
	if err != nil {
		t.Fatalf("token rejected: %v (%v)", err, errors.Unwrap(err))
	}

	return authCtx
}

// mustReject checks the token is turned down with the same fixed 401 every
// unverifiable token gets.
func mustReject(t *testing.T, p *JWTAuthProvider, token string) {
	t.Helper()

	rejectedWith(t, "invalid token", func() (*AuthContext, error) {
		return authenticate(context.Background(), p, token)
	})
}

func rejectedWith(t *testing.T, msg string, call func() (*AuthContext, error)) {
	t.Helper()

	authCtx, err := call()
	if err == nil {
		t.Fatalf("token accepted as %+v", authCtx)
	}

	if authCtx != nil {
		t.Errorf("rejection still returned an auth context: %+v", authCtx)
	}

	var ae *AuthError
	if !errors.As(err, &ae) || ae.Code != http.StatusUnauthorized || ae.Message != msg {
		t.Fatalf("err = %#v, want a 401 %q", err, msg)
	}
}

func running(p *JWTAuthProvider) bool {
	p.runMu.Lock()
	defer p.runMu.Unlock()

	return p.cancel != nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestJWT_RejectsNoneAndUnlistedAlgorithmsWhateverTheKeyState(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	jwks := newJWKS(t, s.jwk(t))

	loaded := newProvider(t, jwks.srv.URL)
	if err := loaded.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A provider that has never fetched: the algorithm check comes before
	// any key lookup, so these tokens don't even start a fetch.
	empty := newJWKS(t, s.jwk(t))
	cold := newProvider(t, empty.srv.URL)

	// A shared-secret token keyed with the public key itself.
	mac := hmac.New(sha256.New, mustJSON(t, s.jwk(t)))
	hsInput := b64(mustJSON(t, map[string]any{"alg": "HS256", "kid": "k1"})) + "." + b64(mustJSON(t, claims()))
	mac.Write([]byte(hsInput))
	hs256 := hsInput + "." + b64(mac.Sum(nil))

	tokens := map[string]string{
		"none":              unsigned(t, map[string]any{"alg": "none"}),
		"none with a kid":   unsigned(t, map[string]any{"alg": "none", "kid": "k1"}),
		"None":              unsigned(t, map[string]any{"alg": "None"}),
		"NONE":              unsigned(t, map[string]any{"alg": "NONE"}),
		"no alg":            unsigned(t, map[string]any{"kid": "k1"}),
		"none with a sig":   s.sign(t, map[string]any{"alg": "none", "kid": "k1"}, claims()),
		"HS256":             hs256,
		"unsupported PS256": s.sign(t, map[string]any{"alg": "PS256", "kid": "k1"}, claims()),
	}

	for name, token := range tokens {
		t.Run(name, func(t *testing.T) {
			mustReject(t, loaded, token)
			mustReject(t, cold, token)
		})
	}

	if n := empty.count(); n != 0 {
		t.Errorf("unaccepted algorithms made the cold provider fetch %d times", n)
	}
}

func TestJWT_AcceptsTokensSignedWithALoadedKey(t *testing.T) {
	signers := []signer{
		ecSigner(t, "ES256", "es256"),
		ecSigner(t, "ES384", "es384"),
		ecSigner(t, "ES512", "es512"),
		rsaSigner(t, "RS256", "rs256", 0),
		rsaSigner(t, "RS384", "rs384", 0),
		rsaSigner(t, "RS512", "rs512", 0),
	}

	keys := make([]map[string]any, 0, len(signers))
	for _, s := range signers {
		keys = append(keys, s.jwk(t))
	}

	p := newProvider(t, newJWKS(t, keys...).srv.URL)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, s := range signers {
		t.Run(s.alg, func(t *testing.T) {
			authCtx := mustAccept(t, p, s.token(t, claims()))

			if authCtx.Subject != "alice" || !authCtx.HasScopes("read", "write") || authCtx.Claims["role"] != "admin" {
				t.Errorf("auth context = %+v", authCtx)
			}
		})
	}

	es := signers[0]

	t.Run("claims changed after signing", func(t *testing.T) {
		good := es.token(t, claims())
		changed := claims()
		changed["sub"] = "bob"

		parts := strings.Split(good, ".")
		mustReject(t, p, parts[0]+"."+b64(mustJSON(t, changed))+"."+parts[2])
	})

	t.Run("signed by another key under a known kid", func(t *testing.T) {
		other := ecSigner(t, "ES256", "es256")
		mustReject(t, p, other.token(t, claims()))
	})

	t.Run("ES256 signature in ASN.1 form", func(t *testing.T) {
		input := b64(mustJSON(t, map[string]any{"alg": "ES256", "kid": "es256"})) + "." + b64(mustJSON(t, claims()))
		h := sha256.Sum256([]byte(input))

		der, err := ecdsa.SignASN1(rand.Reader, es.ec, h[:])
		if err != nil {
			t.Fatal(err)
		}

		mustReject(t, p, input+"."+b64(der))
	})

	t.Run("expired", func(t *testing.T) {
		c := claims()
		c["exp"] = time.Now().Add(-time.Minute).Unix()

		rejectedWith(t, "token expired", func() (*AuthContext, error) {
			return authenticate(context.Background(), p, es.token(t, c))
		})
	})

	t.Run("wrong issuer", func(t *testing.T) {
		strict := newProvider(t, newJWKS(t, es.jwk(t)).srv.URL, func(c *JWTAuthConfig) { c.Issuer = "https://idp.example.com" })
		mustReject(t, strict, es.token(t, claims()))
	})
}

func TestJWT_RejectsEveryTokenWithoutAKey(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	token := s.token(t, claims())

	t.Run("no JWKS URL", func(t *testing.T) {
		p := newProvider(t, "")

		if err := p.Start(context.Background()); err == nil {
			t.Error("Start reported a provider with no JWKS URL as ready")
		}

		mustReject(t, p, token)
		mustReject(t, p, unsigned(t, map[string]any{"alg": "ES256", "kid": "k1"}))
	})

	t.Run("JWKS fetch fails", func(t *testing.T) {
		jwks := newJWKS(t, s.jwk(t))
		jwks.set(t, http.StatusInternalServerError)

		p := newProvider(t, jwks.srv.URL)

		if err := p.Start(context.Background()); err == nil {
			t.Error("Start reported a provider without keys as ready")
		}

		mustReject(t, p, token)
		mustReject(t, p, unsigned(t, map[string]any{"alg": "ES256", "kid": "k1"}))
	})

	t.Run("JWKS has no keys", func(t *testing.T) {
		p := newProvider(t, newJWKS(t).srv.URL)

		if err := p.Start(context.Background()); err == nil {
			t.Error("Start reported a provider without keys as ready")
		}

		mustReject(t, p, token)
	})
}

func TestJWT_RejectsTokensBeforeTheFirstFetchCompletes(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	jwks := newJWKS(t, s.jwk(t))
	release := jwks.hold(t)

	p := newProvider(t, jwks.srv.URL)

	started := make(chan error, 1)

	go func() { started <- p.Start(context.Background()) }()

	eventually(t, "the first fetch to reach the JWKS server", func() bool { return jwks.count() > 0 })

	for name, token := range map[string]string{
		"signed":   s.token(t, claims()),
		"unsigned": unsigned(t, map[string]any{"alg": "ES256", "kid": "k1"}),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()

			rejectedWith(t, "invalid token", func() (*AuthContext, error) { return authenticate(ctx, p, token) })
		})
	}

	release()

	if err := <-started; err != nil {
		t.Fatal(err)
	}

	mustAccept(t, p, s.token(t, claims()))

	if n := jwks.count(); n != 1 {
		t.Errorf("requests waiting on the first fetch started %d fetches, want them to share 1", n)
	}
}

func TestJWT_RecoversWhenTheFirstFetchFails(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	token := s.token(t, claims())

	t.Run("the refresh retries before the interval is up", func(t *testing.T) {
		jwks := newJWKS(t, s.jwk(t))
		jwks.set(t, http.StatusServiceUnavailable)

		p := newProvider(t, jwks.srv.URL)

		if err := p.Start(context.Background()); err == nil {
			t.Fatal("Start reported ready after a failed fetch")
		}

		mustReject(t, p, token)

		jwks.set(t, http.StatusOK, s.jwk(t))

		// No requests: the loop alone has to get the keys, well inside the
		// default one-hour interval.
		eventually(t, "the refresh to load the keys", p.hasKeys)
		mustAccept(t, p, token)
	})

	t.Run("a later request fetches again", func(t *testing.T) {
		jwks := newJWKS(t, s.jwk(t))
		jwks.set(t, http.StatusServiceUnavailable)

		p := newProvider(t, jwks.srv.URL)

		mustReject(t, p, token)

		jwks.set(t, http.StatusOK, s.jwk(t))
		time.Sleep(5 * time.Millisecond)

		mustAccept(t, p, token)

		if n := jwks.count(); n != 2 {
			t.Errorf("JWKS fetched %d times, want 2", n)
		}
	})

	t.Run("requests don't refetch inside the retry gap", func(t *testing.T) {
		jwks := newJWKS(t, s.jwk(t))
		jwks.set(t, http.StatusServiceUnavailable)

		p := NewJWTAuthProvider(JWTAuthConfig{Name: "jwt", JWKSURL: jwks.srv.URL})

		for range 5 {
			mustReject(t, p, token)
		}

		if n := jwks.count(); n != 1 {
			t.Errorf("JWKS fetched %d times, want 1", n)
		}
	})
}

func TestJWT_KeepsItsKeysWhenARefreshBringsNone(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	token := s.token(t, claims())
	jwks := newJWKS(t, s.jwk(t))

	p := newProvider(t, jwks.srv.URL)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	mustAccept(t, p, token)

	unusable := []map[string]any{
		{"kty": "oct", "k": b64([]byte("secret"))},
		func() map[string]any { k := s.jwk(t); k["use"] = "enc"; return k }(),
		func() map[string]any { k := s.jwk(t); k["alg"] = "RSA-OAEP"; return k }(),
		{"kty": "EC", "crv": "P-256", "x": b64([]byte("not")), "y": b64([]byte("a point"))},
		{"kty": "RSA", "n": "", "e": "AQAB"},
	}

	for name, apply := range map[string]func(){
		"an empty set":       func() { jwks.set(t, http.StatusOK) },
		"only unusable keys": func() { jwks.set(t, http.StatusOK, unusable...) },
		"a server error":     func() { jwks.set(t, http.StatusInternalServerError) },
		"a body that isn't JSON": func() {
			jwks.setBody(http.StatusOK, []byte("<html>"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			apply()

			if err := p.refreshKeysTraced(context.Background(), true); err == nil {
				t.Error("refresh reported success")
			}

			mustAccept(t, p, token)
		})
	}
}

func TestJWT_KeysWithoutAKid(t *testing.T) {
	es := ecSigner(t, "ES256", "")
	es2 := ecSigner(t, "ES256", "")
	rs := rsaSigner(t, "RS256", "", 0)

	start := func(t *testing.T, keys ...map[string]any) *JWTAuthProvider {
		t.Helper()

		p := newProvider(t, newJWKS(t, keys...).srv.URL)
		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}

		return p
	}

	t.Run("a single key and a token without a kid", func(t *testing.T) {
		mustAccept(t, start(t, es.jwk(t)), es.token(t, claims()))
	})

	t.Run("a token naming a kid the set doesn't have", func(t *testing.T) {
		named := es
		named.kid = "k1"
		mustReject(t, start(t, es.jwk(t)), named.token(t, claims()))
	})

	t.Run("one key per family, picked by algorithm", func(t *testing.T) {
		p := start(t, es.jwk(t), rs.jwk(t))
		mustAccept(t, p, es.token(t, claims()))
		mustAccept(t, p, rs.token(t, claims()))
	})

	t.Run("a single key with a kid and a token without one", func(t *testing.T) {
		named := es
		named.kid = "k1"
		mustAccept(t, start(t, named.jwk(t)), es.token(t, claims()))
	})

	t.Run("two keys that fit and a token without a kid", func(t *testing.T) {
		p := start(t, es.jwk(t), es2.jwk(t))
		mustReject(t, p, es.token(t, claims()))
		mustReject(t, p, es2.token(t, claims()))
	})
}

func TestJWT_NeverChecksATokenWithAKeyForAnotherAlgorithm(t *testing.T) {
	t.Run("kid of an RSA key on an ES256 token", func(t *testing.T) {
		rs := rsaSigner(t, "RS256", "shared", 0)
		es := ecSigner(t, "ES256", "shared")

		mustReject(t, newProvider(t, newJWKS(t, rs.jwk(t)).srv.URL), es.token(t, claims()))
	})

	t.Run("kid of a P-256 key on an ES384 token", func(t *testing.T) {
		p256 := ecSigner(t, "ES256", "shared")
		p384 := ecSigner(t, "ES384", "shared")

		mustReject(t, newProvider(t, newJWKS(t, p256.jwk(t)).srv.URL), p384.token(t, claims()))
	})

	t.Run("a key pinned to RS384", func(t *testing.T) {
		rs256 := rsaSigner(t, "RS256", "k1", 1)
		rs384 := rs256
		rs384.alg = "RS384"

		jwk := rs256.jwk(t)
		jwk["alg"] = "RS384"

		p := newProvider(t, newJWKS(t, jwk).srv.URL)
		mustReject(t, p, rs256.token(t, claims()))
		mustAccept(t, p, rs384.token(t, claims()))
	})

	t.Run("an algorithm left off the allowed list", func(t *testing.T) {
		es := ecSigner(t, "ES256", "es")
		rs := rsaSigner(t, "RS256", "rs", 0)

		p := newProvider(t, newJWKS(t, es.jwk(t), rs.jwk(t)).srv.URL, func(c *JWTAuthConfig) { c.Algorithms = []string{"RS256", "none"} })
		mustReject(t, p, es.token(t, claims()))
		mustReject(t, p, unsigned(t, map[string]any{"alg": "none"}))
		mustAccept(t, p, rs.token(t, claims()))
	})
}

func TestJWT_StartIsIdempotentAndStopEndsTheRefresh(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	jwks := newJWKS(t, s.jwk(t))
	p := newProvider(t, jwks.srv.URL)

	for range 2 {
		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	p.StartKeyRefresh(context.Background())

	if n := jwks.count(); n != 1 {
		t.Errorf("three starts fetched %d times, want 1", n)
	}

	for range 2 {
		if err := p.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if running(p) {
		t.Fatal("refresh still running after Stop")
	}

	mustAccept(t, p, s.token(t, claims()))

	ctx, cancel := context.WithCancel(context.Background())
	p.StartKeyRefresh(ctx)

	if !running(p) || jwks.count() != 2 {
		t.Fatalf("restart: running %v after %d fetches", running(p), jwks.count())
	}

	cancel()
	eventually(t, "the refresh to end with its context", func() bool { return !running(p) })
}

func TestJWT_KeepsRefreshingOnTheInterval(t *testing.T) {
	old := ecSigner(t, "ES256", "old")
	rotated := ecSigner(t, "ES256", "new")

	interval := func(c *JWTAuthConfig) { c.RefreshInterval = 20 * time.Millisecond }

	t.Run("started", func(t *testing.T) {
		jwks := newJWKS(t, old.jwk(t))
		p := newProvider(t, jwks.srv.URL, interval)

		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}

		eventually(t, "the loop to refetch", func() bool { return jwks.count() >= 3 })
	})

	t.Run("never started", func(t *testing.T) {
		jwks := newJWKS(t, old.jwk(t))
		p := newProvider(t, jwks.srv.URL, interval)

		mustAccept(t, p, old.token(t, claims()))

		jwks.set(t, http.StatusOK, rotated.jwk(t))

		eventually(t, "the rotated key to load", func() bool {
			_, err := authenticate(context.Background(), p, rotated.token(t, claims()))
			return err == nil
		})

		mustReject(t, p, old.token(t, claims()))
	})
}

func TestAuth_StartsAndStopsItsProviders(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")

	t.Run("enabled", func(t *testing.T) {
		jwks := newJWKS(t, s.jwk(t))
		p := newProvider(t, jwks.srv.URL)

		ga := NewAuth(AuthConfig{Enabled: true, Providers: []string{"jwt"}}, forge.NewNoopLogger())
		ga.RegisterProvider(p)
		ga.Start(context.Background())

		if !p.hasKeys() || !running(p) {
			t.Fatalf("after Auth.Start: keys %v, refresh running %v", p.hasKeys(), running(p))
		}

		ga.Stop(context.Background())

		if running(p) {
			t.Error("refresh still running after Auth.Stop")
		}
	})

	t.Run("registered after Start", func(t *testing.T) {
		ga := NewAuth(AuthConfig{Enabled: true, Providers: []string{"jwt"}}, forge.NewNoopLogger())
		ga.Start(context.Background())

		first := newProvider(t, newJWKS(t, s.jwk(t)).srv.URL)
		ga.RegisterProvider(first)

		if !first.hasKeys() || !running(first) {
			t.Fatalf("after RegisterProvider: keys %v, refresh running %v", first.hasKeys(), running(first))
		}

		second := newProvider(t, newJWKS(t, s.jwk(t)).srv.URL)
		ga.RegisterProvider(second)

		if running(first) || !running(second) {
			t.Errorf("after replacing: old running %v, new running %v", running(first), running(second))
		}

		ga.Stop(context.Background())

		if running(second) {
			t.Error("refresh still running after Auth.Stop")
		}

		third := newProvider(t, newJWKS(t, s.jwk(t)).srv.URL)
		ga.RegisterProvider(third)

		if running(third) {
			t.Error("RegisterProvider started a provider after Auth.Stop")
		}
	})

	t.Run("disabled", func(t *testing.T) {
		jwks := newJWKS(t, s.jwk(t))
		p := newProvider(t, jwks.srv.URL)

		ga := NewAuth(AuthConfig{}, forge.NewNoopLogger())
		ga.RegisterProvider(p)
		ga.Start(context.Background())

		if running(p) || jwks.count() != 0 {
			t.Error("Auth.Start started a provider while auth is disabled")
		}
	})

	t.Run("unverified tokens get no auth context", func(t *testing.T) {
		p := newProvider(t, "")

		ga := NewAuth(AuthConfig{Enabled: true, Providers: []string{"jwt"}, ForwardHeaders: true}, forge.NewNoopLogger())
		ga.RegisterProvider(p)

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer "+unsigned(t, map[string]any{"alg": "none"}))

		authCtx, err := ga.Authenticate(r, testRoute{})
		if err == nil || authCtx != nil {
			t.Fatalf("Authenticate = %+v, %v", authCtx, err)
		}

		var ae *AuthError
		if !errors.As(err, &ae) || ae.Message != "invalid token" {
			t.Errorf("err = %v, want the fixed invalid token message", err)
		}
	})
}

// The provider is used through crypto.PublicKey, so make sure the
// algorithm table never lets a key of one type through as another.
func TestJWT_VerifyRefusesAKeyOfTheWrongType(t *testing.T) {
	es := ecSigner(t, "ES256", "")
	rs := rsaSigner(t, "RS256", "", 0)

	algs := allowedAlgs(nil)

	for _, tc := range []struct {
		alg string
		pub crypto.PublicKey
	}{
		{"RS256", &es.ec.PublicKey},
		{"ES256", &rs.rsa.PublicKey},
		{"ES384", &es.ec.PublicKey},
	} {
		if err := algs[tc.alg].verify(tc.pub, []byte("m"), make([]byte, 96)); err == nil {
			t.Errorf("%s verified with a %T", tc.alg, tc.pub)
		}
	}
}

func TestJWT_RejectsCriticalHeaderParameters(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")

	p := newProvider(t, newJWKS(t, s.jwk(t)).srv.URL)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	mustReject(t, p, s.sign(t, map[string]any{"alg": "ES256", "kid": "k1", "crit": []string{"exp"}}, claims()))
	mustReject(t, p, s.sign(t, map[string]any{"alg": "ES256", "kid": "k1", "crit": "exp"}, claims()))
	mustAccept(t, p, s.sign(t, map[string]any{"alg": "ES256", "kid": "k1"}, claims()))
}

func TestJWT_RejectsECSignaturesOfTheWrongLength(t *testing.T) {
	for _, alg := range []string{"ES256", "ES384", "ES512"} {
		t.Run(alg, func(t *testing.T) {
			s := ecSigner(t, alg, "k1")

			p := newProvider(t, newJWKS(t, s.jwk(t)).srv.URL)
			if err := p.Start(context.Background()); err != nil {
				t.Fatal(err)
			}

			good := s.token(t, claims())
			mustAccept(t, p, good)

			parts := strings.Split(good, ".")

			sig, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				t.Fatal(err)
			}

			// One byte either side, every other curve's length, and none.
			for _, n := range []int{len(sig) - 1, len(sig) + 1, 64, 96, 132, 0} {
				if n == len(sig) {
					continue
				}

				bad := make([]byte, n)
				copy(bad, sig)

				t.Run(strconv.Itoa(n)+" bytes", func(t *testing.T) {
					mustReject(t, p, parts[0]+"."+parts[1]+"."+b64(bad))
				})
			}
		})
	}
}

func TestJWT_CapsTheJWKSBodyAtOneMebibyte(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	token := s.token(t, claims())

	// body is a JWKS holding s's key, padded to exactly size bytes.
	body := func(size int) []byte {
		prefix := `{"keys":[` + string(mustJSON(t, s.jwk(t))) + `],"pad":"`
		suffix := `"}`

		return []byte(prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix)
	}

	t.Run("at the cap", func(t *testing.T) {
		jwks := newJWKS(t)
		jwks.setBody(http.StatusOK, body(maxJWKSBytes))

		p := newProvider(t, jwks.srv.URL)
		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}

		mustAccept(t, p, token)
	})

	t.Run("one byte over", func(t *testing.T) {
		jwks := newJWKS(t)
		jwks.setBody(http.StatusOK, body(maxJWKSBytes+1))

		p := newProvider(t, jwks.srv.URL)
		if err := p.Start(context.Background()); err == nil {
			t.Fatal("Start loaded keys from a JWKS over the size cap")
		}

		mustReject(t, p, token)
	})
}

func TestJWT_ConcurrentRequestsShareOneFetch(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	token := s.token(t, claims())

	jwks := newJWKS(t, s.jwk(t))
	release := jwks.hold(t)

	p := newProvider(t, jwks.srv.URL)
	p.fetchWait = time.Minute

	const n = 20

	errs := make(chan error, n)

	for range n {
		go func() {
			_, err := authenticate(context.Background(), p, token)
			errs <- err
		}()
	}

	eventually(t, "the first fetch to reach the JWKS server", func() bool { return jwks.count() > 0 })
	time.Sleep(20 * time.Millisecond)
	release()

	for range n {
		if err := <-errs; err != nil {
			t.Errorf("request rejected: %v (%v)", err, errors.Unwrap(err))
		}
	}

	if got := jwks.count(); got != 1 {
		t.Errorf("%d concurrent requests made %d fetches, want 1", n, got)
	}
}

func TestJWT_CapsHowLongARequestWaitsOnAFetch(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	jwks := newJWKS(t, s.jwk(t))
	jwks.hold(t)

	p := newProvider(t, jwks.srv.URL)
	p.fetchWait = 50 * time.Millisecond

	begin := time.Now()

	mustReject(t, p, s.token(t, claims()))

	if waited := time.Since(begin); waited > 2*time.Second {
		t.Errorf("request waited %v on a stalled fetch", waited)
	}

	if jwksFetchWait > 3*time.Second {
		t.Errorf("jwksFetchWait = %v, want at most 3s", jwksFetchWait)
	}
}

func TestJWT_RefetchesForAnUnknownKid(t *testing.T) {
	old := ecSigner(t, "ES256", "old")
	rotated := ecSigner(t, "ES256", "new")

	t.Run("a rotated-in key is accepted before the refresh is due", func(t *testing.T) {
		jwks := newJWKS(t, old.jwk(t))

		p := newProvider(t, jwks.srv.URL)
		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}

		jwks.set(t, http.StatusOK, old.jwk(t), rotated.jwk(t))
		time.Sleep(5 * time.Millisecond)

		mustAccept(t, p, rotated.token(t, claims()))
		mustAccept(t, p, old.token(t, claims()))

		if n := jwks.count(); n != 2 {
			t.Errorf("JWKS fetched %d times, want 2", n)
		}
	})

	t.Run("made-up kids fetch at most once per retry gap", func(t *testing.T) {
		jwks := newJWKS(t, old.jwk(t))

		p := newProvider(t, jwks.srv.URL)
		p.retryAfter = time.Hour

		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}

		madeUp := func() {
			t.Helper()

			for i := range 50 {
				other := old
				other.kid = "kid-" + strconv.Itoa(i)
				mustReject(t, p, other.token(t, claims()))
			}
		}

		madeUp()

		if n := jwks.count(); n != 1 {
			t.Fatalf("inside the retry gap: %d fetches, want only the first", n)
		}

		// The gap has passed: one more fetch, then the throttle again.
		p.fetchMu.Lock()
		p.lastAttempt = time.Time{}
		p.fetchMu.Unlock()

		madeUp()

		if n := jwks.count(); n != 2 {
			t.Errorf("after the gap: %d fetches, want 2", n)
		}
	})
}

func TestJWT_JWKSRedirects(t *testing.T) {
	s := ecSigner(t, "ES256", "k1")
	body := mustJSON(t, map[string]any{"keys": []map[string]any{s.jwk(t)}})

	var plainHits atomic.Int32

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(plain.Close)

	var tlsHits atomic.Int32

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tlsHits.Add(1)

		switch r.URL.Path {
		case "/to-http":
			http.Redirect(w, r, plain.URL+"/jwks", http.StatusFound)
		case "/to-https":
			http.Redirect(w, r, "/jwks", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/jwks":
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(secure.Close)

	provider := func(t *testing.T, path string) *JWTAuthProvider {
		t.Helper()

		p := newProvider(t, secure.URL+path)
		p.client.Transport = secure.Client().Transport

		return p
	}

	t.Run("to http", func(t *testing.T) {
		if err := provider(t, "/to-http").Start(context.Background()); err == nil {
			t.Error("followed a redirect to a plain http JWKS")
		}

		if plainHits.Load() != 0 {
			t.Error("the plain http server was fetched")
		}
	})

	t.Run("to https", func(t *testing.T) {
		p := provider(t, "/to-https")
		if err := p.Start(context.Background()); err != nil {
			t.Fatal(err)
		}

		mustAccept(t, p, s.token(t, claims()))
	})

	t.Run("redirect loop", func(t *testing.T) {
		tlsHits.Store(0)

		if err := provider(t, "/loop").Start(context.Background()); err == nil {
			t.Error("a redirect loop loaded keys")
		}

		if n := tlsHits.Load(); n > maxJWKSRedirects+1 {
			t.Errorf("followed %d redirects", n-1)
		}
	})
}

func TestJWT_WarnsAboutAPlainHTTPJWKS(t *testing.T) {
	for url, warn := range map[string]bool{
		"http://idp.example.com/jwks":  true,
		"http://10.0.0.5/jwks":         true,
		"https://idp.example.com/jwks": false,
		"http://localhost:8080/jwks":   false,
		"http://127.0.0.1:8080/jwks":   false,
		"http://[::1]:8080/jwks":       false,
		"":                             false,
	} {
		p := NewJWTAuthProvider(JWTAuthConfig{Name: "jwt", JWKSURL: url})

		if got := p.startWarning() != ""; got != warn {
			t.Errorf("%q: warning %v, want %v", url, got, warn)
		}
	}
}
