package contract

import (
	"context"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/bastion/admin"
)

type routesListRequest struct {
	Source   string `json:"source,omitempty"`
	Protocol string `json:"protocol,omitempty"`
}
type routesListResponse struct {
	Routes []admin.RouteSummary `json:"routes"`
	Total  int                  `json:"total"`
}
type routesDetailRequest struct {
	ID string `json:"id"`
}
type routesDetailResponse = admin.RouteDetail
type upstreamsListRequest struct{}
type upstreamsListResponse struct {
	Upstreams []admin.Upstream `json:"upstreams"`
	Total     int              `json:"total"`
}

func routesListHandler(deps Deps) func(context.Context, routesListRequest, contract.Principal) (routesListResponse, error) {
	return func(_ context.Context, in routesListRequest, _ contract.Principal) (routesListResponse, error) {
		routes := deps.Admin.ListRoutes(admin.RouteFilter{Source: in.Source, Protocol: in.Protocol})

		return routesListResponse{Routes: routes, Total: len(routes)}, nil
	}
}

// routesDetailHandler takes the id in the body: config route ids contain a
// slash ("manual-/users") and cannot sit in a URL segment.
func routesDetailHandler(deps Deps) func(context.Context, routesDetailRequest, contract.Principal) (routesDetailResponse, error) {
	return func(_ context.Context, in routesDetailRequest, _ contract.Principal) (routesDetailResponse, error) {
		id, err := requireID(in.ID)
		if err != nil {
			return routesDetailResponse{}, err
		}

		d, err := deps.Admin.GetRoute(id)
		if err != nil {
			return routesDetailResponse{}, deps.mapError("routes.detail", err)
		}

		return d, nil
	}
}

func upstreamsListHandler(deps Deps) func(context.Context, upstreamsListRequest, contract.Principal) (upstreamsListResponse, error) {
	return func(context.Context, upstreamsListRequest, contract.Principal) (upstreamsListResponse, error) {
		ups := deps.Admin.Upstreams()

		return upstreamsListResponse{Upstreams: ups, Total: len(ups)}, nil
	}
}
