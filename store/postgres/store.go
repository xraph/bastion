// Package postgres provides a PostgreSQL implementation of the Bastion
// composite store using grove ORM with programmatic migrations.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/store"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/grove/migrate"
)

// Compile-time interface check.
var _ store.Store = (*Store)(nil)

// Store is a PostgreSQL implementation of the composite Bastion store.
type Store struct {
	db   *grove.DB
	pgdb *pgdriver.PgDB
}

// New creates a new PostgreSQL store.
func New(db *grove.DB) *Store {
	return &Store{
		db:   db,
		pgdb: pgdriver.Unwrap(db),
	}
}

// Migrate runs programmatic migrations via the grove orchestrator.
func (s *Store) Migrate(ctx context.Context) error {
	executor := &pgMigrateExecutor{pgdb: s.pgdb}

	orch := migrate.NewOrchestrator(executor, Migrations)
	if _, err := orch.Migrate(ctx); err != nil {
		return fmt.Errorf("bastion: migration failed: %w", err)
	}

	return nil
}

// Ping verifies the database connection.
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

// Close closes the database connection.
func (s *Store) Close() error { return s.db.Close() }

// --- RouteStore ---

func (s *Store) ListRoutes(ctx context.Context) ([]*bastion.Route, error) {
	rows, err := s.pgdb.Query(ctx,
		`SELECT id, path, methods, targets, strip_prefix, add_prefix, rewrite_path,
		        headers, protocol, source, service_name, priority, version, enabled,
		        retry, timeout, rate_limit, auth, circuit_breaker, cache,
		        traffic_policy, transform, metadata, created_at, updated_at
		 FROM bastion_routes ORDER BY priority DESC`)
	if err != nil {
		return nil, fmt.Errorf("bastion/pg: list routes: %w", err)
	}
	defer rows.Close()

	var routes []*bastion.Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}

	return routes, rows.Err()
}

func (s *Store) GetRoute(ctx context.Context, routeID string) (*bastion.Route, error) {
	row := s.pgdb.QueryRow(ctx,
		`SELECT id, path, methods, targets, strip_prefix, add_prefix, rewrite_path,
		        headers, protocol, source, service_name, priority, version, enabled,
		        retry, timeout, rate_limit, auth, circuit_breaker, cache,
		        traffic_policy, transform, metadata, created_at, updated_at
		 FROM bastion_routes WHERE id = $1`, routeID)

	r, err := scanRouteRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("route %q not found", routeID)
	}

	return r, err
}

func (s *Store) SaveRoute(ctx context.Context, route *bastion.Route) error {
	methods, _ := json.Marshal(route.Methods)
	targets, _ := json.Marshal(route.Targets)
	headers, _ := json.Marshal(route.Headers)
	retry, _ := json.Marshal(route.Retry)
	timeout, _ := json.Marshal(route.Timeout)
	rateLimit, _ := json.Marshal(route.RateLimit)
	auth, _ := json.Marshal(route.Auth)
	cb, _ := json.Marshal(route.CircuitBreaker)
	cache, _ := json.Marshal(route.Cache)
	tp, _ := json.Marshal(route.TrafficPolicy)
	transform, _ := json.Marshal(route.Transform)
	metadata, _ := json.Marshal(route.Metadata)

	_, err := s.pgdb.Exec(ctx, `
		INSERT INTO bastion_routes (id, path, methods, targets, strip_prefix, add_prefix,
		    rewrite_path, headers, protocol, source, service_name, priority, version,
		    enabled, retry, timeout, rate_limit, auth, circuit_breaker, cache,
		    traffic_policy, transform, metadata, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
		ON CONFLICT (id) DO UPDATE SET
		    path=$2, methods=$3, targets=$4, strip_prefix=$5, add_prefix=$6,
		    rewrite_path=$7, headers=$8, protocol=$9, source=$10, service_name=$11,
		    priority=$12, version=$13, enabled=$14, retry=$15, timeout=$16,
		    rate_limit=$17, auth=$18, circuit_breaker=$19, cache=$20,
		    traffic_policy=$21, transform=$22, metadata=$23, updated_at=$25`,
		route.ID, route.Path, methods, targets, route.StripPrefix, route.AddPrefix,
		route.RewritePath, headers, string(route.Protocol), string(route.Source),
		route.ServiceName, route.Priority, route.Version, route.Enabled,
		retry, timeout, rateLimit, auth, cb, cache, tp, transform, metadata,
		route.CreatedAt, route.UpdatedAt)

	return err
}

