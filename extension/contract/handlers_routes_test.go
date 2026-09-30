package contract

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
)

func TestRoutesList_Filter(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "manual-/users", Path: "/gw/users", Source: bastion.SourceManual, Protocol: bastion.ProtocolHTTP})
	addRoute(t, rm, &bastion.Route{ID: "farp-x", Path: "/x", Source: bastion.SourceFARP, Protocol: bastion.ProtocolGRPC})

	out, _ := routesListHandler(deps)(context.Background(), routesListRequest{Protocol: "grpc"}, contract.Principal{})
	if out.Total != 1 || out.Routes[0].ID != "farp-x" {
		t.Errorf("out = %+v", out)
	}
}

func TestRoutesDetail_ConfigRouteIDWithSlash(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "manual-/users", Path: "/gw/users", Source: bastion.SourceManual, Priority: 100,
		Headers: bastion.HeaderPolicy{Set: map[string]string{"Authorization": "Bearer x"}}})

	out, err := routesDetailHandler(deps)(context.Background(), routesDetailRequest{ID: " manual-/users "}, contract.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != "manual-/users" || out.Input == nil || out.Input.Path != "/users" {
		t.Errorf("detail = %+v", out)
	}
	if out.Headers.Set["Authorization"] != admin.Redacted {
		t.Errorf("header leaked: %+v", out.Headers)
	}
}

func TestRoutesDetail_Errors(t *testing.T) {
	deps, _, _, _ := newTestDeps(t)
	h := routesDetailHandler(deps)

	var ce *contract.Error
	if _, err := h(context.Background(), routesDetailRequest{}, contract.Principal{}); !errors.As(err, &ce) || ce.Code != contract.CodeBadRequest {
		t.Errorf("empty id err = %v, want BAD_REQUEST", err)
	}
	if _, err := h(context.Background(), routesDetailRequest{ID: "nope"}, contract.Principal{}); !errors.As(err, &ce) || ce.Code != contract.CodeNotFound {
		t.Errorf("unknown id err = %v, want NOT_FOUND", err)
	}
}

func TestUpstreamsList(t *testing.T) {
	deps, rm, _, _ := newTestDeps(t)
	addRoute(t, rm, &bastion.Route{ID: "a", Path: "/gw/a", Source: bastion.SourceManual,
		Targets: []*bastion.Target{{ID: "a/0", URL: "http://a:1", Healthy: true}}})

	out, _ := upstreamsListHandler(deps)(context.Background(), upstreamsListRequest{}, contract.Principal{})
	if out.Total != 1 || out.Upstreams[0].URL != "http://a:1" {
		t.Errorf("out = %+v", out)
	}
}
