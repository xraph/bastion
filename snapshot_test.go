package bastion_test

import (
	"testing"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/routing"
)

// wiredGateway returns a gateway with the real stats collector and route
// manager injected, as extension/ does, but never started.
func wiredGateway(t *testing.T) *bastion.Gateway {
	t.Helper()

	gw, ok := bastion.New(bastion.WithEnabled(true)).(*bastion.Gateway)
	if !ok {
		t.Fatal("expected *bastion.Gateway")
	}

	gw.SetStatsRecorder(proxy.NewStatsCollector())
	gw.SetRouteRegistry(routing.NewManager())

	return gw
}

func TestGateway_SnapshotUptimeIsZeroBeforeStart(t *testing.T) {
	s := wiredGateway(t).Snapshot()
	if s.Uptime != 0 || !s.StartedAt.IsZero() {
		t.Errorf("before Start: uptime=%d startedAt=%v, want zero", s.Uptime, s.StartedAt)
	}
}
