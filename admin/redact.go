package admin

import (
	"maps"
	"net/url"
	"slices"
	"strings"

	bastion "github.com/xraph/bastion"
)

// Redacted replaces a sensitive header value. The header's name stays, so an
// operator can see that it is set.
const Redacted = "[redacted]"

// RedactedPassword is what url.URL.Redacted shows in place of a password.
const RedactedPassword = "xxxxx"

// SensitiveHeader reports whether a header's value may carry a credential.
func SensitiveHeader(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}

	return strings.Contains(n, "key") || strings.Contains(n, "token") || strings.Contains(n, "secret")
}

// RedactURL hides the password in a URL's userinfo. A URL without userinfo,
// or one that does not parse, comes back unchanged.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}

	return u.Redacted()
}

// redactTraffic returns a copy of p without values that may carry a
// credential: header rules keyed by a sensitive header, every cookie rule,
// and userinfo in the mirror target.
func redactTraffic(p *bastion.TrafficPolicy) *bastion.TrafficPolicy {
	if p == nil {
		return nil
	}

	out := *p
	out.MirrorTarget = RedactURL(p.MirrorTarget)
	out.Rules = slices.Clone(p.Rules)

	for i := range out.Rules {
		m := &out.Rules[i].Match

		switch {
		case m.Type == bastion.MatchCookie && m.Value != "":
			m.Value = Redacted
		case m.Type == bastion.MatchHeader && SensitiveHeader(m.Key) && m.Value != "":
			m.Value = Redacted
		}
	}

	return &out
}

// RedactHeaders returns a copy of p with sensitive values replaced.
func RedactHeaders(p bastion.HeaderPolicy) bastion.HeaderPolicy {
	return bastion.HeaderPolicy{
		Add:    redactMap(p.Add),
		Set:    redactMap(p.Set),
		Remove: slices.Clone(p.Remove),
	}
}

// RedactTransform returns a copy of t with sensitive values replaced.
func RedactTransform(t *bastion.TransformConfig) *bastion.TransformConfig {
	if t == nil {
		return nil
	}

	return &bastion.TransformConfig{
		RequestHeaders:  RedactHeaders(t.RequestHeaders),
		ResponseHeaders: RedactHeaders(t.ResponseHeaders),
	}
}

func redactMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}

	out := maps.Clone(m)
	for k := range out {
		if SensitiveHeader(k) {
			out[k] = Redacted
		}
	}

	return out
}

// sortedKeys returns m's keys in order, never nil.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}
