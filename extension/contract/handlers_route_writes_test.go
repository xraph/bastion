package contract

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
)

func code(err error) (contract.ErrorCode, map[string]any) {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code, ce.Details
	}
	return "", nil
}

func boolp(b bool) *bool { return &b }
func intp(i int) *int    { return &i }

func seedOrders(t *testing.T, rm interface{ AddRoute(*bastion.Route) error }) {
	t.Helper()
	if err := rm.AddRoute(&bastion.Route{
		ID: "r1", Path: "/gw/orders", Source: bastion.SourceManual, Priority: 105, Enabled: true,
		Methods:   []string{"GET"},
		Headers:   bastion.HeaderPolicy{Set: map[string]string{"Authorization": "Bearer real"}},
		Transform: &bastion.TransformConfig{RequestHeaders: bastion.HeaderPolicy{Add: map[string]string{"X-A": "1"}}},
		Timeout:   &bastion.TimeoutConfig{Read: 5},
		RateLimit: &bastion.RateLimitConfig{Enabled: true, RequestsPerSec: 5, Burst: 10},
		Targets: []*bastion.Target{{ID: "r1/0", URL: "http://u:secret@orders:8080", Weight: 1,
			Metadata: map[string]string{"health_check_path": "/healthz"}}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutesCreate_AppliesInputAndAnswersID(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)
	out, err := routesCreateHandler(deps)(context.Background(), routesCreateRequest{
		Path: "/billing", Methods: []string{"GET"}, Priority: 3, Enabled: true,
		Targets: []targetInput{{URL: "http://billing:9000", Weight: 1}},
	}, contract.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := deps.Admin.GetRoute(out.ID)
	if err != nil || d.Path != "/gw/billing" || d.Priority != 103 {
		t.Errorf("created route = %+v, %v", d, err)
	}
}

func TestRoutesCreate_RefusalsCarryDetails(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	seedOrders(t, rm)
	h := routesCreateHandler(deps)

	_, err := h(context.Background(), routesCreateRequest{Path: "/x"}, contract.Principal{})
	if c, d := code(err); c != contract.CodeBadRequest || d["field"] != "targets" {
		t.Errorf("no targets: %v %v", c, d)
	}

	_, err = h(context.Background(), routesCreateRequest{Path: "/orders", Methods: []string{"GET"},
		Targets: []targetInput{{URL: "http://o:1"}}}, contract.Principal{})
	if c, d := code(err); c != contract.CodeConflict || d["reason"] != "duplicate" || d["routeId"] != "r1" {
		t.Errorf("duplicate: %v %v", c, d)
	}
}

func TestRoutesUpdate_PriorityOnlyKeepsEverythingElse(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	seedOrders(t, rm)

	if _, err := routesUpdateHandler(deps)(context.Background(), routesUpdateRequest{ID: "r1", Priority: intp(7)}, contract.Principal{}); err != nil {
		t.Fatal(err)
	}
	r, _ := rm.GetRoute("r1")
	if r.Priority != 107 || r.Path != "/gw/orders" {
		t.Errorf("priority/path = %d/%q", r.Priority, r.Path)
	}
	if r.Headers.Set["Authorization"] != "Bearer real" || r.Transform.RequestHeaders.Add["X-A"] != "1" ||
		r.Timeout == nil || r.RateLimit == nil || r.Targets[0].Metadata["health_check_path"] != "/healthz" ||
		r.Targets[0].URL != "http://u:secret@orders:8080" {
		t.Errorf("a field the editor did not send changed: %+v", r)
	}
}

func TestRoutesUpdate_MaskedURLMapsBackToTheStoredOne(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	seedOrders(t, rm)

	// What the editor sends back after loading routes.detail.
	targets := []targetInput{{URL: "http://u:xxxxx@orders:8080", Weight: 3}}
	if _, err := routesUpdateHandler(deps)(context.Background(), routesUpdateRequest{ID: "r1", Targets: &targets}, contract.Principal{}); err != nil {
		t.Fatal(err)
	}
	r, _ := rm.GetRoute("r1")
	if r.Targets[0].URL != "http://u:secret@orders:8080" {
		t.Errorf("stored URL = %q; the masked password was saved", r.Targets[0].URL)
	}
	if r.Targets[0].Weight != 3 || r.Targets[0].Metadata["health_check_path"] != "/healthz" {
		t.Errorf("target = %+v, want weight 3 and its metadata kept", r.Targets[0])
	}
}

func TestRoutesUpdate_RateLimitAbsentNullObject(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	seedOrders(t, rm)
	h := routesUpdateHandler(deps)

	_, _ = h(context.Background(), routesUpdateRequest{ID: "r1", Enabled: boolp(true)}, contract.Principal{})
	if r, _ := rm.GetRoute("r1"); r.RateLimit == nil {
		t.Fatal("absent rateLimit cleared the override")
	}

	_, _ = h(context.Background(), routesUpdateRequest{ID: "r1", RateLimit: json.RawMessage(`{"enabled":true,"requestsPerSec":1,"burst":2}`)}, contract.Principal{})
	if r, _ := rm.GetRoute("r1"); r.RateLimit == nil || r.RateLimit.Burst != 2 {
		t.Fatalf("object did not replace: %+v", r.RateLimit)
	}

	_, _ = h(context.Background(), routesUpdateRequest{ID: "r1", RateLimit: json.RawMessage(`null`)}, contract.Principal{})
	if r, _ := rm.GetRoute("r1"); r.RateLimit != nil {
		t.Error("null did not clear the override")
	}

	_, err := h(context.Background(), routesUpdateRequest{ID: "r1", RateLimit: json.RawMessage(`"nope"`)}, contract.Principal{})
	if c, d := code(err); c != contract.CodeBadRequest || d["field"] != "rateLimit" {
		t.Errorf("bad rateLimit: %v %v", c, d)
	}
}

func TestRouteWrites_RefuseDiscoveredRoutes(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "farp-x", Path: "/x", Source: bastion.SourceFARP})

	_, err := routesUpdateHandler(deps)(context.Background(), routesUpdateRequest{ID: "farp-x", Priority: intp(1)}, contract.Principal{})
	if c, d := code(err); c != contract.CodeConflict || d["reason"] != "source" || d["source"] != "farp" {
		t.Errorf("update: %v %v", c, d)
	}
	_, err = routesSetEnabledHandler(deps)(context.Background(), routesSetEnabledRequest{ID: "farp-x", Enabled: boolp(false)}, contract.Principal{})
	if c, _ := code(err); c != contract.CodeConflict {
		t.Errorf("setEnabled: %v", c)
	}
}

func TestRoutesSetEnabled_DurabilityAndRequiredField(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "manual-/users", Path: "/gw/users", Source: bastion.SourceManual, Enabled: true})
	h := routesSetEnabledHandler(deps)

	if _, err := h(context.Background(), routesSetEnabledRequest{ID: "manual-/users"}, contract.Principal{}); func() bool { c, _ := code(err); return c != contract.CodeBadRequest }() {
		t.Errorf("missing enabled: err = %v, want BAD_REQUEST", err)
	}
	out, err := h(context.Background(), routesSetEnabledRequest{ID: "manual-/users", Enabled: boolp(false)}, contract.Principal{})
	if err != nil || out.Enabled || out.Durable {
		t.Errorf("out = %+v, err = %v; want disabled and not durable", out, err)
	}
}

func TestRoutesDelete(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	seedOrders(t, rm)
	out, err := routesDeleteHandler(deps)(context.Background(), routesDeleteRequest{ID: "r1"}, contract.Principal{})
	if err != nil || !out.OK {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	_, err = routesDeleteHandler(deps)(context.Background(), routesDeleteRequest{ID: "r1"}, contract.Principal{})
	if c, _ := code(err); c != contract.CodeNotFound {
		t.Errorf("second delete: %v", c)
	}
}
