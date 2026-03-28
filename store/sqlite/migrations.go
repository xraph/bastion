package sqlite

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Migrations is the grove migration group for the Bastion SQLite store.
var Migrations = migrate.NewGroup("bastion")

func init() {
	Migrations.MustRegister(
		&migrate.Migration{
			Name:    "create_bastion_routes",
			Version: "20240101000001",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `CREATE TABLE IF NOT EXISTS bastion_routes (
					id           TEXT PRIMARY KEY,
					path         TEXT NOT NULL,
					methods      TEXT DEFAULT '[]',
					targets      TEXT DEFAULT '[]',
					strip_prefix BOOLEAN DEFAULT 0,
					add_prefix   TEXT DEFAULT '',
					rewrite_path TEXT DEFAULT '',
					headers      TEXT DEFAULT '{}',
					protocol     TEXT DEFAULT 'http',
					source       TEXT DEFAULT 'manual',
					service_name TEXT DEFAULT '',
					priority     INTEGER DEFAULT 0,
					version      INTEGER DEFAULT 0,
					enabled      BOOLEAN DEFAULT 1,
					retry        TEXT DEFAULT 'null',
					timeout      TEXT DEFAULT 'null',
					rate_limit   TEXT DEFAULT 'null',
					auth         TEXT DEFAULT 'null',
					circuit_breaker TEXT DEFAULT 'null',
					cache        TEXT DEFAULT 'null',
					traffic_policy TEXT DEFAULT 'null',
					transform    TEXT DEFAULT 'null',
					metadata     TEXT DEFAULT '{}',
					created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
					updated_at   DATETIME NOT NULL DEFAULT (datetime('now'))
				)`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_routes`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_bastion_circuit_breaker_states",
			Version: "20240101000002",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `CREATE TABLE IF NOT EXISTS bastion_circuit_breaker_states (
					target_id        TEXT PRIMARY KEY,
					state            TEXT NOT NULL DEFAULT 'closed',
					failure_count    INTEGER DEFAULT 0,
					success_count    INTEGER DEFAULT 0,
					last_failure     DATETIME,
					last_state_change DATETIME,
					updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
				)`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_circuit_breaker_states`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_bastion_health_checks",
			Version: "20240101000003",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				if _, err := exec.Exec(ctx, `CREATE TABLE IF NOT EXISTS bastion_health_checks (
					id          INTEGER PRIMARY KEY AUTOINCREMENT,
					target_id   TEXT NOT NULL,
					target_url  TEXT NOT NULL DEFAULT '',
					healthy     BOOLEAN NOT NULL DEFAULT 0,
					latency_ns  INTEGER DEFAULT 0,
					error       TEXT DEFAULT '',
					checked_at  DATETIME NOT NULL DEFAULT (datetime('now'))
				)`); err != nil {
					return err
				}

				_, err := exec.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_health_checks_target_time
					ON bastion_health_checks (target_id, checked_at DESC)`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_health_checks`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_bastion_audit_events",
			Version: "20240101000004",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				if _, err := exec.Exec(ctx, `CREATE TABLE IF NOT EXISTS bastion_audit_events (
					id         INTEGER PRIMARY KEY AUTOINCREMENT,
					action     TEXT NOT NULL,
					actor      TEXT DEFAULT '',
					resource   TEXT DEFAULT '',
					detail     TEXT DEFAULT '{}',
					result     TEXT DEFAULT '',
					error      TEXT DEFAULT '',
					created_at DATETIME NOT NULL DEFAULT (datetime('now'))
				)`); err != nil {
					return err
				}

				_, err := exec.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_audit_events_time
					ON bastion_audit_events (created_at DESC)`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_audit_events`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_bastion_cache_entries",
			Version: "20240101000005",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				if _, err := exec.Exec(ctx, `CREATE TABLE IF NOT EXISTS bastion_cache_entries (
					key        TEXT PRIMARY KEY,
					value      BLOB NOT NULL,
					expires_at DATETIME NOT NULL
				)`); err != nil {
					return err
				}

				_, err := exec.Exec(ctx, `CREATE INDEX IF NOT EXISTS idx_cache_entries_expires
					ON bastion_cache_entries (expires_at)`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_cache_entries`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_bastion_rate_limits",
			Version: "20240101000006",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `CREATE TABLE IF NOT EXISTS bastion_rate_limits (
					key       TEXT PRIMARY KEY,
					tokens    REAL NOT NULL DEFAULT 0,
					last_time DATETIME NOT NULL DEFAULT (datetime('now'))
				)`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS bastion_rate_limits`)
				return err
			},
		},
	)
}
