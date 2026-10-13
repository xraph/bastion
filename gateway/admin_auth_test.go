package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xraph/forge"
	forgetesting "github.com/xraph/forge/testing"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/gateway"
)

const adminRoutes = "/gateway/api/routes"

func startApp(t *testing.T, opts ...bastion.ConfigOption) forge.App {
	t.Helper()

	app := forgetesting.NewTestApp("admin-auth", "0.0.0")
	opts = append([]bastion.ConfigOption{bastion.WithDiscoveryEnabled(false)}, opts...)

	if err := app.RegisterExtension(gateway.New(opts...)); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	return app
}

func call(app forge.App, method, path, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)

	return rec
}

func TestAdminToken(t *testing.T) {
	app := startApp(t, bastion.WithAdminToken("s3cret"))

	cases := []struct {
		name, auth string
		want       int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"wrong scheme", "Basic s3cret", http.StatusUnauthorized},
		{"right token", "Bearer s3cret", http.StatusOK},
		{"scheme is case-insensitive", "bearer s3cret", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(app, http.MethodGet, adminRoutes, tc.auth)
			if rec.Code != tc.want {
				t.Fatalf("GET %s with %q = %d, want %d", adminRoutes, tc.auth, rec.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without a WWW-Authenticate header")
			}
		})
	}

	// Writes are covered too, not just reads.
	if rec := call(app, http.MethodDelete, adminRoutes+"/anything", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("DELETE without token = %d, want 401", rec.Code)
	}
}

// The config endpoint must not hand the token back to whoever holds it.
func TestAdminToken_NotInConfigResponse(t *testing.T) {
	app := startApp(t, bastion.WithAdminToken("s3cret"))

	rec := call(app, http.MethodGet, "/gateway/api/config", "Bearer s3cret")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatal("admin config response contains the admin token")
	}
}

func TestAdminMiddleware(t *testing.T) {
	var calls atomic.Int32

	deny := func(next forge.Handler) forge.Handler {
		return func(ctx forge.Context) error {
			calls.Add(1)
			if ctx.Request().Header.Get("X-Operator") == "" {
				return ctx.JSON(http.StatusForbidden, map[string]string{"error": "operator only"})
			}

			return next(ctx)
		}
	}

	app := startApp(t, bastion.WithAdminMiddleware(deny))

	if rec := call(app, http.MethodGet, adminRoutes, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("GET without operator header = %d, want 403", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, adminRoutes, nil)
	req.Header.Set("X-Operator", "rex")
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET with operator header = %d, want 200", rec.Code)
	}
	if calls.Load() != 2 {
		t.Errorf("middleware ran %d times, want 2", calls.Load())
	}
}

// With no auth configured the admin API still answers, as it always has.
// Refusing to register it would break every deployment that relies on it.
func TestAdminAPI_OpenByDefault(t *testing.T) {
	app := startApp(t)

	if rec := call(app, http.MethodGet, adminRoutes, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET %s with no auth configured = %d, want 200", adminRoutes, rec.Code)
	}
}
