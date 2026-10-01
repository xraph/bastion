package admin_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
	"github.com/xraph/bastion/routing"
)

func ordersDTO() bastion.RouteDTO {
	return bastion.RouteDTO{
		Path:     "/orders",
		Methods:  []string{"GET"},
		Priority: 5,
		Enabled:  true,
		Targets:  []bastion.TargetDTO{{URL: "http://orders:8080", Weight: 2}},
	}
}

func TestCreateAppliesOffsetsOnce(t *testing.T) {
	f := newFixture(t)
	r, err := f.svc.CreateRoute(ordersDTO())
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "/gw/orders" || r.Priority != 105 || r.Source != bastion.SourceManual {
		t.Errorf("route = %+v", r)
	}
	if r.Targets[0].ID != r.ID+"/0" {
		t.Errorf("target id = %q, want %q", r.Targets[0].ID, r.ID+"/0")
	}
}

func TestLoadAndSaveUnchangedIsANoOp(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.CreateRoute(ordersDTO())
	before := r.Targets[0]

	d, _ := f.svc.GetRoute(r.ID)
	// What an editor sends back: the input values, not the effective ones.
	dto := ordersDTO()
	dto.Path, dto.Priority = d.Input.Path, d.Input.Priority

	if _, err := f.svc.UpdateRoute(r.ID, dto); err != nil {
		t.Fatal(err)
	}
	after, _ := f.rm.GetRoute(r.ID)
	if after.Path != "/gw/orders" || after.Priority != 105 {
		t.Errorf("after save: path %q priority %d, want /gw/orders 105", after.Path, after.Priority)
	}
	if after.Targets[0] != before {
		t.Error("an unchanged target was replaced; its counters would restart")
	}
}

func TestUpdateChangedWeightKeepsTargetID(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.CreateRoute(ordersDTO())
	dto := ordersDTO()
	dto.Targets[0].Weight = 9

	up, err := f.svc.UpdateRoute(r.ID, dto)
	if err != nil {
		t.Fatal(err)
	}
	if up.Targets[0].ID != r.Targets[0].ID || up.Targets[0].Weight != 9 {
		t.Errorf("target = %+v", up.Targets[0])
	}
}

func TestUpdateDropsBreakerOfRemovedTarget(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.CreateRoute(ordersDTO())
	f.cbm.Get(r.Targets[0].ID).RecordFailure()

	dto := ordersDTO()
	dto.Targets = []bastion.TargetDTO{{URL: "http://orders-v2:8080"}}
	if _, err := f.svc.UpdateRoute(r.ID, dto); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.cbm.Snapshots() {
		if s.TargetID == r.Targets[0].ID {
			t.Error("breaker of the removed target survived")
		}
	}
}

func TestDeleteKeepsBreakerOfTargetAnotherRouteUses(t *testing.T) {
	f := newFixture(t)
	// A legacy REST route and a config route sharing one target id.
	f.add(t, &bastion.Route{ID: "a", Path: "/gw/a", Source: bastion.SourceManual,
		Targets: []*bastion.Target{{ID: "target-http-orders:8080", URL: "http://orders:8080"}}})
	f.add(t, &bastion.Route{ID: "b", Path: "/gw/b", Source: bastion.SourceManual,
		Targets: []*bastion.Target{{ID: "target-http-orders:8080", URL: "http://orders:8080"}}})
	f.cbm.Get("target-http-orders:8080")

	if err := f.svc.DeleteRoute("a"); err != nil {
		t.Fatal(err)
	}
	if len(f.cbm.Snapshots()) != 1 {
		t.Error("deleting route a removed the breaker route b still uses")
	}
}

func TestWritesRefuseDiscoveredRoutes(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{ID: "farp-x", Path: "/x", Source: bastion.SourceFARP})

	var se *admin.SourceError
	if _, err := f.svc.UpdateRoute("farp-x", ordersDTO()); !errors.As(err, &se) || se.Source != bastion.SourceFARP {
		t.Errorf("update err = %v", err)
	}
	if err := f.svc.DeleteRoute("farp-x"); !errors.Is(err, admin.ErrNotManual) {
		t.Errorf("delete err = %v", err)
	}
	if _, err := f.svc.SetEnabled("farp-x", false); !errors.Is(err, admin.ErrNotManual) {
		t.Errorf("setEnabled err = %v", err)
	}
}

