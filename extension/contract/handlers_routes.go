package contract

import (
	"context"

	"github.com/xraph/forge/extensions/dashboard/contract"
)

type routesListRequest struct{}
type routesListResponse struct{}

func routesListHandler(_ Deps) func(context.Context, routesListRequest, contract.Principal) (routesListResponse, error) {
	return func(context.Context, routesListRequest, contract.Principal) (routesListResponse, error) {
		return routesListResponse{}, nil
	}
}

type routesDetailRequest struct{}
type routesDetailResponse struct{}

func routesDetailHandler(_ Deps) func(context.Context, routesDetailRequest, contract.Principal) (routesDetailResponse, error) {
	return func(context.Context, routesDetailRequest, contract.Principal) (routesDetailResponse, error) {
		return routesDetailResponse{}, nil
	}
}

type upstreamsListRequest struct{}
type upstreamsListResponse struct{}

func upstreamsListHandler(_ Deps) func(context.Context, upstreamsListRequest, contract.Principal) (upstreamsListResponse, error) {
	return func(context.Context, upstreamsListRequest, contract.Principal) (upstreamsListResponse, error) {
		return upstreamsListResponse{}, nil
	}
}
