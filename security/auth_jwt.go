package security

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/xraph/bastion/internal/telemetry"
)

// JWTAuthConfig configures JWT/OIDC authentication.
type JWTAuthConfig struct {
	// Name is the provider name.
	Name string `json:"name" yaml:"name"`

	// JWKSURL is the URL of the JSON Web Key Set the provider checks
	// signatures against. The provider loads it on its own, and it rejects
	// every token until it holds at least one key from it, so a provider
	// without a JWKSURL accepts nothing.
	JWKSURL string `json:"jwksUrl" yaml:"jwks_url"`

	// Issuer is the expected "iss" claim value.
	Issuer string `json:"issuer" yaml:"issuer"`

	// Audience is the expected "aud" claim value.
	Audience string `json:"audience" yaml:"audience"`

	// ScopesClaim is the JWT claim containing scopes (default: "scope").
	ScopesClaim string `json:"scopesClaim,omitempty" yaml:"scopes_claim"`

	// SubjectClaim is the JWT claim containing the subject (default: "sub").
	SubjectClaim string `json:"subjectClaim,omitempty" yaml:"subject_claim"`

	// RefreshInterval is how often to refresh the JWKS (default: 1h).
	RefreshInterval time.Duration `json:"refreshInterval,omitempty" yaml:"refresh_interval"`

	// Algorithms lists the signing algorithms the provider accepts. It
	// defaults to all it supports: RS256, RS384, RS512, ES256, ES384 and
	// ES512. Names outside that list are ignored, and "none" is never
	// accepted.
	Algorithms []string `json:"algorithms,omitempty" yaml:"algorithms"`
}

const (
	// jwksRetryInterval is the shortest gap between two fetches a request
	// triggers, and where the refresh loop's backoff starts while the
	// provider holds no keys.
	jwksRetryInterval = 5 * time.Second

	// maxJWKSBytes caps how much of a JWKS response the provider reads.
	maxJWKSBytes = 1 << 20

	// jwksFetchWait caps how long a request waits on a fetch it needs, so a
	// slow JWKS endpoint holds requests for seconds rather than the client
	// timeout.
	jwksFetchWait = 3 * time.Second

	// maxJWKSRedirects is how many https redirects a JWKS fetch follows.
	maxJWKSRedirects = 3
)

// errNoVerificationKey is the reason behind a rejection while the provider
// holds no key it could check the token with.
var errNoVerificationKey = errors.New("no verification key loaded")

// errUnknownKid is the reason behind a rejection of a token whose kid no
// loaded key carries.
var errUnknownKid = errors.New("unknown key ID")

var (
	errTokenExpired     = errors.New("token expired")
	errTokenNotYetValid = errors.New("token not yet valid")
)

// JWTAuthProvider validates JWT tokens against keys from a JWKS.
//
// It loads the JWKS itself: Start (which the gateway calls for every
// registered provider) fetches it and refreshes it every RefreshInterval,
// and a provider nobody started fetches it on the first token it sees and
// again whenever the keys are older than RefreshInterval. Until a fetch has
// produced a key, every token is rejected. A fetch that fails, or returns a
// set with no usable key in it, leaves the keys already loaded in place.
type JWTAuthProvider struct {
	config JWTAuthConfig
	client *http.Client
	algs   map[string]jwsAlg

	// retryAfter is the shortest gap between two fetches a request
	// triggers, and fetchWait the longest a request waits on one. Tests
	// shorten them.
	retryAfter time.Duration
	fetchWait  time.Duration

	mu       sync.RWMutex
	keys     []jwtKey
	loadedAt time.Time

	fetchMu     sync.Mutex
	inflight    *jwksFetch
	lastAttempt time.Time

	runMu   sync.Mutex
	cancel  context.CancelFunc // set while the refresh loop runs
	stopped chan struct{}      // closed when that loop returns
}

// jwtKey is one verification key from the JWKS.
type jwtKey struct {
	kid string
	alg string // the JWK's "alg"; empty when the set doesn't pin one
	pub crypto.PublicKey
}

// jwksFetch is a JWKS fetch in progress. Everyone who needs it waits on
// done, so concurrent requests share one fetch.
type jwksFetch struct {
	done chan struct{}
	err  error
}

