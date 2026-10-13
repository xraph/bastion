package gateway_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	forgetesting "github.com/xraph/forge/testing"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/gateway"
)

func upstream(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream saw "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func routeTo(url string) bastion.ConfigOption {
	return bastion.WithRoute(bastion.RouteConfig{
		Path:    "/svc/*",
		Enabled: true,
		Targets: []bastion.TargetConfig{{URL: url}},
	})
}

// The point of the package: a gateway built without the extension (and so
// without its store packages) forwards requests once the app starts.
func TestNew_Proxies(t *testing.T) {
	up := upstream(t)
	app := forgetesting.NewTestApp("gateway-proxies", "0.0.0")
	gw := gateway.New(routeTo(up.URL), bastion.WithDiscoveryEnabled(false))

	if err := app.RegisterExtension(gw); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/svc/hello", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), "upstream saw /svc/hello"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	if gw.Admin() == nil {
		t.Error("Admin() = nil after Register, want the admin service")
	}
}

// bastion.New on its own still registers no catch-all. This pins the gap
// gateway.New exists to fill, so a change to the core shows up here.
func TestCoreGatewayAloneDoesNotProxy(t *testing.T) {
	up := upstream(t)
	app := forgetesting.NewTestApp("core-only", "0.0.0")

	if err := app.RegisterExtension(bastion.New(routeTo(up.URL), bastion.WithDiscoveryEnabled(false))); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/svc/hello", nil))

	if rec.Code == http.StatusOK {
		t.Fatalf("core gateway proxied (status 200); gateway.New docs need updating")
	}
}

func TestNew_DisabledGatewayWiresNothing(t *testing.T) {
	app := forgetesting.NewTestApp("gateway-disabled", "0.0.0")
	gw := gateway.New(bastion.WithEnabled(false))

	if err := app.RegisterExtension(gw); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	if gw.Admin() != nil {
		t.Error("Admin() != nil for a disabled gateway")
	}
}
