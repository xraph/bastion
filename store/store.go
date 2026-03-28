// Package store defines the composite store interface for Bastion.
package store

import (
	"context"

	"github.com/xraph/bastion"
)

// Store is the composite persistence interface for all Bastion subsystems.
type Store interface {
	bastion.RouteStore
	bastion.CircuitBreakerStore
	bastion.HealthStore
	bastion.CacheStore
	bastion.RateLimitStore
	bastion.AuditSink

	// Migrate runs schema migrations.
	Migrate(ctx context.Context) error

	// Ping verifies the store connection.
	Ping(ctx context.Context) error

	// Close releases store resources.
	Close() error
}