func (s *Store) DeleteRoute(ctx context.Context, routeID string) error {
	_, err := s.pgdb.Exec(ctx, `DELETE FROM bastion_routes WHERE id = $1`, routeID)

	return err
}

// --- CircuitBreakerStore ---

func (s *Store) GetState(ctx context.Context, targetID string) (*bastion.CircuitBreakerSnapshot, error) {
	row := s.pgdb.QueryRow(ctx,
		`SELECT target_id, state, failure_count, success_count, last_failure, last_state_change, updated_at
		 FROM bastion_circuit_breaker_states WHERE target_id = $1`, targetID)

	snap := &bastion.CircuitBreakerSnapshot{}
	err := row.Scan(&snap.TargetID, &snap.State, &snap.FailureCount, &snap.SuccessCount,
		&snap.LastFailure, &snap.LastStateChange, &snap.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return snap, nil
}

func (s *Store) SaveState(ctx context.Context, snap *bastion.CircuitBreakerSnapshot) error {
	_, err := s.pgdb.Exec(ctx, `
		INSERT INTO bastion_circuit_breaker_states (target_id, state, failure_count, success_count,
		    last_failure, last_state_change, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (target_id) DO UPDATE SET
		    state=$2, failure_count=$3, success_count=$4,
		    last_failure=$5, last_state_change=$6, updated_at=$7`,
		snap.TargetID, string(snap.State), snap.FailureCount, snap.SuccessCount,
		snap.LastFailure, snap.LastStateChange, snap.UpdatedAt)

	return err
}

func (s *Store) ListStates(ctx context.Context) ([]*bastion.CircuitBreakerSnapshot, error) {
	rows, err := s.pgdb.Query(ctx,
		`SELECT target_id, state, failure_count, success_count, last_failure, last_state_change, updated_at
		 FROM bastion_circuit_breaker_states`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []*bastion.CircuitBreakerSnapshot
	for rows.Next() {
		snap := &bastion.CircuitBreakerSnapshot{}
		if err := rows.Scan(&snap.TargetID, &snap.State, &snap.FailureCount, &snap.SuccessCount,
			&snap.LastFailure, &snap.LastStateChange, &snap.UpdatedAt); err != nil {
			return nil, err
		}
		states = append(states, snap)
	}

	return states, rows.Err()
}

func (s *Store) DeleteState(ctx context.Context, targetID string) error {
	_, err := s.pgdb.Exec(ctx, `DELETE FROM bastion_circuit_breaker_states WHERE target_id = $1`, targetID)

	return err
}

// --- HealthStore ---

func (s *Store) RecordCheck(ctx context.Context, result *bastion.HealthCheckResult) error {
	_, err := s.pgdb.Exec(ctx,
		`INSERT INTO bastion_health_checks (target_id, target_url, healthy, latency_ns, error, checked_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		result.TargetID, result.TargetURL, result.Healthy,
		result.Latency.Nanoseconds(), result.Error, result.Timestamp)

	return err
}

func (s *Store) GetHistory(ctx context.Context, targetID string, limit int) ([]bastion.HealthCheckResult, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.pgdb.Query(ctx,
		`SELECT target_id, target_url, healthy, latency_ns, error, checked_at
		 FROM bastion_health_checks WHERE target_id = $1
		 ORDER BY checked_at DESC LIMIT $2`, targetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []bastion.HealthCheckResult
	for rows.Next() {
		var r bastion.HealthCheckResult
		var latencyNs int64
		if err := rows.Scan(&r.TargetID, &r.TargetURL, &r.Healthy, &latencyNs, &r.Error, &r.Timestamp); err != nil {
			return nil, err
		}
		r.Latency = time.Duration(latencyNs)
		results = append(results, r)
	}

	return results, rows.Err()
}

func (s *Store) GetLatestState(ctx context.Context, targetID string) (*bastion.HealthCheckResult, error) {
	row := s.pgdb.QueryRow(ctx,
		`SELECT target_id, target_url, healthy, latency_ns, error, checked_at
		 FROM bastion_health_checks WHERE target_id = $1
		 ORDER BY checked_at DESC LIMIT 1`, targetID)

	var r bastion.HealthCheckResult
	var latencyNs int64
	err := row.Scan(&r.TargetID, &r.TargetURL, &r.Healthy, &latencyNs, &r.Error, &r.Timestamp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	r.Latency = time.Duration(latencyNs)

	return &r, nil
}

// --- CacheStore ---

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	row := s.pgdb.QueryRow(ctx,
		`SELECT value FROM bastion_cache_entries WHERE key = $1 AND expires_at > NOW()`, key)

	var value []byte
	err := row.Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("cache miss: %s", key)
	}

	return value, err
}

func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	expiresAt := time.Now().Add(ttl)
	_, err := s.pgdb.Exec(ctx, `
		INSERT INTO bastion_cache_entries (key, value, expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value=$2, expires_at=$3`,
		key, value, expiresAt)

	return err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.pgdb.Exec(ctx, `DELETE FROM bastion_cache_entries WHERE key = $1`, key)

	return err
}

// --- RateLimitStore ---

func (s *Store) Allow(ctx context.Context, key string, limit float64, burst int, _ time.Duration) (bool, error) {
	now := time.Now()

	// Atomic upsert with token refill
	row := s.pgdb.QueryRow(ctx, `
		INSERT INTO bastion_rate_limits (key, tokens, last_time)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET
		    tokens = LEAST(
		        $2::double precision,
		        bastion_rate_limits.tokens +
		            EXTRACT(EPOCH FROM ($3::timestamptz - bastion_rate_limits.last_time)) * $4::double precision
		    ),
		    last_time = $3
		RETURNING tokens`,
		key, float64(burst), now, limit)

	var tokens float64
	if err := row.Scan(&tokens); err != nil {
		return false, err
	}

	if tokens >= 1.0 {
		_, err := s.pgdb.Exec(ctx,
			`UPDATE bastion_rate_limits SET tokens = tokens - 1 WHERE key = $1`, key)
		return true, err
	}

	return false, nil
}

// --- AuditSink ---

func (s *Store) Write(ctx context.Context, event *bastion.AuditEvent) error {
	detail, _ := json.Marshal(event.Detail)
	_, err := s.pgdb.Exec(ctx,
		`INSERT INTO bastion_audit_events (action, actor, resource, detail, result, error, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		string(event.Action), event.Actor, event.Resource, detail,
		event.Result, event.Error, event.Timestamp)

	return err
}

// --- Helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanRoute(rows scannable) (*bastion.Route, error) {
	r := &bastion.Route{}
	var methods, targets, headers, retry, timeout, rateLimit, auth, cb, cache, tp, transform, metadata []byte

	err := rows.Scan(&r.ID, &r.Path, &methods, &targets, &r.StripPrefix, &r.AddPrefix,
		&r.RewritePath, &headers, &r.Protocol, &r.Source, &r.ServiceName,
		&r.Priority, &r.Version, &r.Enabled,
		&retry, &timeout, &rateLimit, &auth, &cb, &cache, &tp, &transform, &metadata,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}

	json.Unmarshal(methods, &r.Methods)
	json.Unmarshal(targets, &r.Targets)
	json.Unmarshal(headers, &r.Headers)
	json.Unmarshal(metadata, &r.Metadata)
	if len(retry) > 0 {
		r.Retry = &bastion.RetryConfig{}
		json.Unmarshal(retry, r.Retry)
	}
	if len(timeout) > 0 {
		r.Timeout = &bastion.TimeoutConfig{}
		json.Unmarshal(timeout, r.Timeout)
	}
	if len(rateLimit) > 0 {
		r.RateLimit = &bastion.RateLimitConfig{}
		json.Unmarshal(rateLimit, r.RateLimit)
	}
	if len(auth) > 0 {
		r.Auth = &bastion.RouteAuthConfig{}
		json.Unmarshal(auth, r.Auth)
	}
	if len(cb) > 0 {
		r.CircuitBreaker = &bastion.CBConfig{}
		json.Unmarshal(cb, r.CircuitBreaker)
	}
	if len(cache) > 0 {
		r.Cache = &bastion.RouteCacheConfig{}
		json.Unmarshal(cache, r.Cache)
	}
	if len(tp) > 0 {
		r.TrafficPolicy = &bastion.TrafficPolicy{}
		json.Unmarshal(tp, r.TrafficPolicy)
	}
	if len(transform) > 0 {
		r.Transform = &bastion.TransformConfig{}
		json.Unmarshal(transform, r.Transform)
	}

	return r, nil
}

func scanRouteRow(row scannable) (*bastion.Route, error) {
	return scanRoute(row)
}
