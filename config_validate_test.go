package bastion

import (
	"testing"
	"time"
)

func TestValidateConfig_DefaultsPass(t *testing.T) {
	c := DefaultConfig()
	if err := ValidateConfig(&c); err != nil {
		t.Errorf("default config should be valid, got: %v", err)
	}
}

func TestValidateConfig_InvalidTimeouts(t *testing.T) {
	c := DefaultConfig()
	c.Timeouts.Connect = -1

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for negative connect timeout")
	}

	errs, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}

	found := false
	for _, e := range errs {
		if e.Field == "timeouts.connect" {
			found = true
		}
	}

	if !found {
		t.Error("expected error for timeouts.connect field")
	}
}

func TestValidateConfig_InvalidRetry(t *testing.T) {
	c := DefaultConfig()
	c.Retry.MaxAttempts = 0

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for invalid retry config")
	}
}

func TestValidateConfig_InvalidCircuitBreaker(t *testing.T) {
	c := DefaultConfig()
	c.CircuitBreaker.FailureThreshold = 0

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for invalid circuit breaker config")
	}
}

func TestValidateConfig_InvalidRateLimiting(t *testing.T) {
	c := DefaultConfig()
	c.RateLimiting.Enabled = true
	c.RateLimiting.RequestsPerSec = -1

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for negative requests per sec")
	}
}

func TestValidateConfig_InvalidRoutes(t *testing.T) {
	c := DefaultConfig()
	c.Routes = []RouteConfig{
		{Path: "", Targets: []TargetConfig{}},
	}

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for empty route path")
	}

	errs := err.(ValidationErrors)
	if len(errs) < 2 {
		t.Errorf("expected at least 2 errors (empty path + no targets), got %d", len(errs))
	}
}

func TestValidateConfig_DuplicateRoutePaths(t *testing.T) {
	c := DefaultConfig()
	c.Routes = []RouteConfig{
		{Path: "/api", Targets: []TargetConfig{{URL: "http://a:8080"}}},
		{Path: "/api", Targets: []TargetConfig{{URL: "http://b:8080"}}},
	}

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for duplicate route paths")
	}
}

func TestValidateConfig_InvalidTLS(t *testing.T) {
	c := DefaultConfig()
	c.TLS.Enabled = true
	c.TLS.ClientCertFile = "/path/to/cert"
	// Missing key file

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for cert without key")
	}
}

func TestValidateConfig_DisabledSectionsSkipped(t *testing.T) {
	c := DefaultConfig()
	c.RateLimiting.Enabled = false
	c.RateLimiting.RequestsPerSec = -999 // Would be invalid if enabled

	if err := ValidateConfig(&c); err != nil {
		t.Errorf("disabled sections should be skipped, got: %v", err)
	}
}

func TestValidateConfig_HealthCheckTimeoutVsInterval(t *testing.T) {
	c := DefaultConfig()
	c.HealthCheck.Timeout = 30 * time.Second
	c.HealthCheck.Interval = 10 * time.Second

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error when timeout >= interval")
	}
}

func TestValidationErrors_Error(t *testing.T) {
	errs := ValidationErrors{
		{Field: "a.b", Message: "bad"},
		{Field: "c.d", Message: "worse"},
	}

	msg := errs.Error()
	if msg == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestValidateConfig_ExtensionFilters_Valid(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:       "twinos",
			KnownExtensions:   []string{"cortex", "authsome"},
			AllowedExtensions: []string{"authsome"},
			BlockTraffic:      true,
		},
	}

	if err := ValidateConfig(&c); err != nil {
		t.Errorf("valid extension filter should pass, got: %v", err)
	}
}

func TestValidateConfig_ExtensionFilters_ValidMultipleServiceNames(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceNames:      []string{"portal", "twinos"},
			KnownExtensions:   []string{"cortex", "authsome"},
			AllowedExtensions: []string{"authsome"},
			BlockTraffic:      true,
		},
	}

	if err := ValidateConfig(&c); err != nil {
		t.Errorf("valid extension filter with serviceNames should pass, got: %v", err)
	}
}

