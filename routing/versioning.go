package routing

import (
	"net/http"
	"strings"

	bastion "github.com/xraph/bastion"
)

// VersioningConfig configures API version routing.
type VersioningConfig struct {
	// Enabled activates version-aware routing.
	Enabled bool `json:"enabled"`

	// Strategy is "path" or "header".
	Strategy string `json:"strategy"`

	// HeaderName is used when Strategy == "header" (e.g. "Accept-Version").
	HeaderName string `json:"headerName,omitempty"`

	// DefaultVersion is the fallback version when none is specified.
	DefaultVersion string `json:"defaultVersion,omitempty"`

	// Mappings maps version labels to target tags or upstream groups.
	Mappings []VersionMapping `json:"mappings,omitempty"`
}

// VersionMapping maps a version label to routing targets.
type VersionMapping struct {
	Version    string   `json:"version"`
	TargetTags []string `json:"targetTags,omitempty"`
	PathPrefix string   `json:"pathPrefix,omitempty"`
}

// VersionRouter resolves the requested API version and selects
// appropriate targets from a route.
type VersionRouter struct {
	config VersioningConfig
	index  map[string]*VersionMapping
}

// NewVersionRouter creates a new version router.
func NewVersionRouter(cfg VersioningConfig) *VersionRouter {
	vr := &VersionRouter{
		config: cfg,
		index:  make(map[string]*VersionMapping),
	}

	for i := range cfg.Mappings {
		vr.index[cfg.Mappings[i].Version] = &cfg.Mappings[i]
	}

	return vr
}

// Resolve extracts the version from the request and returns the matching
// VersionMapping. Returns nil if versioning is disabled or no match.
func (vr *VersionRouter) Resolve(r *http.Request) *VersionMapping {
	if !vr.config.Enabled {
		return nil
	}

	version := vr.extractVersion(r)
	if version == "" {
		version = vr.config.DefaultVersion
	}

	if version == "" {
		return nil
	}

	return vr.index[version]
}

// FilterTargets returns the subset of route targets that match the
// version mapping's target tags. If no mapping is found, all targets
// are returned.
func (vr *VersionRouter) FilterTargets(r *http.Request, targets []*bastion.Target) []*bastion.Target {
	mapping := vr.Resolve(r)
	if mapping == nil || len(mapping.TargetTags) == 0 {
		return targets
	}

	tagSet := make(map[string]bool, len(mapping.TargetTags))
	for _, t := range mapping.TargetTags {
		tagSet[t] = true
	}

	var filtered []*bastion.Target
	for _, t := range targets {
		for _, tag := range t.Tags {
			if tagSet[tag] {
				filtered = append(filtered, t)

				break
			}
		}
	}

	if len(filtered) == 0 {
		return targets // fallback to all targets
	}

	return filtered
}

func (vr *VersionRouter) extractVersion(r *http.Request) string {
	switch vr.config.Strategy {
	case "header":
		return r.Header.Get(vr.config.HeaderName)
	case "path":
		return extractPathVersion(r.URL.Path)
	default:
		return ""
	}
}

// extractPathVersion extracts a version segment from a URL path.
// Looks for patterns like /v1/, /v2/, /v1.0/ at the start of the path.
func extractPathVersion(path string) string {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) == 0 {
		return ""
	}

	seg := parts[0]
	if strings.HasPrefix(seg, "v") && len(seg) > 1 {
		return seg
	}

	return ""
}