// NewJWTAuthProvider creates a JWT auth provider. It holds no keys yet, so
// it rejects every token until its first JWKS fetch succeeds.
func NewJWTAuthProvider(config JWTAuthConfig) *JWTAuthProvider {
	if config.ScopesClaim == "" {
		config.ScopesClaim = "scope"
	}

	if config.SubjectClaim == "" {
		config.SubjectClaim = "sub"
	}

	if config.RefreshInterval <= 0 {
		config.RefreshInterval = time.Hour
	}

	return &JWTAuthProvider{
		config: config,
		client: &http.Client{
			Timeout:       10 * time.Second,
			Transport:     telemetry.NewTransport(nil, "fetch jwks"),
			CheckRedirect: checkJWKSRedirect,
		},
		algs:       allowedAlgs(config.Algorithms),
		retryAfter: min(jwksRetryInterval, config.RefreshInterval),
		fetchWait:  jwksFetchWait,
	}
}

// checkJWKSRedirect lets a JWKS fetch follow a few redirects, and only to
// https URLs.
func checkJWKSRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxJWKSRedirects {
		return fmt.Errorf("stopped after %d redirects", maxJWKSRedirects)
	}

	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing to follow a redirect to a %s URL", req.URL.Scheme)
	}

	return nil
}

// startWarning is the warning Auth logs when it starts the provider: a JWKS
// fetched over plain HTTP from anywhere but loopback.
func (p *JWTAuthProvider) startWarning() string {
	u, err := url.Parse(p.config.JWKSURL)
	if err != nil || u.Scheme != "http" {
		return ""
	}

	host := u.Hostname()
	if host == "localhost" {
		return ""
	}

	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}

	return "jwt provider fetches its JWKS over plain http; use https so the keys can't be altered in transit"
}

// Name returns the provider name.
func (p *JWTAuthProvider) Name() string { return p.config.Name }

// Authenticate validates a JWT Bearer token.
func (p *JWTAuthProvider) Authenticate(ctx context.Context, r *http.Request) (*AuthContext, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return nil, &AuthError{Code: http.StatusUnauthorized, Message: "authorization header required"}
	}

	if !strings.HasPrefix(auth, "Bearer ") {
		return nil, &AuthError{Code: http.StatusUnauthorized, Message: "bearer token required"}
	}

	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return nil, &AuthError{Code: http.StatusUnauthorized, Message: "empty bearer token"}
	}

	claims, err := p.validateToken(ctx, token)
	if err != nil {
		return nil, tokenError(err)
	}

	return p.buildAuthContext(claims)
}

// tokenError is the AuthError a client gets for a token the provider
// turned down. Its message is one of a few fixed strings, so the response
// never says which check failed or whether the provider holds any keys.
// The reason stays on the error chain for the debug log.
func tokenError(err error) *AuthError {
	msg := "invalid token"

	switch {
	case errors.Is(err, errTokenExpired):
		msg = "token expired"
	case errors.Is(err, errTokenNotYetValid):
		msg = "token not yet valid"
	}

	return &AuthError{Code: http.StatusUnauthorized, Message: msg, cause: err}
}

// Start fetches the JWKS and then refreshes it every RefreshInterval until
// ctx is done or Stop is called. While the provider holds no keys, the
// refresh retries sooner, backing off from a few seconds up to
// RefreshInterval. The first fetch runs before Start returns, under ctx's
// span; each later one is the root of its own trace.
//
// The gateway calls Start for every registered provider, so you don't need
// to. Calling it again while the refresh runs does nothing. It returns an
// error when the provider still has no key afterwards, in which case the
// refresh keeps trying and every token is rejected until one loads.
func (p *JWTAuthProvider) Start(ctx context.Context) error {
	if p.config.JWKSURL == "" {
		return errors.New("jwt provider has no JWKS URL, so it rejects every token")
	}

	p.runMu.Lock()
	if p.cancel != nil {
		p.runMu.Unlock()

		return p.keyStatus(nil)
	}

	// The loop outlives the caller's span, so it drops it.
	loopCtx, cancel := context.WithCancel(telemetry.Detach(ctx))
	stopped := make(chan struct{})
	p.cancel, p.stopped = cancel, stopped
	p.runMu.Unlock()

	var err error
	if f := p.fetch(ctx, false, true); f != nil {
		select {
		case <-f.done:
			err = f.err
		case <-ctx.Done():
			err = ctx.Err()
		}
	}

	go p.refreshLoop(loopCtx, stopped)

	return p.keyStatus(err)
}

