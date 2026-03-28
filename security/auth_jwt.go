package security

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JWTAuthConfig configures JWT/OIDC authentication.
type JWTAuthConfig struct {
	// Name is the provider name.
	Name string `json:"name" yaml:"name"`

	// JWKSURL is the URL to fetch JSON Web Key Sets for key verification.
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
}

// JWTAuthProvider validates JWT tokens using JWKS for key retrieval.
type JWTAuthProvider struct {
	config JWTAuthConfig
	client *http.Client

	mu   sync.RWMutex
	keys map[string]crypto.PublicKey
}

// NewJWTAuthProvider creates a JWT auth provider.
func NewJWTAuthProvider(config JWTAuthConfig) *JWTAuthProvider {
	if config.ScopesClaim == "" {
		config.ScopesClaim = "scope"
	}

	if config.SubjectClaim == "" {
		config.SubjectClaim = "sub"
	}

	if config.RefreshInterval == 0 {
		config.RefreshInterval = time.Hour
	}

	return &JWTAuthProvider{
		config: config,
		client: &http.Client{Timeout: 10 * time.Second},
		keys:   make(map[string]crypto.PublicKey),
	}
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
		return nil, &AuthError{Code: http.StatusUnauthorized, Message: err.Error()}
	}

	return p.buildAuthContext(claims)
}

// StartKeyRefresh starts a background goroutine to refresh JWKS periodically.
func (p *JWTAuthProvider) StartKeyRefresh(ctx context.Context) {
	if p.config.JWKSURL == "" {
		return
	}

	// Initial fetch
	p.refreshKeys(ctx)

	go func() {
		ticker := time.NewTicker(p.config.RefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.refreshKeys(ctx)
			}
		}
	}()
}

func (p *JWTAuthProvider) validateToken(_ context.Context, token string) (map[string]any, error) {
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
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}

	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("invalid JWT header JSON: %w", err)
	}

	// Verify signature if we have keys
	p.mu.RLock()
	hasKeys := len(p.keys) > 0
	p.mu.RUnlock()

	if hasKeys {
		if err := p.verifySignature(parts, header.Alg, header.Kid); err != nil {
			return nil, err
		}
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

func (p *JWTAuthProvider) verifySignature(parts []string, alg, kid string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var key crypto.PublicKey
	if kid != "" {
		var ok bool
		key, ok = p.keys[kid]

		if !ok {
			return fmt.Errorf("unknown key ID: %s", kid)
		}
	} else {
		// Use first available key
		for _, k := range p.keys {
			key = k
			break
		}
	}

	if key == nil {
		return fmt.Errorf("no signing key available")
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}

	message := []byte(parts[0] + "." + parts[1])

	switch alg {
	case "RS256":
		return verifyRSA(key, crypto.SHA256, message, sigBytes)
	case "RS384":
		return verifyRSA(key, crypto.SHA384, message, sigBytes)
	case "RS512":
		return verifyRSA(key, crypto.SHA512, message, sigBytes)
	case "ES256":
		return verifyECDSA(key, crypto.SHA256, message, sigBytes)
	case "ES384":
		return verifyECDSA(key, crypto.SHA384, message, sigBytes)
	default:
		return fmt.Errorf("unsupported algorithm: %s", alg)
	}
}

func verifyRSA(key crypto.PublicKey, hash crypto.Hash, message, sig []byte) error {
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("expected RSA key for RS* algorithm")
	}

	h := hash.New()
	h.Write(message)

	return rsa.VerifyPKCS1v15(rsaKey, hash, h.Sum(nil), sig)
}

func verifyECDSA(key crypto.PublicKey, hash crypto.Hash, message, sig []byte) error {
	ecKey, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("expected ECDSA key for ES* algorithm")
	}

	h := hash.New()
	h.Write(message)

	if !ecdsa.VerifyASN1(ecKey, h.Sum(nil), sig) {
		return fmt.Errorf("ECDSA signature verification failed")
	}

	return nil
}

func (p *JWTAuthProvider) validateClaims(claims map[string]any) error {
	// Check expiration
	if exp, ok := claims["exp"]; ok {
		expFloat, isFloat := exp.(float64)
		if isFloat && time.Now().Unix() > int64(expFloat) {
			return fmt.Errorf("token expired")
		}
	}

	// Check not-before
	if nbf, ok := claims["nbf"]; ok {
		nbfFloat, isFloat := nbf.(float64)
		if isFloat && time.Now().Unix() < int64(nbfFloat) {
			return fmt.Errorf("token not yet valid")
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

func (p *JWTAuthProvider) refreshKeys(ctx context.Context) {
	if p.config.JWKSURL == "" {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.config.JWKSURL, nil)
	if err != nil {
		return
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var jwks struct {
		Keys []json.RawMessage `json:"keys"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return
	}

	keys := make(map[string]crypto.PublicKey)

	for _, raw := range jwks.Keys {
		var jwk struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		}

		if err := json.Unmarshal(raw, &jwk); err != nil {
			continue
		}

		if jwk.Use != "" && jwk.Use != "sig" {
			continue
		}

		var key crypto.PublicKey

		switch jwk.Kty {
		case "RSA":
			key = parseRSAKey(jwk.N, jwk.E)
		case "EC":
			key = parseECKey(jwk.Crv, jwk.X, jwk.Y)
		}

		if key != nil && jwk.Kid != "" {
			keys[jwk.Kid] = key
		}
	}

	if len(keys) > 0 {
		p.mu.Lock()
		p.keys = keys
		p.mu.Unlock()
	}
}

func parseRSAKey(nStr, eStr string) *rsa.PublicKey {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil
	}

	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil
	}

	n := new(big.Int).SetBytes(nBytes)

	var e int
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}

	return &rsa.PublicKey{N: n, E: e}
}

func parseECKey(crv, xStr, yStr string) *ecdsa.PublicKey {
	xBytes, err := base64.RawURLEncoding.DecodeString(xStr)
	if err != nil {
		return nil
	}

	yBytes, err := base64.RawURLEncoding.DecodeString(yStr)
	if err != nil {
		return nil
	}

	var curve elliptic.Curve

	switch crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil
	}

	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)

	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
}
