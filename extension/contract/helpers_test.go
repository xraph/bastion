package contract

import (
	"testing"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
	"github.com/xraph/bastion/routing"
)

// newTestDeps wires a gateway the way extension/ does, without starting it.
func newTestDeps(t *testing.T) (Deps, *routing.Manager, *resilience.CBManager, *proxy.StatsCollector) {
	t.Helper()

	gw, ok := bastion.New(bastion.WithEnabled(true), bastion.WithBasePath("/gw")).(*bastion.Gateway)
	if !ok {
		t.Fatal("expected *bastion.Gateway")
	}

	rm := routing.NewManager()
	cbm := resilience.NewCBManager(gw.Config().CircuitBreaker)
	stats := proxy.NewStatsCollector()
	gw.SetRouteRegistry(rm)
	gw.SetCircuitControl(cbm)
	gw.SetStatsRecorder(stats)

	svc, err := admin.New(admin.Deps{Routes: rm, Breakers: cbm, BasePath: "/gw"})
	if err != nil {
		t.Fatal(err)
	}

	return Deps{Gateway: gw, Admin: svc}, rm, cbm, stats
}

func addRoute(t *testing.T, rm *routing.Manager, r *bastion.Route) {
	t.Helper()
	if err := rm.AddRoute(r); err != nil {
		t.Fatal(err)
	}
}