// Stop ends the refresh Start began. The keys already loaded stay. It is
// safe to call when nothing is running.
func (p *JWTAuthProvider) Stop(ctx context.Context) error {
	p.runMu.Lock()
	cancel, stopped := p.cancel, p.stopped
	p.cancel, p.stopped = nil, nil
	p.runMu.Unlock()

	if cancel == nil {
		return nil
	}

	cancel()

	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartKeyRefresh is Start without the error. The gateway starts the
// provider itself, so you only need this for a provider you use outside
// it, and even then the provider loads its keys on the first token.
func (p *JWTAuthProvider) StartKeyRefresh(ctx context.Context) {
	_ = p.Start(ctx)
}

// keyStatus is what Start returns: nil once the provider holds a key, and
// otherwise an error carrying why the last fetch failed.
func (p *JWTAuthProvider) keyStatus(fetchErr error) error {
	if p.hasKeys() {
		return nil
	}

	if fetchErr == nil {
		fetchErr = errNoVerificationKey
	}

	return fmt.Errorf("jwt provider has no verification key yet, so it rejects every token: %w", fetchErr)
}

// refreshLoop refetches the JWKS on the schedule nextRefresh sets until ctx
// is done, then closes stopped.
func (p *JWTAuthProvider) refreshLoop(ctx context.Context, stopped chan struct{}) {
	defer func() {
		p.runMu.Lock()
		if p.stopped == stopped {
			p.cancel, p.stopped = nil, nil
		}
		p.runMu.Unlock()

		close(stopped)
	}()

	backoff := p.retryAfter

	timer := time.NewTimer(p.nextRefresh(&backoff))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		if f := p.fetch(ctx, true, true); f != nil {
			select {
			case <-f.done:
			case <-ctx.Done():
				return
			}
		}

		timer.Reset(p.nextRefresh(&backoff))
	}
}

// nextRefresh is how long the loop waits before its next fetch:
// RefreshInterval once the provider holds keys, and while it holds none, a
// backoff that starts at retryAfter and doubles up to RefreshInterval.
func (p *JWTAuthProvider) nextRefresh(backoff *time.Duration) time.Duration {
	if p.hasKeys() {
		*backoff = p.retryAfter

		return p.config.RefreshInterval
	}

	wait := max(*backoff, time.Millisecond)
	*backoff = min(2*wait, p.config.RefreshInterval)

	return min(wait, p.config.RefreshInterval)
}

// fetch starts a JWKS fetch, or joins the one already running. Unless
// force is set, it starts nothing within retryAfter of the last attempt and
// returns nil. The fetch runs on ctx, so the caller decides whether it may
// be canceled.
func (p *JWTAuthProvider) fetch(ctx context.Context, root, force bool) *jwksFetch {
	p.fetchMu.Lock()
	defer p.fetchMu.Unlock()

	if p.inflight != nil {
		return p.inflight
	}

	if !force && time.Since(p.lastAttempt) < p.retryAfter {
		return nil
	}

	f := &jwksFetch{done: make(chan struct{})}
	p.inflight = f
	p.lastAttempt = time.Now()

	go func() {
		f.err = p.refreshKeysTraced(ctx, root)

		p.fetchMu.Lock()
		p.inflight = nil
		p.fetchMu.Unlock()

		close(f.done)
	}()

	return f
}

// currentKeys returns the keys a token is checked against, loading them
// first when the provider holds none. A request that finds the keys older
// than RefreshInterval starts a refresh in the background and is checked
// against the keys it has.
func (p *JWTAuthProvider) currentKeys(ctx context.Context) []jwtKey {
	keys, loadedAt := p.snapshot()
	if p.config.JWKSURL == "" {
		return keys
	}

	switch {
	case len(keys) == 0:
		keys = p.fetchAndWait(ctx)
	case time.Since(loadedAt) >= p.config.RefreshInterval:
		p.fetch(context.WithoutCancel(ctx), true, false)
	}

	return keys
}

// fetchAndWait starts a fetch a request needs, or joins the one already
// running, and waits for it until it ends, ctx is done or fetchWait has
// passed. Within retryAfter of the last attempt it starts nothing. It
// returns the keys the provider holds afterwards.
func (p *JWTAuthProvider) fetchAndWait(ctx context.Context) []jwtKey {
	// The fetch is shared with every request waiting on it, so one client
	// hanging up must not cancel it.
	if f := p.fetch(context.WithoutCancel(ctx), false, false); f != nil {
		timer := time.NewTimer(p.fetchWait)
		defer timer.Stop()

		select {
		case <-f.done:
		case <-ctx.Done():
		case <-timer.C:
		}
	}

	keys, _ := p.snapshot()

	return keys
}

