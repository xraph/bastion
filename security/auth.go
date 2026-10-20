package security

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xraph/forge"
	"go.opentelemetry.io/otel/attribute"

	"github.com/xraph/bastion/internal/telemetry"
)

// Auth handles authentication for gateway requests.
// It operates at the HTTP transport level (before request proxying),
// supporting multiple auth strategies and per-route configuration.
type Auth struct {
	config    AuthConfig
	logger    forge.Logger
	forgeAuth AuthRegistry // Optional integration with Forge auth extension

	// mu guards providers and is all the request path takes.
	mu        sync.RWMutex
	providers map[string]AuthProvider

	// lifeMu serialises Start, Stop and the starting and stopping
	// RegisterProvider does, so a provider can't be started after Stop or
	// left running once it's replaced. The request path never takes it.
	lifeMu  sync.Mutex
	started bool // guarded by lifeMu
}

// providerLifecycle is a provider with background work of its own, such as
// the JWT provider's JWKS refresh. Auth.Start and Auth.Stop drive it.
type providerLifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// startWarner is a provider with a configuration problem worth a warning
// each time it starts, such as a JWKS fetched over plain HTTP.
type startWarner interface {
	startWarning() string
}

// NewAuth creates a new gateway auth handler.
func NewAuth(config AuthConfig, logger forge.Logger) *Auth {
	return &Auth{
		config:    config,
		logger:    logger,
		providers: make(map[string]AuthProvider),
	}
}

// SetForgeAuth sets the optional Forge auth extension integration.
func (ga *Auth) SetForgeAuth(registry AuthRegistry) {
	ga.forgeAuth = registry
}

// RegisterProvider registers a custom gateway-level auth provider.
//
// A provider with background work, such as the JWT provider, runs with the
// gateway: Start starts it, Stop stops it. One registered while the gateway
// is already running is started here before RegisterProvider returns. That
// covers a Forge PhaseAfterRegister hook on Forge's default boot path,
// where those hooks run after the extensions start; with CentralMigrations
// they run before, and Start picks the provider up. A provider it replaces
// is stopped.
func (ga *Auth) RegisterProvider(provider AuthProvider) {
	name := provider.Name()

	ga.lifeMu.Lock()
	defer ga.lifeMu.Unlock()

	ga.mu.Lock()
	old := ga.providers[name]
	ga.providers[name] = provider
	ga.mu.Unlock()

	if !ga.started {
		return
	}

	// Background, not a caller's context: the work belongs to the gateway
	// and ends with Stop.
	ctx := context.Background()

	if lp, ok := old.(providerLifecycle); ok {
		ga.stopProvider(ctx, name, lp)
	}

	if lp, ok := provider.(providerLifecycle); ok {
		ga.startProvider(ctx, name, lp)
	}
}

// Start starts the registered providers that have background work, so the
// JWT provider holds its keys before the gateway takes traffic, and has
// RegisterProvider start any registered later. Gateway calls it from its
// own Start, and it does nothing while auth is disabled. A provider that
// isn't ready afterwards is logged and stays registered; the JWT provider
// rejects every token until it has a key.
func (ga *Auth) Start(ctx context.Context) {
	if !ga.config.Enabled {
		return
	}

	ga.lifeMu.Lock()
	defer ga.lifeMu.Unlock()

	ga.started = true

	for name, lp := range ga.lifecycles() {
		ga.startProvider(ctx, name, lp)
	}
}

// Stop stops the background work of every registered provider. Gateway
// calls it from its own Stop.
func (ga *Auth) Stop(ctx context.Context) {
	ga.lifeMu.Lock()
	defer ga.lifeMu.Unlock()

	ga.started = false

	for name, lp := range ga.lifecycles() {
		ga.stopProvider(ctx, name, lp)
	}
}

func (ga *Auth) startProvider(ctx context.Context, name string, lp providerLifecycle) {
	if w, ok := lp.(startWarner); ok {
		if msg := w.startWarning(); msg != "" {
			ga.logger.Warn(msg, forge.F("provider", name))
		}
	}

	if err := lp.Start(ctx); err != nil {
		ga.logger.Warn("auth provider is not ready",
			forge.F("provider", name),
			forge.F("error", err.Error()),
		)
	}
}

