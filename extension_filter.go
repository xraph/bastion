package bastion

import "strings"

// IsExtensionPathExcluded checks whether an upstream path should be excluded
// based on the given extension path filter.
//
// Filtering logic (in priority order):
//  1. If the path starts with any IncludePrefixes entry → include (highest priority).
//  2. If the path starts with any ExcludePrefixes entry → exclude.
//  3. If any path segment matches a KnownExtensions name that is NOT in
//     AllowedExtensions → exclude.
//  4. Otherwise → include (native service path).
//
// This handles both direct mounts (/{ext}/...) and nested mounts
// (/api/{ext}/v1/...) because it checks ALL path segments, not just the first.
func IsExtensionPathExcluded(upstreamPath string, filter *ExtensionPathFilter) bool {
	if filter == nil {
		return false
	}

	// Normalize path for prefix matching.
	normalized := upstreamPath
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}

	// 1. IncludePrefixes — highest priority whitelist.
	for _, prefix := range filter.IncludePrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return false // explicitly included
		}
	}

	// 2. ExcludePrefixes — explicit path exclusion.
	for _, prefix := range filter.ExcludePrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true // explicitly excluded
		}
	}

	// 3. Any-segment matching against KnownExtensions.
	if len(filter.KnownExtensions) == 0 {
		return false
	}

	segments := pathSegments(upstreamPath)
	for _, seg := range segments {
		// Check if this segment is a known extension.
		isKnown := false
		for _, ext := range filter.KnownExtensions {
			if strings.EqualFold(ext, seg) {
				isKnown = true
				break
			}
		}

		if !isKnown {
			continue
		}

		// Known extension — check if it's allowed.
		isAllowed := false
		for _, ext := range filter.AllowedExtensions {
			if strings.EqualFold(ext, seg) {
				isAllowed = true
				break
			}
		}

		if !isAllowed {
			return true // known extension, not allowed → exclude
		}
	}

	return false // no matching extension segment → native path, include
}

// pathSegments splits a URL path into its non-empty segments.
// For "/api/dispatch/v1/jobs" it returns ["api", "dispatch", "v1", "jobs"].
// For "/" or "" it returns nil.
func pathSegments(path string) []string {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return nil
	}

	parts := strings.Split(path, "/")
	segments := make([]string, 0, len(parts))

	for _, p := range parts {
		if p != "" {
			segments = append(segments, p)
		}
	}

	return segments
}
