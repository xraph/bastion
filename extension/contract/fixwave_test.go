package contract

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	bastion "github.com/xraph/bastion"
)

func TestServiceView_EmptyFieldsSerialiseAsEmptyListAndNull(t *testing.T) {
	b, err := json.Marshal(toServiceView(&bastion.DiscoveredService{Name: "billing"}))
	if err != nil {
		t.Fatal(err)
	}

	got := string(b)
	if !strings.Contains(got, `"protocols":[]`) {
		t.Errorf("json = %s, want protocols as an empty list", got)
	}
	if !strings.Contains(got, `"discoveredAt":null`) {
		t.Errorf("json = %s, want discoveredAt null for an unset time", got)
	}

	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	v := toServiceView(&bastion.DiscoveredService{Name: "x", Protocols: []string{"http"}, DiscoveredAt: at})
	if v.DiscoveredAt == nil || !v.DiscoveredAt.Equal(at) || len(v.Protocols) != 1 {
		t.Errorf("view = %+v", v)
	}
}

func TestSpecView_RedactsURLCredentials(t *testing.T) {
	v := toSpecView(&bastion.ServiceOpenAPISpec{ServiceName: "s", SpecURL: "http://u:pw@svc:1/openapi.json"})
	if v.SpecURL != "http://u:xxxxx@svc:1/openapi.json" {
		t.Errorf("specUrl = %q", v.SpecURL)
	}
}
