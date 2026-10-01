package contract

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
	"github.com/xraph/bastion/admin"
)

type targetInput struct {
	URL    string   `json:"url"`
	Weight int      `json:"weight"`
	Tags   []string `json:"tags,omitempty"`
}

// routeFields is what the editor sends on create.
type routeFields struct {
	Path        string                   `json:"path"`
	Methods     []string                 `json:"methods"`
	Priority    int                      `json:"priority"`
	Enabled     bool                     `json:"enabled"`
	Protocol    bastion.RouteProtocol    `json:"protocol"`
	StripPrefix bool                     `json:"stripPrefix"`
	AddPrefix   string                   `json:"addPrefix"`
	RewritePath string                   `json:"rewritePath"`
	Targets     []targetInput            `json:"targets"`
	RateLimit   *bastion.RateLimitConfig `json:"rateLimit"`
	Auth        *bastion.RouteAuthConfig `json:"auth"`
}

type routesCreateRequest = routeFields
type routesCreateResponse struct {
	ID string `json:"id"`
}

// routesUpdateRequest changes only the fields present. rateLimit and auth
// are raw: absent keeps the stored override, null clears it, an object
// replaces it.
type routesUpdateRequest struct {
	ID          string                 `json:"id"`
	Path        *string                `json:"path,omitempty"`
	Methods     *[]string              `json:"methods,omitempty"`
	Priority    *int                   `json:"priority,omitempty"`
	Enabled     *bool                  `json:"enabled,omitempty"`
	Protocol    *bastion.RouteProtocol `json:"protocol,omitempty"`
	StripPrefix *bool                  `json:"stripPrefix,omitempty"`
	AddPrefix   *string                `json:"addPrefix,omitempty"`
	RewritePath *string                `json:"rewritePath,omitempty"`
	Targets     *[]targetInput         `json:"targets,omitempty"`
	RateLimit   json.RawMessage        `json:"rateLimit,omitempty"`
	Auth        json.RawMessage        `json:"auth,omitempty"`
}
type routesUpdateResponse struct {
	ID string `json:"id"`
}

type routesDeleteRequest struct {
	ID string `json:"id"`
}
type routesDeleteResponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
}

type routesSetEnabledRequest struct {
	ID      string `json:"id"`
	Enabled *bool  `json:"enabled"`
}
type routesSetEnabledResponse struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// Durable is false when the change ends at the next restart: a route
	// from the config file, or a gateway with no route store.
	Durable bool `json:"durable"`
}

func (f routeFields) dto() bastion.RouteDTO {
	dto := bastion.RouteDTO{
		Path: f.Path, Methods: f.Methods, Priority: f.Priority, Enabled: f.Enabled, Protocol: f.Protocol,
		StripPrefix: f.StripPrefix, AddPrefix: f.AddPrefix, RewritePath: f.RewritePath,
		RateLimit: f.RateLimit, Auth: f.Auth,
		Targets: make([]bastion.TargetDTO, 0, len(f.Targets)),
	}
	for _, t := range f.Targets {
		dto.Targets = append(dto.Targets, bastion.TargetDTO{URL: t.URL, Weight: t.Weight, Tags: t.Tags})
	}

	return dto
}

// mergeTargets merges edited targets into the stored ones. A URL the
// dashboard showed masked maps back to the stored URL, so a masked password
// is never saved, and a kept upstream keeps the metadata and TLS the editor
// does not show.
//
// A masked URL whose host or port was edited has no exact match. Its password
// is then restored from the one stored upstream with the same scheme, user
// and host, so the placeholder never reaches the validator as a real
// password.
func mergeTargets(stored []bastion.TargetDTO, in []targetInput) []bastion.TargetDTO {
	byShown := make(map[string]bastion.TargetDTO, len(stored)*2)
	for _, s := range stored {
		byShown[admin.RedactURL(s.URL)] = s
		byShown[s.URL] = s
	}

	out := make([]bastion.TargetDTO, 0, len(in))
	for _, t := range in {
		td := bastion.TargetDTO{URL: t.URL, Weight: t.Weight, Tags: t.Tags}
		if s, ok := byShown[t.URL]; ok {
			td.URL, td.Metadata, td.TLS = s.URL, s.Metadata, s.TLS
		} else if restored, ok := restorePassword(stored, t.URL); ok {
			td.URL = restored
		}

		out = append(out, td)
	}

	return out
}

