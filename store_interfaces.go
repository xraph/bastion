package bastion

import (
	"context"
	"time"
)

// RouteStore persists gateway route configuration.
type RouteStore interface {
	// ListRoutes returns all persisted routes.
	ListRoutes(ctx context.Context) ([]*Route, error)

	// GetRoute returns a single route by ID.
	GetRoute(ctx context.Context, routeID string) (*Route, error)

	// SaveRoute creates or updates a route.
	SaveRoute(ctx context.Context, route *Route) error

	// DeleteRoute removes a route by ID.
	DeleteRoute(ctx context.Context, routeID string) error
}

// CircuitBreakerStore persists circuit breaker state snapshots.
type CircuitBreakerStore interface {
	// GetState returns the persisted state for a target.
	GetState(ctx context.Context, targetID string) (*CircuitBreakerSnapshot, error)

	// SaveState persists the current circuit breaker state for a target.
	SaveState(ctx context.Context, snap *CircuitBreakerSnapshot) error

	// ListStates returns all persisted circuit breaker states.
	ListStates(ctx context.Context) ([]*CircuitBreakerSnapshot, error)

	// DeleteState removes the persisted state for a target.
	DeleteState(ctx context.Context, targetID string) error
}

// HealthStore persists health check results for upstream targets.
type HealthStore interface {
	// RecordCheck persists a single health check result.
	RecordCheck(ctx context.Context, result *HealthCheckResult) error

	// GetHistory returns recent health check results for a target, newest first.
	GetHistory(ctx context.Context, targetID string, limit int) ([]HealthCheckResult, error)

	// GetLatestState returns the most recent health check for a target.
	GetLatestState(ctx context.Context, targetID string) (*HealthCheckResult, error)
}

// CircuitBreakerSnapshot is the persistable state of a circuit breaker.
type CircuitBreakerSnapshot struct {
	TargetID        string       `json:"targetId"`
	State           CircuitState `json:"state"`
	FailureCount    int          `json:"failureCount"`
	SuccessCount    int          `json:"successCount"`
	LastFailure     time.Time    `json:"lastFailure"`
	LastStateChange time.Time    `json:"lastStateChange"`
	UpdatedAt       time.Time    `json:"updatedAt"`
}
