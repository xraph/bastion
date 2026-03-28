package bastion

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ValidationError represents a configuration validation error.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidationErrors is a collection of validation errors.
type ValidationErrors []ValidationError

func (ve ValidationErrors) Error() string {
	if len(ve) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("configuration validation failed:\n")

	for i, e := range ve {
		if i > 0 {
			b.WriteByte('\n')
		}

		b.WriteString("  - ")
		b.WriteString(e.Error())
	}

	return b.String()
}

// ValidateConfig validates the gateway configuration and returns all errors found.
func ValidateConfig(c *Config) error {
	var errs ValidationErrors

	errs = append(errs, validateTimeouts(c.Timeouts)...)
	errs = append(errs, validateRetry(c.Retry)...)
	errs = append(errs, validateCircuitBreaker(c.CircuitBreaker)...)
	errs = append(errs, validateRateLimiting(c.RateLimiting)...)
	errs = append(errs, validateHealthCheck(c.HealthCheck)...)
	errs = append(errs, validateRoutes(c.Routes)...)
	errs = append(errs, validateCaching(c.Caching)...)
	errs = append(errs, validateDiscovery(c.Discovery)...)
	errs = append(errs, validateTLS(c.TLS)...)
	errs = append(errs, validateOpenAPI(c.OpenAPI)...)

	if len(errs) > 0 {
		return errs
	}

	return nil
}

func validateTimeouts(t TimeoutConfig) ValidationErrors {
	var errs ValidationErrors

	if t.Connect < 0 {
		errs = append(errs, ValidationError{Field: "timeouts.connect", Message: "must be non-negative"})
	}

	if t.Read < 0 {
		errs = append(errs, ValidationError{Field: "timeouts.read", Message: "must be non-negative"})
	}

	if t.Write < 0 {
		errs = append(errs, ValidationError{Field: "timeouts.write", Message: "must be non-negative"})
	}

	if t.Idle < 0 {
		errs = append(errs, ValidationError{Field: "timeouts.idle", Message: "must be non-negative"})
	}

	if t.Connect > 0 && t.Connect > t.Read {
		errs = append(errs, ValidationError{Field: "timeouts.connect", Message: "should not exceed read timeout"})
	}

	return errs
}

func validateRetry(r RetryConfig) ValidationErrors {
	var errs ValidationErrors

	if !r.Enabled {
		return errs
	}

	if r.MaxAttempts < 1 {
		errs = append(errs, ValidationError{Field: "retry.maxAttempts", Message: "must be at least 1"})
	}

	if r.MaxAttempts > 10 {
		errs = append(errs, ValidationError{Field: "retry.maxAttempts", Message: "should not exceed 10"})
	}

	if r.InitialDelay < 0 {
		errs = append(errs, ValidationError{Field: "retry.initialDelay", Message: "must be non-negative"})
	}

	if r.MaxDelay < r.InitialDelay {
		errs = append(errs, ValidationError{Field: "retry.maxDelay", Message: "must be >= initialDelay"})
	}

	if r.Multiplier <= 0 {
		errs = append(errs, ValidationError{Field: "retry.multiplier", Message: "must be positive"})
	}

	if r.BudgetPercent < 0 || r.BudgetPercent > 100 {
		errs = append(errs, ValidationError{Field: "retry.budgetPercent", Message: "must be between 0 and 100"})
	}

	validBackoff := map[BackoffStrategy]bool{BackoffExponential: true, BackoffLinear: true, BackoffFixed: true}
	if !validBackoff[r.Backoff] {
		errs = append(errs, ValidationError{Field: "retry.backoff", Message: fmt.Sprintf("invalid strategy %q; must be one of: exponential, linear, fixed", r.Backoff)})
	}

	return errs
}

