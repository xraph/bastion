package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/xraph/farp"
	"github.com/xraph/forge"
)

// SchemaFetcher resolves raw schema content from FARP SchemaDescriptors.
// It supports inline schemas (embedded in manifests) and HTTP-located schemas,
// with retry logic and size limits.
type SchemaFetcher struct {
	httpClient *http.Client
	logger     forge.Logger
}

// NewSchemaFetcher creates a new schema fetcher with the given HTTP client and logger.
func NewSchemaFetcher(httpClient *http.Client, logger forge.Logger) *SchemaFetcher {
	return &SchemaFetcher{
		httpClient: httpClient,
		logger:     logger,
	}
}

// FetchedSchema holds the result of fetching a schema from a descriptor.
type FetchedSchema struct {
	// Schema is the parsed schema content.
	Schema map[string]any

	// SchemaType is the type of the schema (openapi, asyncapi, etc.).
	SchemaType farp.SchemaType

	// ServiceName is the service that owns this schema.
	ServiceName string

	// SpecURL is the URL the schema was fetched from (empty for inline schemas).
	SpecURL string

	// FetchedAt is when the schema was retrieved.
	FetchedAt time.Time

	// Error is non-empty if the fetch failed.
	Error string

	// Healthy indicates whether the schema was successfully retrieved.
	Healthy bool

	// PathCount is the number of paths in the schema (for OpenAPI).
	PathCount int
}

// FetchSchema resolves a schema from a SchemaDescriptor, checking inline first
// then falling back to HTTP. Returns the parsed JSON schema.
func (sf *SchemaFetcher) FetchSchema(ctx context.Context, desc farp.SchemaDescriptor, serviceName string) *FetchedSchema {
	result := &FetchedSchema{
		SchemaType:  desc.Type,
		ServiceName: serviceName,
		FetchedAt:   time.Now(),
	}

	// 1. Check for inline schema (embedded in manifest, < 100KB recommended).
	if desc.Location.Type == farp.LocationTypeInline || desc.InlineSchema != nil {
		schema, err := sf.resolveInlineSchema(desc.InlineSchema)
		if err != nil {
			result.Error = fmt.Sprintf("invalid inline schema: %v", err)
			return result
		}

		result.Schema = schema
		result.Healthy = true
		result.PathCount = countPaths(schema)

		if sf.logger != nil {
			sf.logger.Debug("resolved inline schema",
				forge.F("service", serviceName),
				forge.F("type", string(desc.Type)),
				forge.F("paths", result.PathCount),
			)
		}

		return result
	}

	// 2. Fetch via HTTP from the schema location URL.
	if desc.Location.Type == farp.LocationTypeHTTP && desc.Location.URL != "" {
		result.SpecURL = desc.Location.URL
		return sf.fetchHTTP(ctx, desc.Location.URL, desc.Location.Headers, serviceName, result)
	}

	// 3. Registry location type (Consul, etcd KV store) — not yet supported.
	if desc.Location.Type == farp.LocationTypeRegistry {
		result.Error = "registry-based schema location not yet supported"
		if sf.logger != nil {
			sf.logger.Warn("schema uses registry location type which is not yet supported",
				forge.F("service", serviceName),
				forge.F("registry_path", desc.Location.RegistryPath),
			)
		}
		return result
	}

	// 4. No usable location — schema cannot be resolved.
	result.Error = fmt.Sprintf("unsupported schema location type: %s", desc.Location.Type)

	return result
}

// FetchFromURL fetches a schema directly from a URL (for non-FARP services
// that only have flat metadata keys like farp.openapi).
func (sf *SchemaFetcher) FetchFromURL(ctx context.Context, specURL, serviceName string) *FetchedSchema {
	result := &FetchedSchema{
		ServiceName: serviceName,
		SpecURL:     specURL,
		FetchedAt:   time.Now(),
	}

	return sf.fetchHTTP(ctx, specURL, nil, serviceName, result)
}

// fetchHTTP fetches and parses a JSON schema from the given URL.
func (sf *SchemaFetcher) fetchHTTP(ctx context.Context, url string, headers map[string]string, serviceName string, result *FetchedSchema) *FetchedSchema {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		return result
	}

	req.Header.Set("Accept", "application/json")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := sf.httpClient.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("fetch failed: %v", err)

		if sf.logger != nil {
			sf.logger.Debug("failed to fetch schema",
				forge.F("service", serviceName),
				forge.F("url", url),
				forge.F("error", err),
			)
		}

		return result
	}

	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)

		if sf.logger != nil {
			sf.logger.Debug("non-200 response for schema",
				forge.F("service", serviceName),
				forge.F("url", url),
				forge.F("status", resp.StatusCode),
			)
		}

		return result
	}

	// Read with a size limit (10MB max).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		result.Error = fmt.Sprintf("read failed: %v", err)
		return result
	}

	var spec map[string]any
	if err := json.Unmarshal(body, &spec); err != nil {
		result.Error = fmt.Sprintf("invalid JSON: %v", err)

		if sf.logger != nil {
			sf.logger.Debug("failed to parse schema",
				forge.F("service", serviceName),
				forge.F("url", url),
				forge.F("error", err),
			)
		}

		return result
	}

	result.Schema = spec
	result.Healthy = true
	result.PathCount = countPaths(spec)

	if sf.logger != nil {
		sf.logger.Debug("fetched schema",
			forge.F("service", serviceName),
			forge.F("url", url),
			forge.F("paths", result.PathCount),
		)
	}

	return result
}

// resolveInlineSchema converts an inline schema (any type) to map[string]any.
func (sf *SchemaFetcher) resolveInlineSchema(inline any) (map[string]any, error) {
	if inline == nil {
		return nil, fmt.Errorf("inline schema is nil")
	}

	// If it's already a map, use it directly.
	if m, ok := inline.(map[string]any); ok {
		return m, nil
	}

	// Otherwise, marshal and unmarshal to normalize the type.
	data, err := json.Marshal(inline)
	if err != nil {
		return nil, fmt.Errorf("marshal inline schema: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal inline schema: %w", err)
	}

	return result, nil
}
