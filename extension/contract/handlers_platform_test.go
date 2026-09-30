package contract

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
)

func TestServicesList_NoDiscoveryIsEmptyAndSaysSo(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)
	out, err := servicesListHandler(deps)(context.Background(), servicesListRequest{}, contract.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Services == nil || out.Total != 0 {
		t.Errorf("out = %+v, want an empty list", out)
	}
	if out.DiscoveryEnabled != deps.Gateway.Config().Discovery.Enabled {
		t.Error("discoveryEnabled must come from config")
	}
}

func TestOpenAPISummary_NotRunning(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)
	out, _ := openapiSummaryHandler(deps)(context.Background(), openapiSummaryRequest{}, contract.Principal{})
	if out.Running || out.Services == nil || out.LastRefresh != nil {
		t.Errorf("out = %+v, want not running with an empty list", out)
	}
}

func TestConfigSections_NeverLeakTLSPathsOrIPs(t *testing.T) {
	var cfg bastion.Config
	cfg.TLS.Enabled = true
	cfg.TLS.ClientKeyFile = "/etc/bastion/secret/client.key"
	cfg.TLS.CACertFile = "/etc/bastion/ca.pem"
	cfg.IPFilter.Enabled = true
	cfg.IPFilter.AllowIPs = []string{"10.0.0.1", "10.0.0.2"}

	raw, _ := json.Marshal(configSections(cfg))
	body := string(raw)
	for _, leaked := range []string{"client.key", "ca.pem", "10.0.0.1"} {
		if strings.Contains(body, leaked) {
			t.Errorf("config detail leaked %q", leaked)
		}
	}
	if !strings.Contains(body, `"2 addresses"`) {
		t.Errorf("allow list count missing: %s", body)
	}
}

func TestConfigSections_RetryAndCacheSayTheyDoNothing(t *testing.T) {
	var cfg bastion.Config
	cfg.Retry.Enabled = true
	cfg.Caching.Enabled = true

	notes := map[string]string{}
	for _, s := range configSections(cfg) {
		notes[s.ID] = s.Note
	}
	if notes["retry"] == "" || notes["caching"] == "" {
		t.Errorf("retry and caching must carry a note: %+v", notes)
	}
}
