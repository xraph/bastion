package security

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// WAFConfig configures the Web Application Firewall.
type WAFConfig struct {
	Enabled            bool      `json:"enabled"`
	MaxBodySize        int64     `json:"maxBodySize"` // bytes, 0 = no limit
	BlockSQLi          bool      `json:"blockSqli"`
	BlockXSS           bool      `json:"blockXss"`
	BlockPathTraversal bool      `json:"blockPathTraversal"`
	CustomRules        []WAFRule `json:"customRules,omitempty"`
}

// WAFRule defines a custom WAF detection rule.
type WAFRule struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"` // regex pattern
	Target  string `json:"target"`  // "path", "query", "header:<name>", "body"
	Action  string `json:"action"`  // "block" or "log"
}

// WAFViolation describes a blocked request.
type WAFViolation struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// WAF is a Web Application Firewall that blocks malicious requests.
type WAF struct {
	config WAFConfig
	mu     sync.RWMutex
	sqli   []*regexp.Regexp
	xss    []*regexp.Regexp
	path   []*regexp.Regexp
	custom map[string]*compiledRule
}

type compiledRule struct {
	rule    WAFRule
	pattern *regexp.Regexp
}

// NewWAF creates a WAF with the given configuration.
func NewWAF(cfg WAFConfig) *WAF {
	w := &WAF{
		config: cfg,
		custom: make(map[string]*compiledRule),
	}

	if cfg.BlockSQLi {
		w.sqli = compilePatterns(sqliPatterns)
	}

	if cfg.BlockXSS {
		w.xss = compilePatterns(xssPatterns)
	}

	if cfg.BlockPathTraversal {
		w.path = compilePatterns(pathTraversalPatterns)
	}

	for _, rule := range cfg.CustomRules {
		if re, err := regexp.Compile(rule.Pattern); err == nil {
			w.custom[rule.Name] = &compiledRule{rule: rule, pattern: re}
		}
	}

	return w
}

// Check inspects an HTTP request and returns a violation if the request
// is malicious, or nil if it passes.
func (w *WAF) Check(r *http.Request) *WAFViolation {
	if !w.config.Enabled {
		return nil
	}

	// Body size check
	if w.config.MaxBodySize > 0 && r.ContentLength > w.config.MaxBodySize {
		return &WAFViolation{
			Rule:    "body-size",
			Message: fmt.Sprintf("request body size %d exceeds limit %d", r.ContentLength, w.config.MaxBodySize),
		}
	}

	path := r.URL.Path
	query := r.URL.RawQuery
	decodedQuery, _ := url.QueryUnescape(query)

	// Path traversal
	if w.config.BlockPathTraversal {
		if v := w.matchPatterns(w.path, path, "path-traversal"); v != nil {
			return v
		}
	}

	// SQL injection (check path + decoded query)
	if w.config.BlockSQLi {
		combined := path + "?" + decodedQuery
		if v := w.matchPatterns(w.sqli, combined, "sqli"); v != nil {
			return v
		}
	}

	// XSS (check decoded query string)
	if w.config.BlockXSS {
		if v := w.matchPatterns(w.xss, decodedQuery, "xss"); v != nil {
			return v
		}
	}

	// Custom rules
	w.mu.RLock()
	defer w.mu.RUnlock()

	for _, cr := range w.custom {
		target := w.extractTarget(r, cr.rule.Target)
		if target != "" && cr.pattern.MatchString(target) {
			return &WAFViolation{
				Rule:    cr.rule.Name,
				Message: fmt.Sprintf("custom rule %q matched", cr.rule.Name),
			}
		}
	}

	return nil
}

func (w *WAF) matchPatterns(patterns []*regexp.Regexp, input, ruleName string) *WAFViolation {
	decoded := strings.ReplaceAll(input, "%27", "'")
	decoded = strings.ReplaceAll(decoded, "%3C", "<")
	decoded = strings.ReplaceAll(decoded, "%3E", ">")
	decoded = strings.ReplaceAll(decoded, "%22", "\"")

	for _, re := range patterns {
		if re.MatchString(input) || re.MatchString(decoded) {
			return &WAFViolation{
				Rule:    ruleName,
				Message: fmt.Sprintf("%s pattern detected", ruleName),
			}
		}
	}

	return nil
}

func (w *WAF) extractTarget(r *http.Request, target string) string {
	switch {
	case target == "path":
		return r.URL.Path
	case target == "query":
		return r.URL.RawQuery
	case target == "body":
		return "" // body checking requires buffering; skip for now
	case strings.HasPrefix(target, "header:"):
		headerName := strings.TrimPrefix(target, "header:")
		return r.Header.Get(headerName)
	default:
		return ""
	}
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	var compiled []*regexp.Regexp
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			compiled = append(compiled, re)
		}
	}

	return compiled
}

// Common attack patterns (case-insensitive where needed).
var sqliPatterns = []string{
	`(?i)(\b(union\s+(all\s+)?select|select\s+.*\s+from|insert\s+into|update\s+.*\s+set|delete\s+from|drop\s+(table|database))\b)`,
	`(?i)(\b(or|and)\s+[\d']+=[\d']+)`,
	`(?i)(;\s*(drop|alter|create|truncate|exec)\s)`,
	`(?i)('(\s)*(or|and)(\s)*'?\d)`,
	`(?i)(\/\*.*?\*\/)`,
	`(?i)(--\s)`,
}

var xssPatterns = []string{
	`(?i)(<script[^>]*>)`,
	`(?i)(javascript\s*:)`,
	`(?i)(on(error|load|click|mouseover|focus|blur|submit|change)\s*=)`,
	`(?i)(<\s*(iframe|object|embed|form|img)\b[^>]*>)`,
	`(?i)(document\.(cookie|domain|write))`,
	`(?i)(eval\s*\()`,
}

var pathTraversalPatterns = []string{
	`(?i)(\.\.[\\/])`,
	`(?i)(%2e%2e[\\/])`,
	`(?i)(\.\.%2f)`,
	`(?i)(%2e%2e%2f)`,
	`(?i)(/etc/(passwd|shadow|hosts))`,
	`(?i)(/proc/self/)`,
}

// WAFPlugin wraps the WAF as a gateway plugin.
type WAFPlugin struct {
	BasePlugin
	waf *WAF
}

// NewWAFPlugin creates a WAF plugin.
func NewWAFPlugin(cfg WAFConfig) *WAFPlugin {
	return &WAFPlugin{
		BasePlugin: BasePlugin{PluginName: "waf"},
		waf:        NewWAF(cfg),
	}
}

// CheckRequest checks the request against WAF rules.
// Returns an error if the request is blocked.
func (p *WAFPlugin) CheckRequest(r *http.Request) error {
	v := p.waf.Check(r)
	if v != nil {
		return fmt.Errorf("waf: %s — %s", v.Rule, v.Message)
	}

	return nil
}