func TestValidateConfig_ExtensionFilters_ServiceNameOrServiceNamesRequired(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			// Neither ServiceName nor ServiceNames set
			KnownExtensions: []string{"cortex"},
		},
	}

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error when neither serviceName nor serviceNames is set")
	}

	errs := err.(ValidationErrors)
	found := false
	for _, e := range errs {
		if e.Field == "openapi.extensionFilters[0].serviceName" {
			found = true
		}
	}

	if !found {
		t.Error("expected error for extensionFilters[0].serviceName")
	}
}

func TestValidateConfig_ExtensionFilters_ServiceNamesOnlySufficient(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			// Only ServiceNames, no ServiceName
			ServiceNames:    []string{"portal"},
			KnownExtensions: []string{"cortex"},
		},
	}

	if err := ValidateConfig(&c); err != nil {
		t.Errorf("serviceNames alone should be sufficient, got: %v", err)
	}
}

func TestValidateConfig_ExtensionFilters_EmptyServiceName(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:     "",
			KnownExtensions: []string{"cortex"},
		},
	}

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for empty serviceName")
	}

	errs := err.(ValidationErrors)
	found := false
	for _, e := range errs {
		if e.Field == "openapi.extensionFilters[0].serviceName" {
			found = true
		}
	}

	if !found {
		t.Error("expected error for extensionFilters[0].serviceName")
	}
}

func TestValidateConfig_ExtensionFilters_EmptyKnownExtensions(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:     "twinos",
			KnownExtensions: nil,
		},
	}

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for empty knownExtensions")
	}

	errs := err.(ValidationErrors)
	found := false
	for _, e := range errs {
		if e.Field == "openapi.extensionFilters[0].knownExtensions" {
			found = true
		}
	}

	if !found {
		t.Error("expected error for extensionFilters[0].knownExtensions")
	}
}

func TestValidateConfig_ExtensionFilters_UnknownAllowed(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:       "twinos",
			KnownExtensions:   []string{"cortex", "authsome"},
			AllowedExtensions: []string{"authsome", "unknown-ext"},
		},
	}

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for allowed extension not in known")
	}

	errs := err.(ValidationErrors)
	found := false
	for _, e := range errs {
		if e.Field == "openapi.extensionFilters[0].allowedExtensions" {
			found = true
		}
	}

	if !found {
		t.Error("expected error for unknown allowed extension")
	}
}

func TestValidateConfig_ExtensionFilters_ExcludePrefixesValid(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:     "twinos",
			ExcludePrefixes: []string{"/v1/admin", "/v1/instances"},
		},
	}

	if err := ValidateConfig(&c); err != nil {
		t.Errorf("valid exclude prefixes should pass, got: %v", err)
	}
}

func TestValidateConfig_ExtensionFilters_ExcludePrefixMissingSlash(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:     "twinos",
			KnownExtensions: []string{"cortex"},
			ExcludePrefixes: []string{"v1/admin"},
		},
	}

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for prefix without leading /")
	}

	errs := err.(ValidationErrors)
	found := false
	for _, e := range errs {
		if e.Field == "openapi.extensionFilters[0].excludePrefixes[0]" {
			found = true
		}
	}

	if !found {
		t.Error("expected error for excludePrefixes[0]")
	}
}

func TestValidateConfig_ExtensionFilters_IncludePrefixMissingSlash(t *testing.T) {
	c := DefaultConfig()
	c.OpenAPI.ExtensionFilters = []ExtensionPathFilter{
		{
			ServiceName:     "twinos",
			KnownExtensions: []string{"cortex"},
			IncludePrefixes: []string{"v1/auth"},
		},
	}

	err := ValidateConfig(&c)
	if err == nil {
		t.Fatal("expected error for include prefix without leading /")
	}

	errs := err.(ValidationErrors)
	found := false
	for _, e := range errs {
		if e.Field == "openapi.extensionFilters[0].includePrefixes[0]" {
			found = true
		}
	}

	if !found {
		t.Error("expected error for includePrefixes[0]")
	}
}