func validateCircuitBreaker(cb CircuitBreakerConfig) ValidationErrors {
	var errs ValidationErrors

	if !cb.Enabled {
		return errs
	}

	if cb.FailureThreshold < 1 {
		errs = append(errs, ValidationError{Field: "circuitBreaker.failureThreshold", Message: "must be at least 1"})
	}

	if cb.FailureWindow < time.Second {
		errs = append(errs, ValidationError{Field: "circuitBreaker.failureWindow", Message: "must be at least 1s"})
	}

	if cb.ResetTimeout < time.Second {
		errs = append(errs, ValidationError{Field: "circuitBreaker.resetTimeout", Message: "must be at least 1s"})
	}

	if cb.HalfOpenMax < 1 {
		errs = append(errs, ValidationError{Field: "circuitBreaker.halfOpenMax", Message: "must be at least 1"})
	}

	return errs
}

func validateRateLimiting(rl RateLimitConfig) ValidationErrors {
	var errs ValidationErrors

	if !rl.Enabled {
		return errs
	}

	if rl.RequestsPerSec <= 0 {
		errs = append(errs, ValidationError{Field: "rateLimiting.requestsPerSec", Message: "must be positive"})
	}

	if rl.Burst < 1 {
		errs = append(errs, ValidationError{Field: "rateLimiting.burst", Message: "must be at least 1"})
	}

	return errs
}

func validateHealthCheck(hc HealthCheckConfig) ValidationErrors {
	var errs ValidationErrors

	if !hc.Enabled {
		return errs
	}

	if hc.Interval < time.Second {
		errs = append(errs, ValidationError{Field: "healthCheck.interval", Message: "must be at least 1s"})
	}

	if hc.Timeout < 100*time.Millisecond {
		errs = append(errs, ValidationError{Field: "healthCheck.timeout", Message: "must be at least 100ms"})
	}

	if hc.Timeout >= hc.Interval {
		errs = append(errs, ValidationError{Field: "healthCheck.timeout", Message: "must be less than interval"})
	}

	if hc.FailureThreshold < 1 {
		errs = append(errs, ValidationError{Field: "healthCheck.failureThreshold", Message: "must be at least 1"})
	}

	if hc.SuccessThreshold < 1 {
		errs = append(errs, ValidationError{Field: "healthCheck.successThreshold", Message: "must be at least 1"})
	}

	return errs
}

func validateRoutes(routes []RouteConfig) ValidationErrors {
	var errs ValidationErrors

	paths := make(map[string]bool)

	for i, r := range routes {
		prefix := fmt.Sprintf("routes[%d]", i)

		if r.Path == "" {
			errs = append(errs, ValidationError{Field: prefix + ".path", Message: "must not be empty"})
		}

		if paths[r.Path] {
			errs = append(errs, ValidationError{Field: prefix + ".path", Message: fmt.Sprintf("duplicate path %q", r.Path)})
		}

		paths[r.Path] = true

		if len(r.Targets) == 0 {
			errs = append(errs, ValidationError{Field: prefix + ".targets", Message: "must have at least one target"})
		}

		for j, t := range r.Targets {
			tPrefix := fmt.Sprintf("%s.targets[%d]", prefix, j)

			if t.URL == "" {
				errs = append(errs, ValidationError{Field: tPrefix + ".url", Message: "must not be empty"})
			} else if _, err := url.Parse(t.URL); err != nil {
				errs = append(errs, ValidationError{Field: tPrefix + ".url", Message: fmt.Sprintf("invalid URL: %v", err)})
			}

			if t.Weight < 0 {
				errs = append(errs, ValidationError{Field: tPrefix + ".weight", Message: "must be non-negative"})
			}
		}

		validProtocols := map[RouteProtocol]bool{
			ProtocolHTTP: true, ProtocolWebSocket: true, ProtocolSSE: true,
			ProtocolGRPC: true, ProtocolGraphQL: true, "": true,
		}

		if !validProtocols[r.Protocol] {
			errs = append(errs, ValidationError{Field: prefix + ".protocol", Message: fmt.Sprintf("invalid protocol %q", r.Protocol)})
		}
	}

	return errs
}

