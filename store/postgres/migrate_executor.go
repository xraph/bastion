package postgres

import (
	"context"
	"fmt"

	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"
)

const (
	migrationTableName = "grove_migrations"
	lockTableName      = "grove_migration_locks"
)

// pgMigrateExecutor implements migrate.Executor for PostgreSQL.
type pgMigrateExecutor struct {
	pgdb *pgdriver.PgDB
}

var _ migrate.Executor = (*pgMigrateExecutor)(nil)

func (e *pgMigrateExecutor) Exec(ctx context.Context, query string, args ...any) (driver.Result, error) {
	return e.pgdb.Exec(ctx, query, args...)
}

func (e *pgMigrateExecutor) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	return e.pgdb.Query(ctx, query, args...)
}

func (e *pgMigrateExecutor) EnsureMigrationTable(ctx context.Context) error {
	query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id          BIGSERIAL PRIMARY KEY,
		version     VARCHAR(14) NOT NULL,
		name        VARCHAR(255) NOT NULL,
		"group"     VARCHAR(255) NOT NULL,
		migrated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE(version, "group")
	)`, migrationTableName)
	_, err := e.pgdb.Exec(ctx, query)

	return err
}

func (e *pgMigrateExecutor) EnsureLockTable(ctx context.Context) error {
	query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id          INT PRIMARY KEY DEFAULT 1,
		locked_at   TIMESTAMPTZ,
		locked_by   VARCHAR(255),
		CONSTRAINT single_lock CHECK (id = 1)
	)`, lockTableName)
	_, err := e.pgdb.Exec(ctx, query)

	return err
}

func (e *pgMigrateExecutor) AcquireLock(ctx context.Context, lockedBy string) error {
	row := e.pgdb.QueryRow(ctx, "SELECT pg_try_advisory_lock(1)")

	var acquired bool
	if err := row.Scan(&acquired); err != nil {
		return fmt.Errorf("bastion: advisory lock: %w", err)
	}

	if !acquired {
		return fmt.Errorf("bastion: migration lock is held by another process")
	}

	query := fmt.Sprintf(`INSERT INTO %s (id, locked_at, locked_by)
		VALUES (1, NOW(), $1)
		ON CONFLICT (id) DO UPDATE SET locked_at = NOW(), locked_by = $1`,
		lockTableName)
	_, err := e.pgdb.Exec(ctx, query, lockedBy)

	return err
}

func (e *pgMigrateExecutor) ReleaseLock(ctx context.Context) error {
	_, err := e.pgdb.Exec(ctx, "SELECT pg_advisory_unlock(1)")

	return err
}

func (e *pgMigrateExecutor) ListApplied(ctx context.Context) ([]*migrate.AppliedMigration, error) {
	query := fmt.Sprintf(
		`SELECT id, version, name, "group", migrated_at::text FROM %s ORDER BY id ASC`,
		migrationTableName)

	rows, err := e.pgdb.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var applied []*migrate.AppliedMigration
	for rows.Next() {
		a := &migrate.AppliedMigration{}
		if err := rows.Scan(&a.ID, &a.Version, &a.Name, &a.Group, &a.MigratedAt); err != nil {
			return nil, fmt.Errorf("bastion: scan applied migration: %w", err)
		}
		applied = append(applied, a)
	}

	return applied, rows.Err()
}

func (e *pgMigrateExecutor) RecordApplied(ctx context.Context, m *migrate.Migration) error {
	_, err := e.pgdb.Exec(ctx,
		fmt.Sprintf(`INSERT INTO %s (version, name, "group") VALUES ($1, $2, $3)`, migrationTableName),
		m.Version, m.Name, m.Group,
	)

	return err
}

func (e *pgMigrateExecutor) RemoveApplied(ctx context.Context, m *migrate.Migration) error {
	_, err := e.pgdb.Exec(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE version = $1 AND "group" = $2`, migrationTableName),
		m.Version, m.Group,
	)

	return err
}
