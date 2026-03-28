// Package sqlite provides a SQLite implementation of the Bastion
// composite store using grove ORM with programmatic migrations.
package sqlite

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
	"github.com/xraph/grove/drivers/sqlitedriver"
	"github.com/xraph/grove/migrate"
)

// Compile-time interface check.
var _ store.Store = (*Store)(nil)

// Store is a SQLite implementation of the composite Bastion store.
type Store struct {
	db  *grove.DB
	sdb *sqlitedriver.SqliteDB
}

// New creates a new SQLite store.
func New(db *grove.DB) *Store {
	return &Store{
		db:  db,
		sdb: sqlitedriver.Unwrap(db),
	}
}

// Migrate runs programmatic migrations via the grove orchestrator.
func (s *Store) Migrate(ctx context.Context) error {
	executor, err := migrate.NewExecutorFor(s.sdb)
	if err != nil {
		return fmt.Errorf("bastion/sqlite: create migration executor: %w", err)
	}

	orch := migrate.NewOrchestrator(executor, Migrations)
	if _, err := orch.Migrate(ctx); err != nil {
		return fmt.Errorf("bastion/sqlite: migration failed: %w", err)
	}

	return nil
}

// Ping verifies the database connection.
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

// Close closes the database connection.
func (s *Store) Close() error { return s.db.Close() }

// --- RouteStore ---

