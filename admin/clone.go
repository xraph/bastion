package admin

import (
	"maps"
	"slices"

	bastion "github.com/xraph/bastion"
)

// The service stores copies of everything it is handed, so a caller that
// reuses its DTO cannot change a live route behind the proxy's back.

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}

	v := *p

	return &v
}

func cloneHeaders(p bastion.HeaderPolicy) bastion.HeaderPolicy {
	return bastion.HeaderPolicy{Add: maps.Clone(p.Add), Set: maps.Clone(p.Set), Remove: slices.Clone(p.Remove)}
}

func cloneTransform(t *bastion.TransformConfig) *bastion.TransformConfig {
	if t == nil {
		return nil
	}

	return &bastion.TransformConfig{
		RequestHeaders:  cloneHeaders(t.RequestHeaders),
		ResponseHeaders: cloneHeaders(t.ResponseHeaders),
	}
}

func cloneTraffic(p *bastion.TrafficPolicy) *bastion.TrafficPolicy {
	if p == nil {
		return nil
	}

	cp := *p
	cp.Rules = slices.Clone(p.Rules)

	for i := range cp.Rules {
		cp.Rules[i].TargetTags = slices.Clone(cp.Rules[i].TargetTags)
	}

	return &cp
}

func cloneRateLimit(p *bastion.RateLimitConfig) *bastion.RateLimitConfig { return clonePtr(p) }

func cloneAuth(p *bastion.RouteAuthConfig) *bastion.RouteAuthConfig {
	if p == nil {
		return nil
	}

	cp := *p
	cp.Providers = slices.Clone(p.Providers)
	cp.Scopes = slices.Clone(p.Scopes)

	return &cp
}

func cloneRetry(p *bastion.RetryConfig) *bastion.RetryConfig {
	if p == nil {
		return nil
	}

	cp := *p
	cp.RetryableStatus = slices.Clone(p.RetryableStatus)
	cp.RetryableMethods = slices.Clone(p.RetryableMethods)

	return &cp
}

func cloneCache(p *bastion.RouteCacheConfig) *bastion.RouteCacheConfig {
	if p == nil {
		return nil
	}

	cp := *p
	cp.Methods = slices.Clone(p.Methods)
	cp.VaryBy = slices.Clone(p.VaryBy)

	return &cp
}