func (ga *Auth) stopProvider(ctx context.Context, name string, lp providerLifecycle) {
	if err := lp.Stop(ctx); err != nil {
		ga.logger.Warn("auth provider did not stop cleanly",
			forge.F("provider", name),
			forge.F("error", err.Error()),
		)
	}
}

// lifecycles returns the registered providers that have background work.
func (ga *Auth) lifecycles() map[string]providerLifecycle {
	ga.mu.RLock()
	defer ga.mu.RUnlock()

	out := make(map[string]providerLifecycle)

	for name, p := range ga.providers {
		if lp, ok := p.(providerLifecycle); ok {
			out[name] = lp
		}
	}

	return out
}

// Authenticate checks if a request is authenticated based on configuration.
// It supports:
//   - Global auth (applied to all routes unless skipped)
//   - Per-route auth overrides (specific providers, scopes, skip)
//   - Multiple providers tried in order (OR logic)
//   - Auth context forwarding to upstream via headers
func (ga *Auth) Authenticate(r *http.Request, route Route) (*AuthContext, error) {
	if !ga.config.Enabled {
		return nil, nil
	}

	// Check per-route auth config
	routeAuth := route.GetAuth()
	if routeAuth != nil && routeAuth.SkipAuth {
		return nil, nil
	}

	if routeAuth != nil && !routeAuth.Enabled {
		return nil, nil
	}

	// Determine which providers to use
	providerNames := ga.config.Providers
	if routeAuth != nil && len(routeAuth.Providers) > 0 {
		providerNames = routeAuth.Providers
	}

	if len(providerNames) == 0 {
		// No providers configured - check for default policy
		if ga.config.DefaultPolicy == "allow" {
			return nil, nil
		}
		// Default deny when auth is enabled but no providers
		return nil, &AuthError{Code: http.StatusUnauthorized, Message: "no auth providers configured"}
	}

	// The span covers the providers, so a forward-auth call shows up
	// inside it. A denial is the answer auth exists to give, not a failure,
	// so it only sets bastion.auth.result. An auth service that couldn't
	// answer does fail the span.
	ctx, span := telemetry.Start(r.Context(), "authenticate request")
	defer span.End()

	r = r.WithContext(ctx)

	authCtx, err := ga.authenticate(r, routeAuth, providerNames)

	result := "ok"
	if err != nil {
		result = AuthFailureClass(err)
	}

	span.SetAttributes(attribute.String("bastion.auth.result", result))

	if result == AuthFailureUnavailable {
		telemetry.Fail(span, "auth unavailable", nil)
	}

	if authCtx != nil {
		span.SetAttributes(attribute.String("bastion.auth.provider", authCtx.ProviderName))
	}

	return authCtx, err
}

// authenticate tries providerNames in order, the first success winning.
func (ga *Auth) authenticate(r *http.Request, routeAuth *RouteAuthConfig, providerNames []string) (*AuthContext, error) {
	var lastErr error
	for _, name := range providerNames {
		provider := ga.getProvider(name)
		if provider == nil {
			ga.logger.Debug("auth provider not found",
				forge.F("provider", name),
			)
			continue
		}

		authCtx, err := provider.Authenticate(r.Context(), r)
		if err != nil {
			lastErr = err

			fields := []forge.Field{forge.F("provider", name), forge.F("error", err)}

			// A provider can keep its client-facing message fixed and put
			// the reason underneath it.
			if reason := errors.Unwrap(err); reason != nil {
				fields = append(fields, forge.F("reason", reason.Error()))
			}

			ga.logger.Debug("auth provider failed", fields...)

			continue
		}

		if authCtx == nil {
			continue
		}

		// Check scopes if configured
		if routeAuth != nil && len(routeAuth.Scopes) > 0 {
			if !authCtx.HasScopes(routeAuth.Scopes...) {
				return nil, &AuthError{
					Code:    http.StatusForbidden,
					Message: "insufficient scopes",
				}
			}
		}

		authCtx.ProviderName = name
		return authCtx, nil
	}

	// All providers failed
	if lastErr != nil {
		return nil, &AuthError{Code: http.StatusUnauthorized, Message: lastErr.Error(), cause: lastErr}
	}

	return nil, &AuthError{Code: http.StatusUnauthorized, Message: "authentication required"}
}

