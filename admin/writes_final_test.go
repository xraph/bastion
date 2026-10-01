package admin_test

import (
	"errors"
	"testing"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
)

func wantField(t *testing.T, err error, field string) {
	t.Helper()

	var ve *admin.ValidationError
	if !errors.As(err, &ve) || ve.Field != field {
		t.Fatalf("err = %v, want ValidationError on %q", err, field)
	}
}

func TestValidate_RefusesAMaskedPassword(t *testing.T) {
	f := newFixture(t)

	dto := ordersDTO()
	dto.Targets = []bastion.TargetDTO{{URL: "http://u:xxxxx@orders:8080", Weight: 1}}

	_, err := f.svc.CreateRoute(dto)
	wantField(t, err, "targets")

	good := ordersDTO()
	r, err := f.svc.CreateRoute(good)
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.svc.UpdateRoute(r.ID, dto)
	wantField(t, err, "targets")
}

func TestValidate_RefusesARateLimitThatBlackholes(t *testing.T) {
	f := newFixture(t)

	for name, rl := range map[string]*bastion.RateLimitConfig{
		"zero rate":  {Enabled: true, RequestsPerSec: 0, Burst: 5},
		"zero burst": {Enabled: true, RequestsPerSec: 5, Burst: 0},
		"negative":   {Enabled: true, RequestsPerSec: -1, Burst: 5},
	} {
		dto := ordersDTO()
		dto.RateLimit = rl

		_, err := f.svc.CreateRoute(dto)
		if err == nil {
			t.Errorf("%s: create accepted a rate limit that never allows", name)
			continue
		}

		wantField(t, err, "rateLimit")
	}

	// A disabled limit is not checked, and a sane one passes.
	dto := ordersDTO()
	dto.RateLimit = &bastion.RateLimitConfig{Enabled: false}

	if _, err := f.svc.CreateRoute(dto); err != nil {
		t.Errorf("disabled rate limit refused: %v", err)
	}

	dto = ordersDTO()
	dto.Path = "/other"
	dto.RateLimit = &bastion.RateLimitConfig{Enabled: true, RequestsPerSec: 5, Burst: 1}

	if _, err := f.svc.CreateRoute(dto); err != nil {
		t.Errorf("sane rate limit refused: %v", err)
	}
}
