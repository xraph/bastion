package contract

import (
	"bytes"
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

func TestManifest_NineQueriesNamedBastion(t *testing.T) {
	m := loadManifest(t)
	if m.Contributor.Name != ContributorName {
		t.Errorf("contributor = %q, want %q", m.Contributor.Name, ContributorName)
	}
	if len(m.Intents) != 9 {
		t.Errorf("intents = %d, want 9", len(m.Intents))
	}
	for _, in := range m.Intents {
		if in.Kind != dashcontract.IntentKindQuery {
			t.Errorf("%s kind = %q, want query", in.Name, in.Kind)
		}
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
		if !cached[in.Name] {
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