// restorePassword swaps the redaction placeholder in raw for the stored
// password of the single stored upstream sharing its scheme, user and host.
func restorePassword(stored []bastion.TargetDTO, raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return "", false
	}

	if pw, ok := u.User.Password(); !ok || pw != admin.RedactedPassword {
		return "", false
	}

	var found string

	matches := 0

	for _, s := range stored {
		su, err := url.Parse(s.URL)
		if err != nil || su.User == nil {
			continue
		}

		spw, ok := su.User.Password()
		if !ok || su.Scheme != u.Scheme || su.User.Username() != u.User.Username() || su.Hostname() != u.Hostname() {
			continue
		}

		found = spw
		matches++
	}

	if matches != 1 {
		return "", false
	}

	u.User = url.UserPassword(u.User.Username(), found)

	return u.String(), true
}

func (in routesUpdateRequest) apply(dto *bastion.RouteDTO) error {
	if in.Path != nil {
		dto.Path = *in.Path
	}
	if in.Methods != nil {
		dto.Methods = *in.Methods
	}
	if in.Priority != nil {
		dto.Priority = *in.Priority
	}
	if in.Enabled != nil {
		dto.Enabled = *in.Enabled
	}
	if in.Protocol != nil {
		dto.Protocol = *in.Protocol
	}
	if in.StripPrefix != nil {
		dto.StripPrefix = *in.StripPrefix
	}
	if in.AddPrefix != nil {
		dto.AddPrefix = *in.AddPrefix
	}
	if in.RewritePath != nil {
		dto.RewritePath = *in.RewritePath
	}
	if in.Targets != nil {
		dto.Targets = mergeTargets(dto.Targets, *in.Targets)
	}

	if len(in.RateLimit) > 0 {
		var rl *bastion.RateLimitConfig
		if err := json.Unmarshal(in.RateLimit, &rl); err != nil {
			return fieldError("rateLimit", "rate limit must be an object or null")
		}
		dto.RateLimit = rl
	}

	if len(in.Auth) > 0 {
		var a *bastion.RouteAuthConfig
		if err := json.Unmarshal(in.Auth, &a); err != nil {
			return fieldError("auth", "auth must be an object or null")
		}
		dto.Auth = a
	}

	return nil
}

func routesCreateHandler(deps Deps) func(context.Context, routesCreateRequest, contract.Principal) (routesCreateResponse, error) {
	return func(_ context.Context, in routesCreateRequest, p contract.Principal) (routesCreateResponse, error) {
		r, err := deps.Admin.CreateRoute(in.dto())
		if err != nil {
			return routesCreateResponse{}, deps.mapError("routes.create", err)
		}

		deps.audit("routes.create", r.ID, p)

		return routesCreateResponse{ID: r.ID}, nil
	}
}

func routesUpdateHandler(deps Deps) func(context.Context, routesUpdateRequest, contract.Principal) (routesUpdateResponse, error) {
	return func(_ context.Context, in routesUpdateRequest, p contract.Principal) (routesUpdateResponse, error) {
		id, err := requireID(in.ID)
		if err != nil {
			return routesUpdateResponse{}, err
		}

		dto, err := deps.Admin.Entry(id)
		if err != nil {
			return routesUpdateResponse{}, deps.mapError("routes.update", err)
		}

		if err := in.apply(&dto); err != nil {
			return routesUpdateResponse{}, err
		}

		if _, err := deps.Admin.UpdateRoute(id, dto); err != nil {
			return routesUpdateResponse{}, deps.mapError("routes.update", err)
		}

		deps.audit("routes.update", id, p)

		return routesUpdateResponse{ID: id}, nil
	}
}

func routesDeleteHandler(deps Deps) func(context.Context, routesDeleteRequest, contract.Principal) (routesDeleteResponse, error) {
	return func(_ context.Context, in routesDeleteRequest, p contract.Principal) (routesDeleteResponse, error) {
		id, err := requireID(in.ID)
		if err != nil {
			return routesDeleteResponse{}, err
		}

		if err := deps.Admin.DeleteRoute(id); err != nil {
			return routesDeleteResponse{}, deps.mapError("routes.delete", err)
		}

		deps.audit("routes.delete", id, p)

		return routesDeleteResponse{OK: true, ID: id}, nil
	}
}

func routesSetEnabledHandler(deps Deps) func(context.Context, routesSetEnabledRequest, contract.Principal) (routesSetEnabledResponse, error) {
	return func(_ context.Context, in routesSetEnabledRequest, p contract.Principal) (routesSetEnabledResponse, error) {
		id, err := requireID(in.ID)
		if err != nil {
			return routesSetEnabledResponse{}, err
		}

		if in.Enabled == nil {
			return routesSetEnabledResponse{}, fieldError("enabled", "enabled is required")
		}

		durable, err := deps.Admin.SetEnabled(id, *in.Enabled)
		if err != nil {
			return routesSetEnabledResponse{}, deps.mapError("routes.setEnabled", err)
		}

		deps.audit("routes.setEnabled", id, p)

		return routesSetEnabledResponse{ID: id, Enabled: *in.Enabled, Durable: durable}, nil
	}
}
