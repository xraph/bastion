package postgres

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Migrations is the grove migration group for the Bastion postgres store.
var Migrations = func() *migrate.Group {
	g := migrate.NewGroup("bastion")
	g.MustRegister(
		&migrate.Migration{
			Name:    "create_routes",
			Version: "20240101000001",
			Comment: "Create bastion_routes table",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS bastion_routes (
    id              TEXT PRIMARY KEY,
    path            TEXT NOT NULL,
    methods         JSONB DEFAULT '[]',
    targets         JSONB DEFAULT '[]',
    strip_prefix    BOOLEAN DEFAULT FALSE,
    add_prefix      TEXT DEFAULT '',
    rewrite_path    TEXT DEFAULT '',
    headers         JSONB DEFAULT '{}',
    protocol        TEXT DEFAULT 'http',
    source          TEXT DEFAULT 'manual',
    service_name    TEXT DEFAULT '',
    priority        INTEGER DEFAULT 0,
    version         BIGINT DEFAULT 0,
    enabled         BOOLEAN DEFAULT TRUE,
    retry           JSONB,
    timeout         JSONB,
    rate_limit      JSONB,
    auth            JSONB,
    circuit_breaker JSONB,
    cache           JSONB,
    traffic_policy  JSONB,
    transform       JSONB,
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_bastion_routes_path ON bastion_routes (path);
CREATE INDEX IF NOT EXISTS idx_bastion_routes_source ON bastion_routes (source);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_routes CASCADE`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_circuit_breaker_states",
			Version: "20240101000002",
			Comment: "Create bastion_circuit_breaker_states table",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS bastion_circuit_breaker_states (
    target_id        TEXT PRIMARY KEY,
    state            TEXT NOT NULL DEFAULT 'closed',
    failure_count    INTEGER DEFAULT 0,
    success_count    INTEGER DEFAULT 0,
    last_failure     TIMESTAMPTZ,
    last_state_change TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_circuit_breaker_states`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_health_checks",
			Version: "20240101000003",
			Comment: "Create bastion_health_checks table",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS bastion_health_checks (
    id          BIGSERIAL PRIMARY KEY,
    target_id   TEXT NOT NULL,
    target_url  TEXT DEFAULT '',
    healthy     BOOLEAN NOT NULL,
    latency_ns  BIGINT DEFAULT 0,
    error       TEXT DEFAULT '',
    checked_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_bastion_health_target_time ON bastion_health_checks (target_id, checked_at DESC);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_health_checks`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_audit_events",
			Version: "20240101000004",
			Comment: "Create bastion_audit_events table",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS bastion_audit_events (
    id          BIGSERIAL PRIMARY KEY,
    action      TEXT NOT NULL,
    actor       TEXT DEFAULT '',
    resource    TEXT DEFAULT '',
    detail      JSONB DEFAULT '{}',
    result      TEXT DEFAULT '',
    error       TEXT DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_bastion_audit_time ON bastion_audit_events (created_at DESC);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_audit_events`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_cache_entries",
			Version: "20240101000005",
			Comment: "Create bastion_cache_entries table",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS bastion_cache_entries (
    key         TEXT PRIMARY KEY,
    value       BYTEA NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_bastion_cache_expires ON bastion_cache_entries (expires_at);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_cache_entries`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_rate_limits",
			Version: "20240101000006",
			Comment: "Create bastion_rate_limits table",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS bastion_rate_limits (
    key         TEXT PRIMARY KEY,
    tokens      DOUBLE PRECISION NOT NULL DEFAULT 0,
    last_time   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_rate_limits`)
				return err
			},
		},
	)

	return g
}()
