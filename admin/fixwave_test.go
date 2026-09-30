package admin_test

import (
	"testing"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
)

func TestGetRouteRedactsSensitiveTrafficMatchValues(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{
		ID: "r1", Path: "/gw/a", Source: bastion.SourceManual, Enabled: true,
		TrafficPolicy: &bastion.TrafficPolicy{
			Type:         bastion.TrafficMirror,
			MirrorTarget: "http://user:pw@mirror:9000",
			Rules: []bastion.TrafficRule{
				{Match: bastion.TrafficMatch{Type: bastion.MatchHeader, Key: "X-Api-Key", Value: "s3cret"}},
				{Match: bastion.TrafficMatch{Type: bastion.MatchHeader, Key: "X-Canary", Value: "yes"}},
				{Match: bastion.TrafficMatch{Type: bastion.MatchCookie, Key: "sid", Value: "abc"}},
			},
		},
	})

	d, err := f.svc.GetRoute("r1")
	if err != nil {
		t.Fatal(err)
	}

	rules := d.TrafficPolicy.Rules
	if rules[0].Match.Value != admin.Redacted {
		t.Errorf("sensitive header rule value = %q, want redacted", rules[0].Match.Value)
	}
	if rules[1].Match.Value != "yes" {
		t.Errorf("plain header rule value = %q, want kept", rules[1].Match.Value)
	}
	if rules[2].Match.Value != admin.Redacted {
		t.Errorf("cookie rule value = %q, want redacted", rules[2].Match.Value)
	}
	if d.TrafficPolicy.MirrorTarget != "http://user:xxxxx@mirror:9000" {
		t.Errorf("mirror target = %q, want credentials redacted", d.TrafficPolicy.MirrorTarget)
	}

	orig, _ := f.rm.GetRoute("r1")
	if orig.TrafficPolicy.Rules[0].Match.Value != "s3cret" || orig.TrafficPolicy.MirrorTarget != "http://user:pw@mirror:9000" {
		t.Error("redaction mutated the live route")
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"http://user:pw@host:80/p": "http://user:xxxxx@host:80/p",
		"http://host:80/p":         "http://host:80/p",
		"://bad url":               "://bad url",
		"":                         "",
	}
	for in, want := range cases {
		if got := admin.RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestViewsRedactURLCredentialsButGroupOnRawURL(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{ID: "r1", Path: "/gw/a", Source: bastion.SourceManual, Enabled: true,
		Targets: []*bastion.Target{{ID: "a", URL: "http://u:pw@h:1"}}})
	f.add(t, &bastion.Route{ID: "r2", Path: "/gw/b", Source: bastion.SourceManual, Enabled: true,
		Targets: []*bastion.Target{{ID: "b", URL: "http://u:pw@h:1"}}})

	d, _ := f.svc.GetRoute("r1")
	if d.Targets[0].URL != "http://u:xxxxx@h:1" {
		t.Errorf("target url = %q", d.Targets[0].URL)
	}

	ups := f.svc.Upstreams()
	if len(ups) != 1 || len(ups[0].Routes) != 2 || ups[0].URL != "http://u:xxxxx@h:1" {
		t.Errorf("upstreams = %+v, want one grouped upstream with redacted url", ups)
	}

	if got := f.svc.Targets()["a"].URL; got != "http://u:xxxxx@h:1" {
		t.Errorf("target ref url = %q", got)
	}
}

func TestUpdateKeepsServiceNameOfConfigRoute(t *testing.T) {
	f := newFixture(t)
	f.add(t, &bastion.Route{
		ID: "manual-/orders", Path: "/gw/orders", Source: bastion.SourceManual, Priority: 105, Enabled: true,
		ServiceName: "orders-svc", Targets: []*bastion.Target{{ID: "manual-/orders/0", URL: "http://orders:8080", Weight: 1}},
	})

	d, _ := f.svc.GetRoute("manual-/orders")
	dto := ordersDTO()
	dto.Path, dto.Priority = d.Input.Path, d.Input.Priority
	dto.Targets[0].Weight = 1

	if _, err := f.svc.UpdateRoute("manual-/orders", dto); err != nil {
		t.Fatal(err)
	}

	after, _ := f.rm.GetRoute("manual-/orders")
	if after.ServiceName != "orders-svc" {
		t.Errorf("service name = %q, want orders-svc", after.ServiceName)
	}
}