func (p *JWTAuthProvider) snapshot() ([]jwtKey, time.Time) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.keys, p.loadedAt
}

func (p *JWTAuthProvider) hasKeys() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return len(p.keys) > 0
}

// refreshKeysTraced runs refreshKeys under a "refresh jwks" span. root
// starts it as the root of a new trace. It returns why the fetch failed,
// or nil when it loaded keys.
func (p *JWTAuthProvider) refreshKeysTraced(ctx context.Context, root bool) error {
	startSpan := telemetry.Start
	if root {
		startSpan = telemetry.StartRoot
	}

	ctx, span := startSpan(ctx, "refresh jwks")
	defer span.End()

	loaded, class, err := p.refreshKeys(ctx)
	if class != "" {
		telemetry.Fail(span, class, err)

		if err != nil {
			return fmt.Errorf("jwks %s: %w", class, err)
		}

		return fmt.Errorf("jwks %s", class)
	}

	span.SetAttributes(attribute.Int("bastion.jwks.keys", loaded))

	return nil
}

func (p *JWTAuthProvider) validateToken(ctx context.Context, token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT: expected 3 parts, got %d", len(parts))
	}

	// Decode header
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid JWT header: %w", err)
	}

	var header struct {
		Alg  string   `json:"alg"`
		Kid  string   `json:"kid"`
		Crit []string `json:"crit"`
	}

	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("invalid JWT header JSON: %w", err)
	}

	// The algorithm is checked first, whatever keys the provider holds, so
	// "none" and anything outside the allowed list stop here.
	alg, ok := p.algs[header.Alg]
	if !ok {
		return nil, fmt.Errorf("algorithm %q is not accepted", header.Alg)
	}

	// The provider understands no header extensions, so a token that
	// marks any as critical is rejected (RFC 7515, section 4.1.11).
	if len(header.Crit) > 0 {
		return nil, fmt.Errorf("critical header parameters %q are not supported", header.Crit)
	}

	// Every token is verified. With no key to verify it against, it is
	// rejected; the claims are never read unverified.
	key, err := selectKey(p.currentKeys(ctx), alg, header.Kid)

	// A kid no loaded key carries may be a key the identity provider has
	// just rotated in, so fetch again. It goes through the same throttle as
	// every fetch a request starts, so made-up kids cost at most one fetch
	// per retry gap.
	if errors.Is(err, errUnknownKid) && p.config.JWKSURL != "" {
		key, err = selectKey(p.fetchAndWait(ctx), alg, header.Kid)
	}

	if err != nil {
		return nil, err
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}

	if err := alg.verify(key, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return nil, err
	}

	// Decode claims
	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid JWT payload: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		return nil, fmt.Errorf("invalid JWT claims JSON: %w", err)
	}

	// Validate standard claims
	if err := p.validateClaims(claims); err != nil {
		return nil, err
	}

	return claims, nil
}

// jwsAlg is a JWS signing algorithm the provider can verify.
type jwsAlg struct {
	name  string
	hash  crypto.Hash
	curve elliptic.Curve // the curve an ES algorithm is bound to; nil for RS
	size  int            // bytes in each of R and S for an ES algorithm
}

// supportedAlgs are the algorithms the provider can verify. "none" and the
// HMAC family are left out on purpose: a provider that verifies with public
// keys accepts neither.
var supportedAlgs = []jwsAlg{
	{name: "RS256", hash: crypto.SHA256},
	{name: "RS384", hash: crypto.SHA384},
	{name: "RS512", hash: crypto.SHA512},
	{name: "ES256", hash: crypto.SHA256, curve: elliptic.P256(), size: 32},
	{name: "ES384", hash: crypto.SHA384, curve: elliptic.P384(), size: 48},
	{name: "ES512", hash: crypto.SHA512, curve: elliptic.P521(), size: 66},
}

// allowedAlgs is the supported algorithms named in names, or all of them
// when names is empty.
func allowedAlgs(names []string) map[string]jwsAlg {
	algs := make(map[string]jwsAlg, len(supportedAlgs))

	for _, a := range supportedAlgs {
		algs[a.name] = a
	}

	if len(names) == 0 {
		return algs
	}

	allowed := make(map[string]jwsAlg, len(names))

	for _, name := range names {
		if a, ok := algs[name]; ok {
			allowed[name] = a
		}
	}

	return allowed
}

func isSupportedAlg(name string) bool {
	for _, a := range supportedAlgs {
		if a.name == name {
			return true
		}
	}

	return false
}

