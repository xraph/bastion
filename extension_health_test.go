package bastion_test

import (
	"testing"
	"time"

	bastion "github.com/xraph/bastion"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

func TestGateway_RemovingRouteKeepsHealthEntrySharedWithAnotherRoute(t *testing.T) {
	gw := wiredGateway(t)
	gw.WireCallbacks()

	rm := gw.RouteManager()
	mon := gw.HealthMonitor()

	shared := func() *bastion.Target { return &bastion.Target{ID: "shared", URL: "http://orders:8080"} }
	only := &bastion.Target{ID: "only-a", URL: "http://a:8080"}

	a := &bastion.Route{ID: "a", Path: "/a", Targets: []*bastion.Target{shared(), only}}
	b := &bastion.Route{ID: "b", Path: "/b", Targets: []*bastion.Target{shared()}}

	for _, r := range []*bastion.Route{a, b} {
		if err := rm.AddRoute(r); err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, "targets registered", func() bool { return mon.Tracks("shared") && mon.Tracks("only-a") })

	if err := rm.RemoveRoute("a"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "unshared target deregistered", func() bool { return !mon.Tracks("only-a") })

	if !mon.Tracks("shared") {
		t.Error("shared target still used by route b was deregistered")
	}
}
