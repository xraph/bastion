package proxy

import (
	"testing"
	"time"

	"github.com/xraph/forge"

	bastion "github.com/xraph/bastion"
)

func TestRouteTimeouts_FallBackPerField(t *testing.T) {
	global := bastion.TimeoutConfig{Connect: 10 * time.Second, Read: 30 * time.Second, Write: 30 * time.Second, Idle: 90 * time.Second}

	if _, ok := routeTimeouts(&bastion.Route{}, global); ok {
		t.Error("route without a timeout block reported its own timeouts")
	}

	got, ok := routeTimeouts(&bastion.Route{Timeout: &bastion.TimeoutConfig{Read: 5 * time.Second}}, global)
	if !ok {
		t.Fatal("route with a timeout block reported none")
	}

	want := bastion.TimeoutConfig{Connect: 10 * time.Second, Read: 5 * time.Second, Idle: 90 * time.Second}
	if got != want {
		t.Errorf("routeTimeouts = %+v, want %+v (write only from the route)", got, want)
	}
}

func TestTransportFor_RouteTimeoutsGetTheirOwnPool(t *testing.T) {
	global := bastion.TimeoutConfig{Connect: 10 * time.Second, Read: 30 * time.Second, Idle: 90 * time.Second}
	pe := NewEngine(bastion.Config{Timeouts: global}, forge.NewNoopLogger(), nil, nil, nil, nil, nil, nil, nil)

	if pe.transportFor(&bastion.Route{}) != pe.transport {
		t.Error("route without timeouts did not get the shared transport")
	}

	if pe.transportFor(&bastion.Route{Timeout: &bastion.TimeoutConfig{Write: time.Second}}) != pe.transport {
		t.Error("a write-only timeout split the connection pool")
	}

	route := &bastion.Route{Timeout: &bastion.TimeoutConfig{Read: 5 * time.Second, Idle: 15 * time.Second}}

	tr := pe.transportFor(route)
	if tr == pe.transport {
		t.Fatal("route with its own read timeout got the shared transport")
	}
	if tr.ResponseHeaderTimeout != 5*time.Second || tr.IdleConnTimeout != 15*time.Second {
		t.Errorf("transport read %v idle %v, want 5s and 15s", tr.ResponseHeaderTimeout, tr.IdleConnTimeout)
	}
	if pe.transportFor(&bastion.Route{Timeout: &bastion.TimeoutConfig{Read: 5 * time.Second, Idle: 15 * time.Second}}) != tr {
		t.Error("routes with the same timeouts got different transports")
	}
}
