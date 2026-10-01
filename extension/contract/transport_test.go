package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	dashcontract "github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"
	"github.com/xraph/forge/extensions/dashboard/contract/transport"

	bastion "github.com/xraph/bastion"
)

// TestQueriesOverTheWire posts real envelopes through forge's transport, so
// the JSON field names and the id-in-params path are what a browser sees.
func TestQueriesOverTheWire(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "manual-/users", Path: "/gw/users", Source: bastion.SourceManual, Priority: 100})

	reg := dashcontract.NewRegistry()
	wreg := dashcontract.NewWardenRegistry()
	d := dispatcher.New(nil)
	if err := Register(d, reg, wreg, deps); err != nil {
		t.Fatal(err)
	}
	h := transport.NewHandler(reg, wreg, d, nil)

	post := func(intent, params string) map[string]any {
		body := `{"envelope":"v1","kind":"query","contributor":"bastion","intent":"` + intent + `","params":` + params + `}`
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/dashboard/v1", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		var resp struct {
			OK   bool           `json:"ok"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !resp.OK {
			t.Fatalf("%s: %s", intent, rec.Body)
		}
		return resp.Data
	}

	detail := post("routes.detail", `{"id":"manual-/users"}`)
	input, _ := detail["input"].(map[string]any)
	if detail["id"] != "manual-/users" || input["path"] != "/users" || input["priority"] != float64(0) {
		t.Errorf("routes.detail = %v", detail)
	}

	overview := post("overview.stats", `{}`)
	if _, ok := overview["errorRate"]; !ok || overview["errorRate"] != nil {
		t.Errorf("overview.errorRate must be present and null on an idle gateway: %v", overview)
	}
}

func TestCommandOverTheWireCarriesInvalidates(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "manual-/users", Path: "/gw/users", Source: bastion.SourceManual, Enabled: true})

	reg := dashcontract.NewRegistry()
	wreg := dashcontract.NewWardenRegistry()
	d := dispatcher.New(nil)
	if err := Register(d, reg, wreg, deps); err != nil {
		t.Fatal(err)
	}

	body := `{"envelope":"v1","kind":"command","contributor":"bastion","intent":"routes.setEnabled",` +
		`"csrf":"test","idempotencyKey":"test","payload":{"id":"manual-/users","enabled":false}}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/dashboard/v1", strings.NewReader(body))
	rec := httptest.NewRecorder()
	transport.NewHandler(reg, wreg, d, nil).ServeHTTP(rec, req)

	var resp dashcontract.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !resp.OK {
		t.Fatalf("response: %s", rec.Body)
	}
	want := []string{"routes.list", "routes.detail", "overview.stats"}
	if !reflect.DeepEqual(resp.Meta.Invalidates, want) {
		t.Errorf("meta.invalidates = %v, want %v", resp.Meta.Invalidates, want)
	}
}
