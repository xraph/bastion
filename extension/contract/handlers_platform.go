package contract

import (
	"context"

	"github.com/xraph/forge/extensions/dashboard/contract"
)

type servicesListRequest struct{}
type servicesListResponse struct{}

func servicesListHandler(_ Deps) func(context.Context, servicesListRequest, contract.Principal) (servicesListResponse, error) {
	return func(context.Context, servicesListRequest, contract.Principal) (servicesListResponse, error) {
		return servicesListResponse{}, nil
	}
}

type openapiSummaryRequest struct{}
type openapiSummaryResponse struct{}

func openapiSummaryHandler(_ Deps) func(context.Context, openapiSummaryRequest, contract.Principal) (openapiSummaryResponse, error) {
	return func(context.Context, openapiSummaryRequest, contract.Principal) (openapiSummaryResponse, error) {
		return openapiSummaryResponse{}, nil
	}
}

type configDetailRequest struct{}
type configDetailResponse struct{}

func configDetailHandler(_ Deps) func(context.Context, configDetailRequest, contract.Principal) (configDetailResponse, error) {
	return func(context.Context, configDetailRequest, contract.Principal) (configDetailResponse, error) {
		return configDetailResponse{}, nil
	}
}
