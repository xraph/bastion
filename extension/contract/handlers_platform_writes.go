package contract

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/xraph/forge"
	"github.com/xraph/forge/extensions/dashboard/contract"

	bastion "github.com/xraph/bastion"
)

type discoveryRefreshRequest struct{}

type discoveryRefreshResponse struct {
	OK bool `json:"ok"`
}

type openapiRefreshRequest struct{}

type openapiRefreshResponse struct {
	Started bool `json:"started"`
}

type circuitsResetRequest struct {
	TargetID string `json:"targetId"`
}

type circuitsResetResponse struct {
	TargetID string               `json:"targetId"`
	State    bastion.CircuitState `json:"state"`
}

const (
	discoveryRefreshTimeout = 30 * time.Second
	openapiRefreshTimeout   = 2 * time.Minute
)

func discoveryRefreshHandler(deps Deps) func(context.Context, discoveryRefreshRequest, contract.Principal) (discoveryRefreshResponse, error) {
	return func(ctx context.Context, _ discoveryRefreshRequest, p contract.Principal) (discoveryRefreshResponse, error) {
		disc := deps.Gateway.Discovery()
		if !deps.Gateway.Config().Discovery.Enabled || disc == nil {
			return discoveryRefreshResponse{}, &contract.Error{
				Code:    contract.CodeConflict,
				Message: "discovery is switched off in the gateway config, so there is nothing to refresh",
				Details: map[string]any{"reason": "discoveryOff"},
			}
		}

		// Detached from the request: an operator who navigates away should
		// not cancel a scan halfway through rebuilding routes.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryRefreshTimeout)
		defer cancel()

		if err := disc.Refresh(rctx); err != nil {
			if deps.Logger != nil {
				deps.Logger.Warn("bastion/contract: discovery refresh failed", forge.F("error", err))
			}

			return discoveryRefreshResponse{}, &contract.Error{
				Code:    contract.CodeUnavailable,
				Message: "the discovery refresh failed; the gateway log has the cause",
			}
		}

		deps.audit("discovery.refresh", "", p)

		return discoveryRefreshResponse{OK: true}, nil
	}
}

func openapiRefreshHandler(deps Deps) func(context.Context, openapiRefreshRequest, contract.Principal) (openapiRefreshResponse, error) {
	return func(_ context.Context, _ openapiRefreshRequest, p contract.Principal) (openapiRefreshResponse, error) {
		oa := deps.Gateway.OpenAPI()
		if oa == nil {
			return openapiRefreshResponse{}, &contract.Error{
				Code:    contract.CodeConflict,
				Message: "OpenAPI aggregation is not running on this gateway",
				Details: map[string]any{"reason": "openapiOff"},
			}
		}

		// Fetching every service's spec can take a while, so it runs in the
		// background with its own bound. The aggregator drops a second
		// refresh while one is in flight.
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), openapiRefreshTimeout)
			defer cancel()
			oa.Refresh(rctx)
		}()

		deps.audit("openapi.refresh", "", p)

		return openapiRefreshResponse{Started: true}, nil
	}
}

func circuitsResetHandler(deps Deps) func(context.Context, circuitsResetRequest, contract.Principal) (circuitsResetResponse, error) {
	return func(_ context.Context, in circuitsResetRequest, p contract.Principal) (circuitsResetResponse, error) {
		id := strings.TrimSpace(in.TargetID)
		if id == "" {
			return circuitsResetResponse{}, fieldError("targetId", "targetId is required")
		}

		if err := deps.Gateway.ResetCircuit(id); err != nil {
			if errors.Is(err, bastion.ErrCircuitNotFound) {
				return circuitsResetResponse{}, &contract.Error{
					Code:    contract.CodeNotFound,
					Message: "this target has no circuit breaker yet; it gets one on its first proxied request",
				}
			}

			return circuitsResetResponse{}, deps.mapError("circuits.reset", err)
		}

		deps.audit("circuits.reset", id, p)

		return circuitsResetResponse{TargetID: id, State: bastion.CircuitClosed}, nil
	}
}
