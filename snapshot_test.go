package bastion_test

import (
	"errors"
	"testing"
	"time"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/proxy"
	"github.com/xraph/bastion/resilience"
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

func TestGateway_ResetCircuitUnknownTarget(t *testing.T) {
	gw, _ := bastion.New(bastion.WithEnabled(true)).(*bastion.Gateway)
	if err := gw.ResetCircuit("nope"); !errors.Is(err, bastion.ErrCircuitNotFound) {
		t.Errorf("err = %v, want ErrCircuitNotFound", err)
	}
	if got := gw.Circuits(); got != nil {
		t.Errorf("circuits = %v, want nil with no circuit control", got)
	}
}

func TestGateway_CircuitsAndReset(t *testing.T) {
	gw := wiredGateway(t)
	cbm := resilience.NewCBManager(bastion.CircuitBreakerConfig{Enabled: true, FailureThreshold: 1, ResetTimeout: time.Hour})
	gw.SetCircuitControl(cbm)
	cbm.Get("t").RecordFailure()

	if c := gw.Circuits(); len(c) != 1 || c[0].State != bastion.CircuitOpen {
		t.Fatalf("circuits = %+v, want one open", c)
	}
	if err := gw.ResetCircuit("t"); err != nil {
		t.Fatal(err)
	}
	if c := gw.Circuits(); c[0].State != bastion.CircuitClosed {
		t.Errorf("after reset = %+v, want closed", c)
	}
}
