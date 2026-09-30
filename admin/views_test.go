package admin_test

import (
	"errors"
	"testing"
	"time"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

type fixture struct {
	svc *admin.Service
	rm  *routing.Manager
	cbm *resilience.CBManager
}

func newFixture(t *testing.T) fixture {
	t.Helper()

	rm := routing.NewManager()
	cbm := resilience.NewCBManager(bastion.CircuitBreakerConfig{
		Enabled: true, FailureThreshold: 1, ResetTimeout: time.Hour, HalfOpenMax: 1,
	})
	svc, err := admin.New(admin.Deps{Routes: rm, Breakers: cbm, BasePath: "/gw"})
	if err != nil {
		t.Fatal(err)
	}

	return fixture{svc: svc, rm: rm, cbm: cbm}
}

func (f fixture) add(t *testing.T, r *bastion.Route) {
	t.Helper()
	if err := f.rm.AddRoute(r); err != nil {
		t.Fatal(err)
	}
}

func TestListRoutesFiltersAndMarksEditable(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{ID: "manual-/users", Path: "/gw/users", Source: bastion.SourceManual, Protocol: bastion.ProtocolHTTP, Priority: 110, Enabled: true,
		Targets: []*bastion.Target{{ID: "t1", URL: "http://users:8080", Healthy: true}, {ID: "t2", URL: "http://users2:8080", Healthy: false}}})
	f.add(t, &bastion.Route{ID: "farp-billing-http", Path: "/billing/*", Source: bastion.SourceFARP, Protocol: bastion.ProtocolHTTP, Enabled: true})

	all := f.svc.ListRoutes(admin.RouteFilter{})
	if len(all) != 2 {
		t.Fatalf("routes = %d, want 2", len(all))
	}

	farp := f.svc.ListRoutes(admin.RouteFilter{Source: "farp"})
	if len(farp) != 1 || farp[0].ID != "farp-billing-http" || farp[0].Editable {
		t.Errorf("farp filter = %+v", farp)
	}

	users := f.svc.ListRoutes(admin.RouteFilter{Source: "manual"})[0]
	if !users.Editable || !users.Config || users.TargetCount != 2 || users.HealthyTargets != 1 {
		t.Errorf("users = %+v", users)
	}
	if users.Methods == nil {
		t.Error("methods must be an empty list, not null")
	}
}

func TestGetRouteGivesInputRedactsAndReportsCircuit(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{
		ID: "r1", Path: "/gw/orders", Source: bastion.SourceManual, Priority: 105, Enabled: true,
		Transform: &bastion.TransformConfig{RequestHeaders: bastion.HeaderPolicy{Set: map[string]string{"X-Api-Key": "k-1"}}},
		Metadata:  map[string]any{"owner": "team-a"},
		Targets: []*bastion.Target{{ID: "r1/0", URL: "http://orders:8080", Healthy: true,
			Metadata: map[string]string{"health_check_path": "/healthz", "internal": "x"}}},
	})
	f.cbm.Get("r1/0").RecordFailure() // threshold 1: open

	d, err := f.svc.GetRoute("r1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Input == nil || d.Input.Path != "/orders" || d.Input.Priority != 5 {
		t.Errorf("input = %+v, want /orders at 5", d.Input)
	}
	if d.Transform.RequestHeaders.Set["X-Api-Key"] != admin.Redacted {
		t.Errorf("transform header leaked: %+v", d.Transform)
	}
	if len(d.MetadataKeys) != 1 || d.MetadataKeys[0] != "owner" {
		t.Errorf("metadata keys = %v", d.MetadataKeys)
	}
	tv := d.Targets[0]
	if tv.CircuitState != bastion.CircuitOpen || tv.HealthCheckPath != "/healthz" {
		t.Errorf("target = %+v", tv)
	}
}

func TestGetRouteDiscoveredHasNoInput(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{ID: "farp-x", Path: "/x", Source: bastion.SourceFARP})

	d, err := f.svc.GetRoute("farp-x")
	if err != nil || d.Input != nil {
		t.Errorf("detail = %+v, err = %v; want no input for a FARP route", d, err)
	}
}

func TestGetRouteUnknown(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.GetRoute("nope"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUpstreamsGroupByURL(t *testing.T) {
	f := newFixture(t)
	shared := "http://orders:8080"
	f.add(t, &bastion.Route{ID: "a", Path: "/a", Source: bastion.SourceManual,
		Targets: []*bastion.Target{{ID: "a/0", URL: shared, Healthy: true}}})
	f.add(t, &bastion.Route{ID: "b", Path: "/b", Source: bastion.SourceManual,
		Targets: []*bastion.Target{{ID: "b/0", URL: shared, Healthy: false}, {ID: "b/1", URL: "http://users:8080", Healthy: true}}})
	f.cbm.Get("b/0").RecordFailure()

	ups := f.svc.Upstreams()
	if len(ups) != 2 || ups[0].URL != shared {
		t.Fatalf("upstreams = %+v", ups)
	}
	if ups[0].Healthy || ups[0].CircuitState != bastion.CircuitOpen || len(ups[0].Routes) != 2 {
		t.Errorf("orders = %+v, want unhealthy, open, used by 2 routes", ups[0])
	}

	refs := f.svc.Targets()
	if refs["b/1"].URL != "http://users:8080" || len(refs) != 3 {
		t.Errorf("targets = %+v", refs)
	}
}
