package admin

import (
	"maps"
	"slices"
	"strings"

	bastion "github.com/xraph/bastion"
)

// Redacted replaces a sensitive header value. The header's name stays, so an
// operator can see that it is set.
const Redacted = "[redacted]"

// SensitiveHeader reports whether a header's value may carry a credential.
func SensitiveHeader(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		return true
	}

	return strings.Contains(n, "key") || strings.Contains(n, "token") || strings.Contains(n, "secret")
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