func validateCaching(c CachingConfig) ValidationErrors {
	var errs ValidationErrors

	if !c.Enabled {
		return errs
	}

	if c.DefaultTTL < 0 {
		errs = append(errs, ValidationError{Field: "caching.defaultTtl", Message: "must be non-negative"})
	}

	if c.MaxSize < 1 {
		errs = append(errs, ValidationError{Field: "caching.maxSize", Message: "must be at least 1"})
	}

	return errs
}

func validateDiscovery(d DiscoveryConfig) ValidationErrors {
	var errs ValidationErrors

	if !d.Enabled {
		return errs
	}

	if d.PollInterval < time.Second {
		errs = append(errs, ValidationError{Field: "discovery.pollInterval", Message: "must be at least 1s"})
	}

	return errs
}

func validateTLS(t TLSConfig) ValidationErrors {
	var errs ValidationErrors

	if !t.Enabled {
		return errs
	}

	validVersions := map[string]bool{"1.0": true, "1.1": true, "1.2": true, "1.3": true, "": true}
	if !validVersions[t.MinVersion] {
		errs = append(errs, ValidationError{Field: "tls.minVersion", Message: fmt.Sprintf("invalid version %q; must be one of: 1.0, 1.1, 1.2, 1.3", t.MinVersion)})
	}

	if t.ClientCertFile != "" && t.ClientKeyFile == "" {
		errs = append(errs, ValidationError{Field: "tls.clientKeyFile", Message: "required when clientCertFile is set"})
	}

	if t.ClientKeyFile != "" && t.ClientCertFile == "" {
		errs = append(errs, ValidationError{Field: "tls.clientCertFile", Message: "required when clientKeyFile is set"})
	}

	return errs
}

func validateOpenAPI(o OpenAPIConfig) ValidationErrors {
	var errs ValidationErrors

	if !o.Enabled {
		return errs
	}

	if o.RefreshInterval < time.Second {
		errs = append(errs, ValidationError{Field: "openapi.refreshInterval", Message: "must be at least 1s"})
	}

	errs = append(errs, validateExtensionFilters(o.ExtensionFilters)...)

	return errs
}

func validateExtensionFilters(filters []ExtensionPathFilter) ValidationErrors {
	var errs ValidationErrors

	for i, ef := range filters {
		prefix := fmt.Sprintf("openapi.extensionFilters[%d]", i)

		if ef.ServiceName == "" && len(ef.ServiceNames) == 0 {
			errs = append(errs, ValidationError{
				Field:   prefix + ".serviceName",
				Message: "must set at least one of serviceName or serviceNames",
			})
		}

		if len(ef.KnownExtensions) == 0 && len(ef.ExcludePrefixes) == 0 {
			errs = append(errs, ValidationError{
				Field:   prefix + ".knownExtensions",
				Message: "must list at least one known extension or exclude prefix",
			})
		}

		// Verify allowedExtensions are a subset of knownExtensions
		knownSet := make(map[string]bool, len(ef.KnownExtensions))
		for _, k := range ef.KnownExtensions {
			knownSet[strings.ToLower(k)] = true
		}

		for _, a := range ef.AllowedExtensions {
			if !knownSet[strings.ToLower(a)] {
				errs = append(errs, ValidationError{
					Field:   prefix + ".allowedExtensions",
					Message: fmt.Sprintf("extension %q is not in knownExtensions", a),
				})
			}
		}

		// Validate ExcludePrefixes
		for j, ep := range ef.ExcludePrefixes {
			if !strings.HasPrefix(ep, "/") {
				errs = append(errs, ValidationError{
					Field:   fmt.Sprintf("%s.excludePrefixes[%d]", prefix, j),
					Message: fmt.Sprintf("must start with \"/\", got %q", ep),
				})
			}
		}

		// Validate IncludePrefixes
		for j, ip := range ef.IncludePrefixes {
			if !strings.HasPrefix(ip, "/") {
				errs = append(errs, ValidationError{
					Field:   fmt.Sprintf("%s.includePrefixes[%d]", prefix, j),
					Message: fmt.Sprintf("must start with \"/\", got %q", ip),
				})
			}
		}
	}

	return errs
}
