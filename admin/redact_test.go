package admin_test

import (
	"testing"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
)

func TestSensitiveHeader(t *testing.T) {
	for name, want := range map[string]bool{
		"Authorization":       true,
		"proxy-authorization": true,
		"Cookie":              true,
		"Set-Cookie":          true,
		"X-Api-Key":           true,
		"X-Auth-Token":        true,
		"X-Client-Secret":     true,
		"X-Request-Id":        false,
		"Accept":              false,
	} {
		if got := admin.SensitiveHeader(name); got != want {
			t.Errorf("SensitiveHeader(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRedactHeadersKeepsNamesAndCopies(t *testing.T) {
	in := bastion.HeaderPolicy{
		Set:    map[string]string{"X-Api-Key": "k-123", "X-Env": "prod"},
		Add:    map[string]string{"Authorization": "Bearer abc"},
		Remove: []string{"X-Debug"},
	}
	out := admin.RedactHeaders(in)

	if out.Set["X-Api-Key"] != admin.Redacted || out.Add["Authorization"] != admin.Redacted {
		t.Errorf("sensitive values leaked: %+v", out)
	}
	if out.Set["X-Env"] != "prod" || len(out.Remove) != 1 {
		t.Errorf("plain values lost: %+v", out)
	}
	if in.Set["X-Api-Key"] != "k-123" {
		t.Error("RedactHeaders modified its input")
	}
}

func TestRedactTransformNilAndNested(t *testing.T) {
	if admin.RedactTransform(nil) != nil {
		t.Error("nil transform must stay nil")
	}
	out := admin.RedactTransform(&bastion.TransformConfig{
		RequestHeaders: bastion.HeaderPolicy{Set: map[string]string{"X-Api-Key": "k"}},
	})
	if out.RequestHeaders.Set["X-Api-Key"] != admin.Redacted {
		t.Errorf("request header leaked: %+v", out)
	}
}

func TestNewRequiresRoutes(t *testing.T) {
	if _, err := admin.New(admin.Deps{}); err == nil {
		t.Error("New without Routes returned no error")
	}
}
