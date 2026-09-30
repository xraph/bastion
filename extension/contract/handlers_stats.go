package contract

import (
	"context"

	"github.com/xraph/forge/extensions/dashboard/contract"
)

type overviewStatsRequest struct{}
type overviewStatsResponse struct{}

func overviewStatsHandler(_ Deps) func(context.Context, overviewStatsRequest, contract.Principal) (overviewStatsResponse, error) {
	return func(context.Context, overviewStatsRequest, contract.Principal) (overviewStatsResponse, error) {
		return overviewStatsResponse{}, nil
	}
}

type trafficStatsRequest struct{}
type trafficStatsResponse struct{}

func trafficStatsHandler(_ Deps) func(context.Context, trafficStatsRequest, contract.Principal) (trafficStatsResponse, error) {
	return func(context.Context, trafficStatsRequest, contract.Principal) (trafficStatsResponse, error) {
		return trafficStatsResponse{}, nil
	}
}

type circuitsListRequest struct{}
type circuitsListResponse struct{}

func circuitsListHandler(_ Deps) func(context.Context, circuitsListRequest, contract.Principal) (circuitsListResponse, error) {
	return func(context.Context, circuitsListRequest, contract.Principal) (circuitsListResponse, error) {
		return circuitsListResponse{}, nil
	}
}
