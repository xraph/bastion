package contract

import (
	"context"
	"testing"

	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
)

func TestDiscoveryRefresh_OffIsAConflictWithAReason(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)
	_, err := discoveryRefreshHandler(deps)(context.Background(), discoveryRefreshRequest{}, contract.Principal{})
	if c, d := code(err); c != contract.CodeConflict || d["reason"] != "discoveryOff" {
		t.Errorf("err = %v %v, want CONFLICT discoveryOff", c, d)
	}
}

func TestOpenAPIRefresh_NotRunningIsAConflictWithAReason(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)
	_, err := openapiRefreshHandler(deps)(context.Background(), openapiRefreshRequest{}, contract.Principal{})
	if c, d := code(err); c != contract.CodeConflict || d["reason"] != "openapiOff" {
		t.Errorf("err = %v %v, want CONFLICT openapiOff", c, d)
	}
}

func TestCircuitsReset(t *testing.T) {
	deps, rm, cbm, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "a", Path: "/gw/a", Source: bastion.SourceManual,
		Targets: []*bastion.Target{{ID: "a/0", URL: "http://a:1"}}})
	for i := 0; i < deps.Gateway.Config().CircuitBreaker.FailureThreshold; i++ {
		cbm.Get("a/0").RecordFailure()
	}
	h := circuitsResetHandler(deps)

	out, err := h(context.Background(), circuitsResetRequest{TargetID: "a/0"}, contract.Principal{})
	if err != nil || out.State != bastion.CircuitClosed {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if s := cbm.Get("a/0").State(); s != bastion.CircuitClosed {
		t.Errorf("breaker = %s after reset", s)
	}

	if _, err := h(context.Background(), circuitsResetRequest{TargetID: "never-used"}, contract.Principal{}); func() bool { c, _ := code(err); return c != contract.CodeNotFound }() {
		t.Errorf("unknown target: %v, want NOT_FOUND", err)
	}
	if _, err := h(context.Background(), circuitsResetRequest{}, contract.Principal{}); func() bool { c, _ := code(err); return c != contract.CodeBadRequest }() {
		t.Errorf("missing targetId: %v, want BAD_REQUEST", err)
	}
}
