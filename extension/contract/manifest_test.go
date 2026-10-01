package contract

import (
	"bytes"
	"reflect"
	"testing"

	dashcontract "github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/loader"
)

func loadManifest(t *testing.T) *dashcontract.ContractManifest {
	t.Helper()
	m, err := loader.Load(bytes.NewReader(manifestYAML), "bastion/contract/manifest.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return m
}

func TestManifest_IntentsNamedBastion(t *testing.T) {
	m := loadManifest(t)
	if m.Contributor.Name != ContributorName {
		t.Errorf("contributor = %q", m.Contributor.Name)
	}
	queries, commands := 0, 0
	for _, in := range m.Intents {
		switch in.Kind {
		case dashcontract.IntentKindQuery:
			queries++
		case dashcontract.IntentKindCommand:
			commands++
		}
	}
	if queries != 9 || commands != 7 {
		t.Errorf("queries/commands = %d/%d, want 9/7", queries, commands)
	}
}

func TestManifest_CommandsInvalidateExactly(t *testing.T) {
	routeWrite := []string{"routes.list", "routes.detail", "upstreams.list", "overview.stats", "traffic.stats", "circuits.list"}
	want := map[string][]string{
		"routes.create":     routeWrite,
		"routes.update":     routeWrite,
		"routes.delete":     routeWrite,
		"routes.setEnabled": {"routes.list", "routes.detail", "overview.stats"},
		"discovery.refresh": {"services.list", "routes.list", "routes.detail", "upstreams.list", "overview.stats", "openapi.summary"},
		"openapi.refresh":   {"openapi.summary"},
		"circuits.reset":    {"circuits.list", "upstreams.list", "routes.detail", "overview.stats"},
	}
	seen := 0
	for _, in := range loadManifest(t).Intents {
		if w, ok := want[in.Name]; ok {
			seen++
			if !reflect.DeepEqual(in.Invalidates, w) {
				t.Errorf("%s invalidates %v, want %v", in.Name, in.Invalidates, w)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("found %d of %d commands", seen, len(want))
	}
}

func TestManifest_EveryIntentHasACacheHint(t *testing.T) {
	m := loadManifest(t)
	cached := map[string]bool{}
	for _, q := range m.Queries {
		if q.Cache == nil || q.Cache.StaleTime == "" {
			t.Errorf("query for %s has no staleTime", q.Intent)
			continue
		}
		cached[q.Intent] = true
	}
	for _, in := range m.Intents {
		if in.Kind == dashcontract.IntentKindQuery && !cached[in.Name] {
			t.Errorf("%s has no queries entry", in.Name)
		}
	}
}

func TestManifest_ValidatesAndRegisters(t *testing.T) {
	m := loadManifest(t)
	if err := loader.Validate(m, dashcontract.NewWardenRegistry()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := dashcontract.NewRegistry().Register(m); err != nil {
		t.Fatalf("register: %v", err)
	}
}