func TestConflictOnlyAgainstManualRoutes(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{ID: "farp-o", Path: "/gw/orders", Source: bastion.SourceFARP})
	if _, err := f.svc.CreateRoute(ordersDTO()); err != nil {
		t.Fatalf("a manual route may shadow a FARP route: %v", err)
	}

	dup := ordersDTO()
	dup.Methods = nil // any method overlaps GET
	if _, err := f.svc.CreateRoute(dup); !errors.Is(err, admin.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}

	post := ordersDTO()
	post.Methods = []string{"POST"}
	if _, err := f.svc.CreateRoute(post); err != nil {
		t.Errorf("disjoint methods must not conflict: %v", err)
	}
}

func TestValidation(t *testing.T) {
	f := newFixture(t)
	for name, mutate := range map[string]func(*bastion.RouteDTO){
		"path":      func(d *bastion.RouteDTO) { d.Path = "orders" },
		"targets":   func(d *bastion.RouteDTO) { d.Targets = nil },
		"scheme":    func(d *bastion.RouteDTO) { d.Targets[0].URL = "ftp://x" },
		"duplicate": func(d *bastion.RouteDTO) { d.Targets = append(d.Targets, d.Targets[0]) },
		"weight":    func(d *bastion.RouteDTO) { d.Targets[0].Weight = -1 },
		"protocol":  func(d *bastion.RouteDTO) { d.Protocol = "smtp" },
		"method":    func(d *bastion.RouteDTO) { d.Methods = []string{"FETCH"} },
	} {
		dto := ordersDTO()
		mutate(&dto)
		var ve *admin.ValidationError
		if _, err := f.svc.CreateRoute(dto); !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
}

func TestSetEnabledDurability(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.CreateRoute(ordersDTO())
	f.add(t, &bastion.Route{ID: "manual-/users", Path: "/gw/users", Source: bastion.SourceManual})

	durable, err := f.svc.SetEnabled(r.ID, false)
	if err != nil || durable {
		t.Errorf("no store: durable=%v err=%v, want false nil", durable, err)
	}
	got, _ := f.rm.GetRoute(r.ID)
	if got.Enabled {
		t.Error("route still enabled")
	}
	if got == r {
		t.Error("SetEnabled wrote the live route pointer instead of a copy")
	}

	svc, _ := admin.New(admin.Deps{Routes: f.rm, BasePath: "/gw", Persisted: func() bool { return true }})
	if durable, _ := svc.SetEnabled(r.ID, true); !durable {
		t.Error("with a store an API route change is durable")
	}
	if durable, _ := svc.SetEnabled("manual-/users", false); durable {
		t.Error("a config-file route change is never durable")
	}
}

// slowAdd widens the gap between a conflict check and its write, so a missing
// lock shows up as more than one route on the path.
type slowAdd struct{ *routing.Manager }

func (s slowAdd) AddRoute(r *bastion.Route) error {
	time.Sleep(5 * time.Millisecond)

	return s.Manager.AddRoute(r)
}

func TestConcurrentCreatesOfOnePathMakeOneRoute(t *testing.T) {
	svc, err := admin.New(admin.Deps{Routes: slowAdd{routing.NewManager()}, BasePath: "/gw"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.CreateRoute(ordersDTO())
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else if !errors.Is(err, admin.ErrConflict) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d creates succeeded, want exactly 1", ok)
	}
}

func TestChangedTargetKeepsItsHealth(t *testing.T) {
	f := newFixture(t)
	r, _ := f.svc.CreateRoute(ordersDTO())
	r.Targets[0].SetHealthy(false)

	dto := ordersDTO()
	dto.Targets[0].Weight = 9
	up, err := f.svc.UpdateRoute(r.ID, dto)
	if err != nil {
		t.Fatal(err)
	}
	if up.Targets[0].Healthy {
		t.Error("a weight edit reported a known-unhealthy upstream healthy")
	}
}

func TestStoredRouteDoesNotAliasTheDTO(t *testing.T) {
	f := newFixture(t)
	dto := ordersDTO()
	dto.Headers = bastion.HeaderPolicy{Set: map[string]string{"X-Env": "prod"}}
	dto.Metadata = map[string]any{"owner": "a"}
	dto.RateLimit = &bastion.RateLimitConfig{Enabled: true, RequestsPerSec: 5, Burst: 10}

	r, err := f.svc.CreateRoute(dto)
	if err != nil {
		t.Fatal(err)
	}
	dto.Headers.Set["X-Env"] = "changed"
	dto.Metadata["owner"] = "b"
	dto.RateLimit.Burst = 99

	got, _ := f.rm.GetRoute(r.ID)
	if got.Headers.Set["X-Env"] != "prod" || got.Metadata["owner"] != "a" || got.RateLimit.Burst != 10 {
		t.Errorf("stored route changed with the caller's DTO: %+v %+v %+v", got.Headers, got.Metadata, got.RateLimit)
	}
}

func TestTargetTLSIsNotShared(t *testing.T) {
	f := newFixture(t)
	dto := ordersDTO()
	dto.Targets[0].TLS = &bastion.TargetTLSConfig{Enabled: true, ServerName: "orders"}

	r, err := f.svc.CreateRoute(dto)
	if err != nil {
		t.Fatal(err)
	}
	dto.Targets[0].TLS.ServerName = "changed"
	if got := r.Targets[0].TLS.ServerName; got != "orders" {
		t.Errorf("stored TLS followed the caller's DTO: %q", got)
	}

	entry, err := f.svc.Entry(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry.Targets[0].TLS.ServerName = "edited"
	if got := r.Targets[0].TLS.ServerName; got != "orders" {
		t.Errorf("live TLS followed Entry's copy: %q", got)
	}
}

func TestEntryRoundTripsEverything(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{
		ID: "r1", Path: "/gw/orders", Source: bastion.SourceManual, Priority: 105, Enabled: true,
		Methods:   []string{"GET"},
		Headers:   bastion.HeaderPolicy{Set: map[string]string{"Authorization": "Bearer real"}},
		Transform: &bastion.TransformConfig{RequestHeaders: bastion.HeaderPolicy{Add: map[string]string{"X-A": "1"}}},
		Timeout:   &bastion.TimeoutConfig{Read: 5},
		Metadata:  map[string]any{"owner": "a"},
		Targets: []*bastion.Target{{ID: "r1/0", URL: "http://u:secret@orders:8080", Weight: 2,
			Metadata: map[string]string{"health_check_path": "/healthz"}}},
	})
	before, _ := f.rm.GetRoute("r1")

	dto, err := f.svc.Entry("r1")
	if err != nil {
		t.Fatal(err)
	}
	if dto.Path != "/orders" || dto.Priority != 5 {
		t.Errorf("entry path/priority = %q/%d, want /orders/5", dto.Path, dto.Priority)
	}
	if dto.Headers.Set["Authorization"] != "Bearer real" {
		t.Error("Entry redacted a header; it must not")
	}
	if dto.Targets[0].URL != "http://u:secret@orders:8080" {
		t.Error("Entry masked a URL; it must not")
	}

	if _, err := f.svc.UpdateRoute("r1", dto); err != nil {
		t.Fatal(err)
	}
	after, _ := f.rm.GetRoute("r1")
	if after.Path != before.Path || after.Priority != before.Priority ||
		after.Headers.Set["Authorization"] != "Bearer real" ||
		after.Transform.RequestHeaders.Add["X-A"] != "1" ||
		after.Timeout.Read != 5 || after.Metadata["owner"] != "a" ||
		after.Targets[0] != before.Targets[0] {
		t.Errorf("Entry then UpdateRoute changed the route:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestEntryRefusesDiscoveredRoutes(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{ID: "farp-x", Path: "/x", Source: bastion.SourceFARP})
	if _, err := f.svc.Entry("farp-x"); !errors.Is(err, admin.ErrNotManual) {
		t.Errorf("err = %v, want ErrNotManual", err)
	}
	if _, err := f.svc.Entry("nope"); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