// forwardedAuthHeaders are the identity headers ForwardAuthHeaders sets on
// the upstream request.
var forwardedAuthHeaders = [...]string{
	"X-Auth-Subject",
	"X-Auth-Provider",
	"X-Auth-Scopes",
	"X-Auth-Role",
	"X-Auth-Email",
}

// DropAuthHeaders deletes the identity headers ForwardAuthHeaders sets
// (X-Auth-Subject, X-Auth-Provider, X-Auth-Scopes, X-Auth-Role and
// X-Auth-Email) from h. The proxy calls it on every inbound request before
// authentication, so the copies an upstream sees are always ones the
// gateway set. Other X-Auth- headers, such as a token an API key provider
// reads, are left alone.
func DropAuthHeaders(h http.Header) {
	for _, name := range forwardedAuthHeaders {
		h.Del(name)
	}
}

// ForwardAuthHeaders adds authentication context to upstream request headers.
// This allows upstream services to access the authenticated user info
// without re-authenticating. It clears the five identity headers first, so
// a claim the token doesn't carry is never filled in from the request.
func (ga *Auth) ForwardAuthHeaders(r *http.Request, authCtx *AuthContext) {
	DropAuthHeaders(r.Header)

	if authCtx == nil || !ga.config.ForwardHeaders {
		return
	}

	r.Header.Set("X-Auth-Subject", authCtx.Subject)
	r.Header.Set("X-Auth-Provider", authCtx.ProviderName)

	if len(authCtx.Scopes) > 0 {
		r.Header.Set("X-Auth-Scopes", strings.Join(authCtx.Scopes, ","))
	}

	// Forward selected claims as headers
	if role, ok := authCtx.Claims["role"]; ok {
		if roleStr, isStr := role.(string); isStr {
			r.Header.Set("X-Auth-Role", roleStr)
		}
	}

	if email, ok := authCtx.Claims["email"]; ok {
		if emailStr, isStr := email.(string); isStr {
			r.Header.Set("X-Auth-Email", emailStr)
		}
	}
}

// getProvider resolves a provider by name, checking both local providers
// and the optional Forge auth extension registry.
func (ga *Auth) getProvider(name string) AuthProvider {
	// Check local gateway providers first
	ga.mu.RLock()
	p, ok := ga.providers[name]
	ga.mu.RUnlock()

	if ok {
		return p
	}

	// Fall back to Forge auth extension
	if ga.forgeAuth != nil {
		if p, ok := ga.forgeAuth.Get(name); ok {
			return p
		}
	}

	return nil
}

// --- Built-in Auth Providers ---

// APIKeyAuthProvider validates requests using API keys in headers or query params.
type APIKeyAuthProvider struct {
	name       string
	header     string
	queryParam string
	keys       map[string]*APIKeyEntry
}