// fits reports whether k can verify a signature made with a: the right key
// type, the right curve for an ES algorithm, and the same "alg" when the
// JWK pins one.
func (a jwsAlg) fits(k jwtKey) bool {
	if k.alg != "" && k.alg != a.name {
		return false
	}

	if a.curve == nil {
		_, ok := k.pub.(*rsa.PublicKey)

		return ok
	}

	ec, ok := k.pub.(*ecdsa.PublicKey)

	return ok && ec.Curve == a.curve
}

// verify checks sig over message with pub.
func (a jwsAlg) verify(pub crypto.PublicKey, message, sig []byte) error {
	if !a.fits(jwtKey{pub: pub}) {
		return fmt.Errorf("key does not fit %s", a.name)
	}

	h := a.hash.New()
	h.Write(message)
	digest := h.Sum(nil)

	if a.curve == nil {
		rsaKey, _ := pub.(*rsa.PublicKey)

		return rsa.VerifyPKCS1v15(rsaKey, a.hash, digest, sig)
	}

	// JWS carries an ECDSA signature as R then S, each padded to the
	// curve's size (RFC 7518, section 3.4), not as ASN.1.
	if len(sig) != 2*a.size {
		return fmt.Errorf("%s signature is %d bytes, want %d", a.name, len(sig), 2*a.size)
	}

	ecKey, _ := pub.(*ecdsa.PublicKey)
	r := new(big.Int).SetBytes(sig[:a.size])
	s := new(big.Int).SetBytes(sig[a.size:])

	if !ecdsa.Verify(ecKey, digest, r, s) {
		return errors.New("ECDSA signature verification failed")
	}

	return nil
}

// selectKey picks the key a token is checked against. A token that names a
// kid gets the key with that kid. One that names none gets the only key
// that fits its algorithm, and is rejected when several do. Either way the
// key must fit the algorithm, so an RSA key never checks an ES token and a
// P-256 key never checks an ES384 one.
func selectKey(keys []jwtKey, alg jwsAlg, kid string) (crypto.PublicKey, error) {
	if len(keys) == 0 {
		return nil, errNoVerificationKey
	}

	var (
		match   crypto.PublicKey
		n       int
		kidSeen bool
	)

	for _, k := range keys {
		if kid != "" && k.kid != kid {
			continue
		}

		kidSeen = true

		if !alg.fits(k) {
			continue
		}

		match = k.pub
		n++
	}

	switch {
	case n == 1:
		return match, nil
	case kid != "" && !kidSeen:
		return nil, fmt.Errorf("%w %q", errUnknownKid, kid)
	case n == 0 && kid != "":
		return nil, fmt.Errorf("no %s key with kid %q", alg.name, kid)
	case n == 0:
		return nil, fmt.Errorf("no %s key", alg.name)
	case kid != "":
		return nil, fmt.Errorf("%d %s keys share kid %q", n, alg.name, kid)
	default:
		return nil, fmt.Errorf("%d keys fit %s and the token names no kid", n, alg.name)
	}
}

func (p *JWTAuthProvider) validateClaims(claims map[string]any) error {
	// Check expiration
	if exp, ok := claims["exp"]; ok {
		expFloat, isFloat := exp.(float64)
		if isFloat && time.Now().Unix() > int64(expFloat) {
			return errTokenExpired
		}
	}

	// Check not-before
	if nbf, ok := claims["nbf"]; ok {
		nbfFloat, isFloat := nbf.(float64)
		if isFloat && time.Now().Unix() < int64(nbfFloat) {
			return errTokenNotYetValid
		}
	}

	// Check issuer
	if p.config.Issuer != "" {
		iss, _ := claims["iss"].(string)
		if iss != p.config.Issuer {
			return fmt.Errorf("invalid issuer: expected %q, got %q", p.config.Issuer, iss)
		}
	}

	// Check audience
	if p.config.Audience != "" {
		if !p.checkAudience(claims) {
			return fmt.Errorf("invalid audience")
		}
	}

	return nil
}

func (p *JWTAuthProvider) checkAudience(claims map[string]any) bool {
	aud := claims["aud"]

	switch v := aud.(type) {
	case string:
		return v == p.config.Audience
	case []any:
		for _, a := range v {
			if aStr, ok := a.(string); ok && aStr == p.config.Audience {
				return true
			}
		}
	}

	return false
}

