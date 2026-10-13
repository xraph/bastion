package bastion_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/bastion"
)

// registerWithYAML registers a core gateway built from opts with an app
// whose config.yaml holds yaml (no file at all when yaml is ""), and
// returns the config the gateway ended up with.
func registerWithYAML(t *testing.T, yaml string, opts ...bastion.ConfigOption) bastion.Config {
	t.Helper()

	dir := t.TempDir()
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	app := forge.New(
		forge.WithAppName("bastion-config-precedence"),
		forge.WithAppLogger(forge.NewNoopLogger()),
		forge.WithConfigSearchPaths(dir),
	)

	gw, ok := bastion.New(opts...).(*bastion.Gateway)
	if !ok {
		t.Fatal("bastion.New did not return a *bastion.Gateway")
	}

	if err := gw.Register(app); err != nil {
		t.Fatalf("Register: %v", err)
	}

	return gw.Config()
}

// A YAML value inside a block that has defaults used to be dropped,
// because the defaults went in as the programmatic config and overrode
// the whole block.
func TestRegister_YAMLOnlyValuesAreHonoured(t *testing.T) {
	cfg := registerWithYAML(t, `
extensions:
  bastion:
    dashboard:
      admin_token: from-yaml
    health_check:
      path: /healthz
`)

	if got, want := cfg.Dashboard.AdminToken, "from-yaml"; got != want {
		t.Errorf("Dashboard.AdminToken = %q, want %q", got, want)
	}
	if got, want := cfg.HealthCheck.Path, "/healthz"; got != want {
		t.Errorf("HealthCheck.Path = %q, want %q", got, want)
	}

	// Keys the file leaves out keep their defaults, in the same blocks.
	def := bastion.DefaultConfig()
	if got, want := cfg.Dashboard.BasePath, def.Dashboard.BasePath; got != want {
		t.Errorf("Dashboard.BasePath = %q, want default %q", got, want)
	}
	if !cfg.Dashboard.Enabled {
		t.Error("Dashboard.Enabled = false, want default true")
	}
	if got, want := cfg.HealthCheck.Interval, def.HealthCheck.Interval; got != want {
		t.Errorf("HealthCheck.Interval = %v, want default %v", got, want)
	}
}

// The legacy top-level "bastion" key goes through the same path.
func TestRegister_YAMLLegacyKeyIsHonoured(t *testing.T) {
	cfg := registerWithYAML(t, `
bastion:
  dashboard:
    admin_token: legacy-yaml
`)

	if got, want := cfg.Dashboard.AdminToken, "legacy-yaml"; got != want {
		t.Errorf("Dashboard.AdminToken = %q, want %q", got, want)
	}
}

func TestRegister_CodeOptionBeatsYAML(t *testing.T) {
	hc := bastion.DefaultConfig().HealthCheck
	hc.Path = "/from-code"

	cfg := registerWithYAML(t, `
extensions:
  bastion:
    dashboard:
      admin_token: from-yaml
      base_path: /yaml-admin
    health_check:
      path: /from-yaml
`, bastion.WithAdminToken("from-code"), bastion.WithHealthCheck(hc))

	if got, want := cfg.Dashboard.AdminToken, "from-code"; got != want {
		t.Errorf("Dashboard.AdminToken = %q, want %q", got, want)
	}
	if got, want := cfg.HealthCheck.Path, "/from-code"; got != want {
		t.Errorf("HealthCheck.Path = %q, want %q", got, want)
	}

	// WithAdminToken sets one field. The rest of the YAML dashboard block
	// stays.
	if got, want := cfg.Dashboard.BasePath, "/yaml-admin"; got != want {
		t.Errorf("Dashboard.BasePath = %q, want YAML %q", got, want)
	}
}

// Booleans whose default is true are where a zero-value merge goes wrong:
// false can't be told from unset. Both directions have to work.
func TestRegister_BoolsDefaultingToTrue(t *testing.T) {
	t.Run("YAML false beats a true default", func(t *testing.T) {
		cfg := registerWithYAML(t, `
extensions:
  bastion:
    dashboard:
      enabled: false
    health_check:
      enabled: false
`)

		if cfg.Dashboard.Enabled {
			t.Error("Dashboard.Enabled = true, want false from YAML")
		}
		if cfg.HealthCheck.Enabled {
			t.Error("HealthCheck.Enabled = true, want false from YAML")
		}
	})

	t.Run("code false beats YAML true", func(t *testing.T) {
		cfg := registerWithYAML(t, `
extensions:
  bastion:
    dashboard:
      enabled: true
    discovery:
      enabled: true
`, bastion.WithDashboardEnabled(false), bastion.WithDiscoveryEnabled(false))

		if cfg.Dashboard.Enabled {
			t.Error("Dashboard.Enabled = true, want false from code")
		}
		if cfg.Discovery.Enabled {
			t.Error("Discovery.Enabled = true, want false from code")
		}
	})

	t.Run("code false with no YAML", func(t *testing.T) {
		cfg := registerWithYAML(t, "", bastion.WithEnabled(false))

		if cfg.Enabled {
			t.Error("Enabled = true, want false from code")
		}
	})
}

func TestRegister_DefaultsWhenNothingIsSet(t *testing.T) {
	cfg := registerWithYAML(t, "")
	def := bastion.DefaultConfig()

	if cfg.Dashboard.AdminToken != "" {
		t.Errorf("Dashboard.AdminToken = %q, want empty", cfg.Dashboard.AdminToken)
	}
	if cfg.HealthCheck.Path != "" {
		t.Errorf("HealthCheck.Path = %q, want empty (lenient default probe)", cfg.HealthCheck.Path)
	}
	if got, want := cfg.Dashboard.BasePath, def.Dashboard.BasePath; got != want {
		t.Errorf("Dashboard.BasePath = %q, want %q", got, want)
	}
	if !cfg.Enabled || !cfg.Dashboard.Enabled || !cfg.HealthCheck.Enabled || !cfg.Discovery.Enabled {
		t.Errorf("Enabled flags = gateway %v, dashboard %v, health %v, discovery %v; want all true",
			cfg.Enabled, cfg.Dashboard.Enabled, cfg.HealthCheck.Enabled, cfg.Discovery.Enabled)
	}
	if got, want := cfg.HealthCheck.Interval, 10*time.Second; got != want {
		t.Errorf("HealthCheck.Interval = %v, want %v", got, want)
	}
}

// Everything set in code, nothing in YAML: the code config comes through
// whole, as it did before.
func TestRegister_CodeOnlyConfigIsKept(t *testing.T) {
	cfg := registerWithYAML(t, "",
		bastion.WithBasePath("/gw"),
		bastion.WithAdminToken("code-only"),
		bastion.WithRoute(bastion.RouteConfig{Path: "/svc/*", Enabled: true}),
	)

	if cfg.BasePath != "/gw" {
		t.Errorf("BasePath = %q, want /gw", cfg.BasePath)
	}
	if cfg.Dashboard.AdminToken != "code-only" {
		t.Errorf("Dashboard.AdminToken = %q, want code-only", cfg.Dashboard.AdminToken)
	}
	if len(cfg.Routes) != 1 || cfg.Routes[0].Path != "/svc/*" {
		t.Errorf("Routes = %+v, want the one route set in code", cfg.Routes)
	}
}
