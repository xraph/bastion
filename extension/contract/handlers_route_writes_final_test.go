package contract

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/forge/extensions/dashboard/contract"
)

func TestRoutesUpdate_PortChangeOnAMaskedURLKeepsThePassword(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	seedOrders(t, rm)

	targets := []targetInput{{URL: "http://u:xxxxx@orders:9090", Weight: 1}}
	if _, err := routesUpdateHandler(deps)(context.Background(), routesUpdateRequest{ID: "r1", Targets: &targets}, contract.Principal{}); err != nil {
		t.Fatal(err)
	}

	r, _ := rm.GetRoute("r1")
	if got := r.Targets[0].URL; got != "http://u:secret@orders:9090" {
		t.Errorf("stored URL = %q, want the real password on the new port", got)
	}
}

func TestRoutesUpdate_RetypedPlaintextURLKeepsMetadata(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	seedOrders(t, rm)

	targets := []targetInput{{URL: "http://u:secret@orders:8080", Weight: 1}}
	if _, err := routesUpdateHandler(deps)(context.Background(), routesUpdateRequest{ID: "r1", Targets: &targets}, contract.Principal{}); err != nil {
		t.Fatal(err)
	}

	r, _ := rm.GetRoute("r1")
	if r.Targets[0].Metadata["health_check_path"] != "/healthz" {
		t.Errorf("metadata = %v, want it kept for a retyped URL", r.Targets[0].Metadata)
	}
}

func TestRoutesCreate_MaskedURLIsBadRequest(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)

	_, err := routesCreateHandler(deps)(context.Background(), routesCreateRequest{
		Path: "/billing", Enabled: true,
		Targets: []targetInput{{URL: "http://u:xxxxx@billing:9000", Weight: 1}},
	}, contract.Principal{})
	if c, d := code(err); c != contract.CodeBadRequest || d["field"] != "targets" {
		t.Errorf("masked create: %v %v", c, d)
	}
}

func TestRoutesCreate_BlackholeRateLimitIsBadRequest(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)

	_, err := routesCreateHandler(deps)(context.Background(), routesCreateRequest{
		Path: "/billing", Enabled: true,
		Targets: []targetInput{{URL: "http://billing:9000", Weight: 1}},
	}, contract.Principal{})
	if err != nil {
		t.Fatal(err)
	}

	rl := json.RawMessage(`{"enabled":true,"requestsPerSec":5,"burst":0}`)
	deps2, rm, _, _ := newTestDeps(t)
	seedOrders(t, rm)

	_, err = routesUpdateHandler(deps2)(context.Background(), routesUpdateRequest{ID: "r1", RateLimit: rl}, contract.Principal{})
	if c, d := code(err); c != contract.CodeBadRequest || d["field"] != "rateLimit" {
		t.Errorf("zero burst update: %v %v", c, d)
	}
}
