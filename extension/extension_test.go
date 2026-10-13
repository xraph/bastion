package extension_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	forgetesting "github.com/xraph/forge/testing"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/extension"
)

// The extension builds its proxy through gateway.Wire. This checks the
// extension still forwards traffic after that move.
func TestExtension_Proxies(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream saw "+r.URL.Path)
	}))
	t.Cleanup(up.Close)

	app := forgetesting.NewTestApp("extension-proxies", "0.0.0")
	ext := extension.New(
		bastion.WithDiscoveryEnabled(false),
		bastion.WithRoute(bastion.RouteConfig{
			Path:    "/svc/*",
			Enabled: true,
			Targets: []bastion.TargetConfig{{URL: up.URL}},
		}),
	)

	if err := app.RegisterExtension(ext); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/svc/hello", nil))

	if got, want := rec.Body.String(), "upstream saw /svc/hello"; rec.Code != http.StatusOK || got != want {
		t.Fatalf("status %d body %q, want 200 %q", rec.Code, got, want)
	}
}