func (s *Store) ListRoutes(ctx context.Context) ([]*bastion.Route, error) {
	rows, err := s.sdb.Query(ctx,
		`SELECT id, path, methods, targets, strip_prefix, add_prefix, rewrite_path,
		        headers, protocol, source, service_name, priority, version, enabled,
		        retry, timeout, rate_limit, auth, circuit_breaker, cache,
		        traffic_policy, transform, metadata, created_at, updated_at
		 FROM bastion_routes ORDER BY priority DESC`)
	if err != nil {
		return nil, fmt.Errorf("bastion/sqlite: list routes: %w", err)
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
	row := s.sdb.QueryRow(ctx,
		`SELECT id, path, methods, targets, strip_prefix, add_prefix, rewrite_path,
		        headers, protocol, source, service_name, priority, version, enabled,
		        retry, timeout, rate_limit, auth, circuit_breaker, cache,
		        traffic_policy, transform, metadata, created_at, updated_at
		 FROM bastion_routes WHERE id = ?`, routeID)

	r, err := scanRoute(row)
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

	_, err := s.sdb.Exec(ctx, `
		INSERT INTO bastion_routes (id, path, methods, targets, strip_prefix, add_prefix,
		    rewrite_path, headers, protocol, source, service_name, priority, version,
		    enabled, retry, timeout, rate_limit, auth, circuit_breaker, cache,
		    traffic_policy, transform, metadata, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (id) DO UPDATE SET
		    path=excluded.path, methods=excluded.methods, targets=excluded.targets,
		    strip_prefix=excluded.strip_prefix, add_prefix=excluded.add_prefix,
		    rewrite_path=excluded.rewrite_path, headers=excluded.headers,
		    protocol=excluded.protocol, source=excluded.source, service_name=excluded.service_name,
		    priority=excluded.priority, version=excluded.version, enabled=excluded.enabled,
		    retry=excluded.retry, timeout=excluded.timeout, rate_limit=excluded.rate_limit,
		    auth=excluded.auth, circuit_breaker=excluded.circuit_breaker, cache=excluded.cache,
		    traffic_policy=excluded.traffic_policy, transform=excluded.transform,
		    metadata=excluded.metadata, updated_at=excluded.updated_at`,
		route.ID, route.Path, string(methods), string(targets), route.StripPrefix, route.AddPrefix,
		route.RewritePath, string(headers), string(route.Protocol), string(route.Source),
		route.ServiceName, route.Priority, route.Version, route.Enabled,
		string(retry), string(timeout), string(rateLimit), string(auth), string(cb), string(cache),
		string(tp), string(transform), string(metadata),
		route.CreatedAt, route.UpdatedAt)

	return err
}

func (s *Store) DeleteRoute(ctx context.Context, routeID string) error {
	_, err := s.sdb.Exec(ctx, `DELETE FROM bastion_routes WHERE id = ?`, routeID)

	return err
}

// --- CircuitBreakerStore ---

func (s *Store) GetState(ctx context.Context, targetID string) (*bastion.CircuitBreakerSnapshot, error) {
	row := s.sdb.QueryRow(ctx,
		`SELECT target_id, state, failure_count, success_count, last_failure, last_state_change, updated_at
		 FROM bastion_circuit_breaker_states WHERE target_id = ?`, targetID)

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
	_, err := s.sdb.Exec(ctx, `
		INSERT INTO bastion_circuit_breaker_states (target_id, state, failure_count, success_count,
		    last_failure, last_state_change, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (target_id) DO UPDATE SET
		    state=excluded.state, failure_count=excluded.failure_count,
		    success_count=excluded.success_count, last_failure=excluded.last_failure,
		    last_state_change=excluded.last_state_change, updated_at=excluded.updated_at`,
		snap.TargetID, string(snap.State), snap.FailureCount, snap.SuccessCount,
		snap.LastFailure, snap.LastStateChange, snap.UpdatedAt)

	return err
}

func (s *Store) ListStates(ctx context.Context) ([]*bastion.CircuitBreakerSnapshot, error) {
	rows, err := s.sdb.Query(ctx,
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
	_, err := s.sdb.Exec(ctx, `DELETE FROM bastion_circuit_breaker_states WHERE target_id = ?`, targetID)

	return err
}

// --- HealthStore ---

func (s *Store) RecordCheck(ctx context.Context, result *bastion.HealthCheckResult) error {
	_, err := s.sdb.Exec(ctx,
		`INSERT INTO bastion_health_checks (target_id, target_url, healthy, latency_ns, error, checked_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		result.TargetID, result.TargetURL, result.Healthy,
		result.Latency.Nanoseconds(), result.Error, result.Timestamp)

	return err
}

func (s *Store) GetHistory(ctx context.Context, targetID string, limit int) ([]bastion.HealthCheckResult, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.sdb.Query(ctx,
		`SELECT target_id, target_url, healthy, latency_ns, error, checked_at
		 FROM bastion_health_checks WHERE target_id = ?
		 ORDER BY checked_at DESC LIMIT ?`, targetID, limit)
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
	row := s.sdb.QueryRow(ctx,
		`SELECT target_id, target_url, healthy, latency_ns, error, checked_at
		 FROM bastion_health_checks WHERE target_id = ?
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
	row := s.sdb.QueryRow(ctx,
		`SELECT value FROM bastion_cache_entries WHERE key = ? AND expires_at > datetime('now')`, key)

	var value []byte
	err := row.Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("cache miss: %s", key)
	}

	return value, err
}

func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	expiresAt := time.Now().Add(ttl)
	_, err := s.sdb.Exec(ctx, `
		INSERT INTO bastion_cache_entries (key, value, expires_at)
		VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value=excluded.value, expires_at=excluded.expires_at`,
		key, value, expiresAt)

	return err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.sdb.Exec(ctx, `DELETE FROM bastion_cache_entries WHERE key = ?`, key)

	return err
}

// --- RateLimitStore ---

func (s *Store) Allow(ctx context.Context, key string, limit float64, burst int, _ time.Duration) (bool, error) {
	now := time.Now()

	// Upsert with token refill
	_, err := s.sdb.Exec(ctx, `
		INSERT INTO bastion_rate_limits (key, tokens, last_time)
		VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
		    tokens = MIN(
		        ?,
		        bastion_rate_limits.tokens +
		            (julianday(?) - julianday(bastion_rate_limits.last_time)) * 86400.0 * ?
		    ),
		    last_time = ?`,
		key, float64(burst), now,
		float64(burst), now, limit, now)
	if err != nil {
		return false, err
	}

	// Check and consume
	row := s.sdb.QueryRow(ctx,
		`SELECT tokens FROM bastion_rate_limits WHERE key = ?`, key)

	var tokens float64
	if err := row.Scan(&tokens); err != nil {
		return false, err
	}

	if tokens >= 1.0 {
		_, err := s.sdb.Exec(ctx,
			`UPDATE bastion_rate_limits SET tokens = tokens - 1 WHERE key = ?`, key)
		return true, err
	}

	return false, nil
}

// --- AuditSink ---

func (s *Store) Write(ctx context.Context, event *bastion.AuditEvent) error {
	detail, _ := json.Marshal(event.Detail)
	_, err := s.sdb.Exec(ctx,
		`INSERT INTO bastion_audit_events (action, actor, resource, detail, result, error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(event.Action), event.Actor, event.Resource, string(detail),
		event.Result, event.Error, event.Timestamp)

	return err
}

// --- Helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanRoute(row scannable) (*bastion.Route, error) {
	r := &bastion.Route{}
	var methods, targets, headers, retry, timeout, rateLimit, auth, cb, cache, tp, transform, metadata string

	err := row.Scan(&r.ID, &r.Path, &methods, &targets, &r.StripPrefix, &r.AddPrefix,
		&r.RewritePath, &headers, &r.Protocol, &r.Source, &r.ServiceName,
		&r.Priority, &r.Version, &r.Enabled,
		&retry, &timeout, &rateLimit, &auth, &cb, &cache, &tp, &transform, &metadata,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}

	json.Unmarshal([]byte(methods), &r.Methods)
	json.Unmarshal([]byte(targets), &r.Targets)
	json.Unmarshal([]byte(headers), &r.Headers)
	json.Unmarshal([]byte(metadata), &r.Metadata)
	if retry != "" && retry != "null" {
		r.Retry = &bastion.RetryConfig{}
		json.Unmarshal([]byte(retry), r.Retry)
	}
	if timeout != "" && timeout != "null" {
		r.Timeout = &bastion.TimeoutConfig{}
		json.Unmarshal([]byte(timeout), r.Timeout)
	}
	if rateLimit != "" && rateLimit != "null" {
		r.RateLimit = &bastion.RateLimitConfig{}
		json.Unmarshal([]byte(rateLimit), r.RateLimit)
	}
	if auth != "" && auth != "null" {
		r.Auth = &bastion.RouteAuthConfig{}
		json.Unmarshal([]byte(auth), r.Auth)
	}
	if cb != "" && cb != "null" {
		r.CircuitBreaker = &bastion.CBConfig{}
		json.Unmarshal([]byte(cb), r.CircuitBreaker)
	}
	if cache != "" && cache != "null" {
		r.Cache = &bastion.RouteCacheConfig{}
		json.Unmarshal([]byte(cache), r.Cache)
	}
	if tp != "" && tp != "null" {
		r.TrafficPolicy = &bastion.TrafficPolicy{}
		json.Unmarshal([]byte(tp), r.TrafficPolicy)
	}
	if transform != "" && transform != "null" {
		r.Transform = &bastion.TransformConfig{}
		json.Unmarshal([]byte(transform), r.Transform)
	}

	return r, nil
}