// APIKeyEntry represents a registered API key with associated metadata.
type APIKeyEntry struct {
	Key      string         `json:"key"`
	Subject  string         `json:"subject"`
	Scopes   []string       `json:"scopes,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// NewAPIKeyAuthProvider creates an API key auth provider.
func NewAPIKeyAuthProvider(name, header, queryParam string, keys []*APIKeyEntry) *APIKeyAuthProvider {
	keyMap := make(map[string]*APIKeyEntry, len(keys))
	for _, k := range keys {
		keyMap[k.Key] = k
	}

	if header == "" {
		header = "X-API-Key"
	}

	return &APIKeyAuthProvider{
		name:       name,
		header:     header,
		queryParam: queryParam,
		keys:       keyMap,
	}
}

func (p *APIKeyAuthProvider) Name() string { return p.name }

func (p *APIKeyAuthProvider) Authenticate(_ context.Context, r *http.Request) (*AuthContext, error) {
	// Check header first
	key := r.Header.Get(p.header)

	// Fall back to query param
	if key == "" && p.queryParam != "" {
		key = r.URL.Query().Get(p.queryParam)
	}

	if key == "" {
		return nil, &AuthError{Code: http.StatusUnauthorized, Message: "API key required"}
	}

	entry, ok := p.keys[key]
	if !ok {
		return nil, &AuthError{Code: http.StatusUnauthorized, Message: "invalid API key"}
	}

	return &AuthContext{
		Subject: entry.Subject,
		Scopes:  entry.Scopes,
		Claims:  map[string]any{"api_key": true},
	}, nil
}

// BearerTokenAuthProvider validates Bearer tokens by forwarding them to a
// validation endpoint or using a configurable validation function.
type BearerTokenAuthProvider struct {
	name     string
	validate func(ctx context.Context, token string) (*AuthContext, error)
}

// NewBearerTokenAuthProvider creates a bearer token auth provider with a custom validator.
func NewBearerTokenAuthProvider(name string, validate func(ctx context.Context, token string) (*AuthContext, error)) *BearerTokenAuthProvider {
	return &BearerTokenAuthProvider{
		name:     name,
		validate: validate,
	}
}

func (p *BearerTokenAuthProvider) Name() string { return p.name }

func (p *BearerTokenAuthProvider) Authenticate(ctx context.Context, r *http.Request) (*AuthContext, error) {
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

	return p.validate(ctx, token)
}

// ForwardAuthProvider delegates authentication to an external auth service.
// It sends the original request headers to the auth endpoint and uses
// the response to determine authentication status.
type ForwardAuthProvider struct {
	name     string
	endpoint string
	headers  []string // Headers to forward to auth service
	client   *http.Client
}

// NewForwardAuthProvider creates a forward auth provider.
func NewForwardAuthProvider(name, endpoint string, headers []string) *ForwardAuthProvider {
	if len(headers) == 0 {
		headers = []string{"Authorization", "Cookie", "X-Forwarded-For"}
	}

	return &ForwardAuthProvider{
		name:     name,
		endpoint: endpoint,
		headers:  headers,
		client: &http.Client{
			Timeout: 5 * time.Second,
			// The call gets a client span and carries the trace context.
			Transport: telemetry.NewTransport(nil, "call forward auth"),
		},
	}
}

func (p *ForwardAuthProvider) Name() string { return p.name }

func (p *ForwardAuthProvider) Authenticate(ctx context.Context, r *http.Request) (*AuthContext, error) {
	// #nosec G704 -- p.endpoint is the forward-auth service configured at
	// construction, not anything derived from r. Only header values are
	// forwarded from the incoming request, and those cannot change the target.
	authReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint, nil)
	if err != nil {
		return nil, &AuthError{Code: http.StatusInternalServerError, Message: "failed to create auth request"}
	}

	// Forward configured headers
	for _, h := range p.headers {
		if v := r.Header.Get(h); v != "" {
			authReq.Header.Set(h, v)
		}
	}

	// Forward original request info
	authReq.Header.Set("X-Original-URI", r.RequestURI)
	authReq.Header.Set("X-Original-Method", r.Method)

	// #nosec G704 -- authReq targets the configured endpoint; see above.
	resp, err := p.client.Do(authReq)
	if err != nil {
		return nil, &AuthError{Code: http.StatusBadGateway, Message: "auth service unavailable"}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &AuthError{Code: resp.StatusCode, Message: "authentication failed"}
	}

	// Extract auth context from response headers
	authCtx := &AuthContext{
		Subject: resp.Header.Get("X-Auth-Subject"),
		Claims:  make(map[string]any),
	}

	if authCtx.Subject == "" {
		authCtx.Subject = "authenticated"
	}

	if role := resp.Header.Get("X-Auth-Role"); role != "" {
		authCtx.Claims["role"] = role
	}

	if scopes := resp.Header.Get("X-Auth-Scopes"); scopes != "" {
		authCtx.Scopes = strings.Split(scopes, ",")
	}

	return authCtx, nil
}