func (p *JWTAuthProvider) buildAuthContext(claims map[string]any) (*AuthContext, error) {
	subject, _ := claims[p.config.SubjectClaim].(string)
	if subject == "" {
		subject = "unknown"
	}

	var scopes []string

	scopeVal := claims[p.config.ScopesClaim]

	switch v := scopeVal.(type) {
	case string:
		scopes = strings.Fields(v)
	case []any:
		for _, s := range v {
			if str, ok := s.(string); ok {
				scopes = append(scopes, str)
			}
		}
	}

	return &AuthContext{
		Subject: subject,
		Claims:  claims,
		Scopes:  scopes,
	}, nil
}

// refreshKeys fetches the JWKS and swaps in the signing keys it holds. It
// returns how many keys it loaded, or a short class and the error when the
// fetch failed. A set with no usable key counts as a failure and leaves the
// keys already loaded in place.
func (p *JWTAuthProvider) refreshKeys(ctx context.Context) (loaded int, class string, err error) {
	if p.config.JWKSURL == "" {
		return 0, "", nil
	}

	// #nosec G704 -- the URL is JWTAuthConfig.JWKSURL, set when the provider
	// is built. A fetch a request starts only carries that request's
	// context; nothing from the request can change the target.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.config.JWKSURL, nil)
	if err != nil {
		return 0, "build request failed", err
	}

	// #nosec G704 -- req targets the configured JWKS URL; see above.
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, "fetch failed", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, "unexpected status", nil
	}

	var jwks struct {
		Keys []json.RawMessage `json:"keys"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&jwks); err != nil {
		return 0, "decode failed", err
	}

	keys := make([]jwtKey, 0, len(jwks.Keys))

	for _, raw := range jwks.Keys {
		if k, ok := parseJWK(raw); ok {
			keys = append(keys, k)
		}
	}

	if len(keys) == 0 {
		return 0, "no usable keys", nil
	}

	p.mu.Lock()
	p.keys = keys
	p.loadedAt = time.Now()
	p.mu.Unlock()

	return len(keys), "", nil
}

// parseJWK turns one JWKS entry into a verification key. It skips keys
// meant for encryption, keys pinned to an algorithm the provider can't
// verify, and keys it can't parse. A key with no kid is kept.
func parseJWK(raw json.RawMessage) (jwtKey, bool) {
	var jwk struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}

	if err := json.Unmarshal(raw, &jwk); err != nil {
		return jwtKey{}, false
	}

	if jwk.Use != "" && jwk.Use != "sig" {
		return jwtKey{}, false
	}

	if jwk.Alg != "" && !isSupportedAlg(jwk.Alg) {
		return jwtKey{}, false
	}

	var pub crypto.PublicKey

	switch jwk.Kty {
	case "RSA":
		if k := parseRSAKey(jwk.N, jwk.E); k != nil {
			pub = k
		}
	case "EC":
		if k := parseECKey(jwk.Crv, jwk.X, jwk.Y); k != nil {
			pub = k
		}
	}

	if pub == nil {
		return jwtKey{}, false
	}

	return jwtKey{kid: jwk.Kid, alg: jwk.Alg, pub: pub}, true
}

func parseRSAKey(nStr, eStr string) *rsa.PublicKey {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil || len(nBytes) == 0 {
		return nil
	}

	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
		return nil
	}

	n := new(big.Int).SetBytes(nBytes)

	var e int
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}

	return &rsa.PublicKey{N: n, E: e}
}

// parseECKey builds an EC public key from a JWK's coordinates, checking
// that the point is on the curve.
func parseECKey(crv, xStr, yStr string) *ecdsa.PublicKey {
	xBytes, err := base64.RawURLEncoding.DecodeString(xStr)
	if err != nil {
		return nil
	}

	yBytes, err := base64.RawURLEncoding.DecodeString(yStr)
	if err != nil {
		return nil
	}

	var (
		curve elliptic.Curve
		size  int
	)

	switch crv {
	case "P-256":
		curve, size = elliptic.P256(), 32
	case "P-384":
		curve, size = elliptic.P384(), 48
	case "P-521":
		curve, size = elliptic.P521(), 66
	default:
		return nil
	}

	if len(xBytes) > size || len(yBytes) > size {
		return nil
	}

	// Uncompressed point: 0x04, then X and Y left-padded to the curve size.
	point := make([]byte, 1+2*size)
	point[0] = 4
	copy(point[1+size-len(xBytes):1+size], xBytes)
	copy(point[1+2*size-len(yBytes):], yBytes)

	key, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil
	}

	return key
}
