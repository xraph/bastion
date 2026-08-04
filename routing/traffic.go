package routing

import (
	"math/rand"
	"net/http"
	"regexp"
	"strings"

	bastion "github.com/xraph/bastion"
)

// TrafficSplitter evaluates traffic rules to determine target selection.
type TrafficSplitter struct {
	enabled bool
}

// NewTrafficSplitter creates a new traffic splitter.
func NewTrafficSplitter(enabled bool) *TrafficSplitter {
	return &TrafficSplitter{enabled: enabled}
}

// FilterTargets filters targets based on the route's traffic policy and request.
// Returns the filtered targets, or the original targets if no policy applies.
func (ts *TrafficSplitter) FilterTargets(r *http.Request, route *bastion.Route) []*bastion.Target {
	if !ts.enabled || route.TrafficPolicy == nil {
		return route.Targets
	}

	policy := route.TrafficPolicy

	switch policy.Type {
	case bastion.TrafficCanary:
		return ts.applyCanary(r, route.Targets, policy)
	case bastion.TrafficBlueGreen:
		return ts.applyBlueGreen(r, route.Targets, policy)
	case bastion.TrafficABTest:
		return ts.applyABTest(r, route.Targets, policy)
	case bastion.TrafficWeighted:
		return ts.applyWeighted(r, route.Targets, policy)
	default:
		return route.Targets
	}
}

// ShouldMirror returns the mirror target URL if the request should be mirrored.
func (ts *TrafficSplitter) ShouldMirror(route *bastion.Route) string {
	if !ts.enabled || route.TrafficPolicy == nil {
		return ""
	}

	if route.TrafficPolicy.Type == bastion.TrafficMirror {
		return route.TrafficPolicy.MirrorTarget
	}

	return ""
}

func (ts *TrafficSplitter) applyCanary(_ *http.Request, targets []*bastion.Target, policy *bastion.TrafficPolicy) []*bastion.Target {
	for _, rule := range policy.Rules {
		if rule.Match.Type == bastion.MatchWeight {
			// Random roll to decide canary vs stable
			// #nosec G404 -- canary percentage rollout. math/rand is the right tool here: the value is a statistical choice, not a secret, and nothing about it needs to be unpredictable to an attacker.
			if rand.Intn(100) < rule.Weight {
				return filterByTags(targets, rule.TargetTags)
			}
		}
	}

	// Default: return non-canary targets (targets without "canary" tag)
	stable := make([]*bastion.Target, 0, len(targets))

	for _, t := range targets {
		if !hasTag(t.Tags, "canary") {
			stable = append(stable, t)
		}
	}

	if len(stable) == 0 {
		return targets
	}

	return stable
}

func (ts *TrafficSplitter) applyBlueGreen(_ *http.Request, targets []*bastion.Target, policy *bastion.TrafficPolicy) []*bastion.Target {
	// Blue-green uses the first rule to determine which group is active
	if len(policy.Rules) > 0 {
		rule := policy.Rules[0]

		return filterByTags(targets, rule.TargetTags)
	}

	return targets
}

func (ts *TrafficSplitter) applyABTest(r *http.Request, targets []*bastion.Target, policy *bastion.TrafficPolicy) []*bastion.Target {
	for _, rule := range policy.Rules {
		if matchTrafficRule(r, rule.Match) {
			filtered := filterByTags(targets, rule.TargetTags)
			if len(filtered) > 0 {
				return filtered
			}
		}
	}

	return targets
}

func (ts *TrafficSplitter) applyWeighted(_ *http.Request, targets []*bastion.Target, policy *bastion.TrafficPolicy) []*bastion.Target {
	// Build weighted selection based on rules
	totalWeight := 0

	for _, rule := range policy.Rules {
		totalWeight += rule.Weight
	}

	if totalWeight == 0 {
		return targets
	}

	// #nosec G404 -- weighted route selection. math/rand is the right tool here: the value is a statistical choice, not a secret, and nothing about it needs to be unpredictable to an attacker.
	roll := rand.Intn(totalWeight)

	for _, rule := range policy.Rules {
		roll -= rule.Weight
		if roll < 0 {
			filtered := filterByTags(targets, rule.TargetTags)
			if len(filtered) > 0 {
				return filtered
			}
		}
	}

	return targets
}

func matchTrafficRule(r *http.Request, match bastion.TrafficMatch) bool {
	var matched bool

	switch match.Type {
	case bastion.MatchHeader:
		val := r.Header.Get(match.Key)
		matched = val == match.Value

	case bastion.MatchCookie:
		cookie, err := r.Cookie(match.Key)
		if err == nil {
			matched = cookie.Value == match.Value
		}

	case bastion.MatchWeight:
		// #nosec G404 -- weight-based traffic split. math/rand is the right tool here: the value is a statistical choice, not a secret, and nothing about it needs to be unpredictable to an attacker.
		matched = rand.Intn(100) < 50 // Generic weight-based

	case bastion.MatchIPRange:
		host, _, _ := splitHostPort(r.RemoteAddr)
		matched = matchIPPattern(host, match.Value)
	}

	if match.Negate {
		return !matched
	}

	return matched
}

func filterByTags(targets []*bastion.Target, tags []string) []*bastion.Target {
	if len(tags) == 0 {
		return targets
	}

	filtered := make([]*bastion.Target, 0, len(targets))

	for _, t := range targets {
		if hasAnyTag(t.Tags, tags) {
			filtered = append(filtered, t)
		}
	}

	return filtered
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}

	return false
}

func hasAnyTag(tags, required []string) bool {
	for _, req := range required {
		if hasTag(tags, req) {
			return true
		}
	}

	return false
}

// splitHostPort splits an address into host and port parts.
func splitHostPort(addr string) (string, string, error) {
	if strings.Contains(addr, ":") {
		parts := strings.SplitN(addr, ":", 2)

		return parts[0], parts[1], nil
	}

	return addr, "", nil
}

// matchIPPattern checks if an IP address matches a pattern.
func matchIPPattern(ip, pattern string) bool {
	// Exact match
	if ip == pattern {
		return true
	}

	// CIDR match
	if strings.Contains(pattern, "/") {
		// Simple prefix check for CIDR-like patterns
		prefix := strings.Split(pattern, "/")[0]

		return strings.HasPrefix(ip, prefix)
	}

	// Wildcard match
	if strings.Contains(pattern, "*") {
		pattern = strings.ReplaceAll(pattern, ".", "\\.")
		pattern = strings.ReplaceAll(pattern, "*", ".*")

		re, err := regexp.Compile("^" + pattern + "$")
		if err != nil {
			return false
		}

		return re.MatchString(ip)
	}

	return false
}
