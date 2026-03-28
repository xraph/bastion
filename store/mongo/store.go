// Package mongo provides a MongoDB implementation of the Bastion
// composite store using grove ORM.
package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/xraph/bastion"
	"github.com/xraph/bastion/store"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/mongodriver"
)

// Collection name constants.
const (
	colRoutes       = "bastion_routes"
	colCBStates     = "bastion_cb_states"
	colHealthChecks = "bastion_health_checks"
	colAuditEvents  = "bastion_audit_events"
	colCacheEntries = "bastion_cache"
	colRateLimits   = "bastion_rate_limits"
)

// Compile-time interface check.
var _ store.Store = (*Store)(nil)

// Store implements store.Store using MongoDB via Grove ORM.
type Store struct {
	db  *grove.DB
	mdb *mongodriver.MongoDB
}

// New creates a new MongoDB store backed by Grove ORM.
func New(db *grove.DB) *Store {
	return &Store{
		db:  db,
		mdb: mongodriver.Unwrap(db),
	}
}

// Migrate creates indexes for all bastion collections.
func (s *Store) Migrate(ctx context.Context) error {
	indexes := migrationIndexes()

	for col, models := range indexes {
		if len(models) == 0 {
			continue
		}

		_, err := s.mdb.Collection(col).Indexes().CreateMany(ctx, models)
		if err != nil {
			return fmt.Errorf("bastion/mongo: migrate %s indexes: %w", col, err)
		}
	}

	return nil
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.db.Ping(ctx) }

// Close closes the database connection.
func (s *Store) Close() error { return s.db.Close() }

// --- RouteStore ---

func (s *Store) ListRoutes(ctx context.Context) ([]*bastion.Route, error) {
	opts := options.Find().SetSort(bson.D{{Key: "priority", Value: -1}})
	cursor, err := s.mdb.Collection(colRoutes).Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, fmt.Errorf("bastion/mongo: list routes: %w", err)
	}
	defer cursor.Close(ctx)

	var docs []routeDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}

	routes := make([]*bastion.Route, len(docs))
	for i, doc := range docs {
		routes[i] = doc.toRoute()
	}

	return routes, nil
}

func (s *Store) GetRoute(ctx context.Context, routeID string) (*bastion.Route, error) {
	var doc routeDoc
	err := s.mdb.Collection(colRoutes).FindOne(ctx, bson.M{"_id": routeID}).Decode(&doc)
	if isNoDocuments(err) {
		return nil, fmt.Errorf("route %q not found", routeID)
	}
	if err != nil {
		return nil, err
	}

	return doc.toRoute(), nil
}

func (s *Store) SaveRoute(ctx context.Context, route *bastion.Route) error {
	doc := newRouteDoc(route)
	opts := options.Replace().SetUpsert(true)
	_, err := s.mdb.Collection(colRoutes).ReplaceOne(ctx, bson.M{"_id": route.ID}, doc, opts)

	return err
}

func (s *Store) DeleteRoute(ctx context.Context, routeID string) error {
	_, err := s.mdb.Collection(colRoutes).DeleteOne(ctx, bson.M{"_id": routeID})

	return err
}

// --- CircuitBreakerStore ---

func (s *Store) GetState(ctx context.Context, targetID string) (*bastion.CircuitBreakerSnapshot, error) {
	var doc cbDoc
	err := s.mdb.Collection(colCBStates).FindOne(ctx, bson.M{"_id": targetID}).Decode(&doc)
	if isNoDocuments(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return doc.toSnapshot(), nil
}

func (s *Store) SaveState(ctx context.Context, snap *bastion.CircuitBreakerSnapshot) error {
	doc := newCBDoc(snap)
	opts := options.Replace().SetUpsert(true)
	_, err := s.mdb.Collection(colCBStates).ReplaceOne(ctx, bson.M{"_id": snap.TargetID}, doc, opts)

	return err
}

func (s *Store) ListStates(ctx context.Context) ([]*bastion.CircuitBreakerSnapshot, error) {
	cursor, err := s.mdb.Collection(colCBStates).Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []cbDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}

	states := make([]*bastion.CircuitBreakerSnapshot, len(docs))
	for i, doc := range docs {
		states[i] = doc.toSnapshot()
	}

	return states, nil
}

func (s *Store) DeleteState(ctx context.Context, targetID string) error {
	_, err := s.mdb.Collection(colCBStates).DeleteOne(ctx, bson.M{"_id": targetID})

	return err
}

// --- HealthStore ---

func (s *Store) RecordCheck(ctx context.Context, result *bastion.HealthCheckResult) error {
	doc := bson.M{
		"target_id":  result.TargetID,
		"target_url": result.TargetURL,
		"healthy":    result.Healthy,
		"latency_ns": result.Latency.Nanoseconds(),
		"error":      result.Error,
		"checked_at": result.Timestamp,
	}

	_, err := s.mdb.Collection(colHealthChecks).InsertOne(ctx, doc)

	return err
}

func (s *Store) GetHistory(ctx context.Context, targetID string, limit int) ([]bastion.HealthCheckResult, error) {
	if limit <= 0 {
		limit = 100
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "checked_at", Value: -1}}).
		SetLimit(int64(limit))

	cursor, err := s.mdb.Collection(colHealthChecks).Find(ctx, bson.M{"target_id": targetID}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []healthDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}

	results := make([]bastion.HealthCheckResult, len(docs))
	for i, doc := range docs {
		results[i] = bastion.HealthCheckResult{
			TargetID:  doc.TargetID,
			TargetURL: doc.TargetURL,
			Healthy:   doc.Healthy,
			Latency:   time.Duration(doc.LatencyNs),
			Error:     doc.Error,
			Timestamp: doc.CheckedAt,
		}
	}

	return results, nil
}

func (s *Store) GetLatestState(ctx context.Context, targetID string) (*bastion.HealthCheckResult, error) {
	opts := options.FindOne().SetSort(bson.D{{Key: "checked_at", Value: -1}})

	var doc healthDoc
	err := s.mdb.Collection(colHealthChecks).FindOne(ctx, bson.M{"target_id": targetID}, opts).Decode(&doc)
	if isNoDocuments(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &bastion.HealthCheckResult{
		TargetID:  doc.TargetID,
		TargetURL: doc.TargetURL,
		Healthy:   doc.Healthy,
		Latency:   time.Duration(doc.LatencyNs),
		Error:     doc.Error,
		Timestamp: doc.CheckedAt,
	}, nil
}

// --- CacheStore ---

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	var doc cacheDoc
	err := s.mdb.Collection(colCacheEntries).FindOne(ctx, bson.M{
		"_id":        key,
		"expires_at": bson.M{"$gt": time.Now()},
	}).Decode(&doc)
	if isNoDocuments(err) {
		return nil, fmt.Errorf("cache miss: %s", key)
	}
	if err != nil {
		return nil, err
	}

	return doc.Value, nil
}

func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	doc := cacheDoc{
		Key:       key,
		Value:     value,
		ExpiresAt: time.Now().Add(ttl),
	}
	opts := options.Replace().SetUpsert(true)
	_, err := s.mdb.Collection(colCacheEntries).ReplaceOne(ctx, bson.M{"_id": key}, doc, opts)

	return err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.mdb.Collection(colCacheEntries).DeleteOne(ctx, bson.M{"_id": key})

	return err
}

// --- RateLimitStore ---

func (s *Store) Allow(ctx context.Context, key string, limit float64, burst int, _ time.Duration) (bool, error) {
	now := time.Now()

	// Upsert the rate limit entry
	filter := bson.M{"_id": key}
	update := bson.M{
		"$setOnInsert": bson.M{
			"tokens":    float64(burst),
			"last_time": now,
		},
	}
	opts := options.UpdateOne().SetUpsert(true)
	_, err := s.mdb.Collection(colRateLimits).UpdateOne(ctx, filter, update, opts)
	if err != nil {
		return false, err
	}

	// Read current state
	var doc rateLimitDoc
	err = s.mdb.Collection(colRateLimits).FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		return false, err
	}

	// Calculate refilled tokens
	elapsed := now.Sub(doc.LastTime).Seconds()
	tokens := doc.Tokens + elapsed*limit
	if tokens > float64(burst) {
		tokens = float64(burst)
	}

	if tokens >= 1.0 {
		tokens--
		_, err = s.mdb.Collection(colRateLimits).UpdateOne(ctx, filter, bson.M{
			"$set": bson.M{"tokens": tokens, "last_time": now},
		})
		return true, err
	}

	// Update last_time and tokens even if not allowed (for refill tracking)
	_, _ = s.mdb.Collection(colRateLimits).UpdateOne(ctx, filter, bson.M{
		"$set": bson.M{"tokens": tokens, "last_time": now},
	})

	return false, nil
}

// --- AuditSink ---

func (s *Store) Write(ctx context.Context, event *bastion.AuditEvent) error {
	detail, _ := json.Marshal(event.Detail)
	doc := bson.M{
		"action":     string(event.Action),
		"actor":      event.Actor,
		"resource":   event.Resource,
		"detail":     string(detail),
		"result":     event.Result,
		"error":      event.Error,
		"created_at": event.Timestamp,
	}

	_, err := s.mdb.Collection(colAuditEvents).InsertOne(ctx, doc)

	return err
}

// --- Document Types ---

type routeDoc struct {
	ID             string    `bson:"_id"`
	Path           string    `bson:"path"`
	Methods        []string  `bson:"methods"`
	Targets        bson.Raw  `bson:"targets"`
	StripPrefix    bool      `bson:"strip_prefix"`
	AddPrefix      string    `bson:"add_prefix"`
	RewritePath    string    `bson:"rewrite_path"`
	Headers        bson.Raw  `bson:"headers"`
	Protocol       string    `bson:"protocol"`
	Source         string    `bson:"source"`
	ServiceName    string    `bson:"service_name"`
	Priority       int       `bson:"priority"`
	Version        int64     `bson:"version"`
	Enabled        bool      `bson:"enabled"`
	Retry          bson.Raw  `bson:"retry,omitempty"`
	Timeout        bson.Raw  `bson:"timeout,omitempty"`
	RateLimit      bson.Raw  `bson:"rate_limit,omitempty"`
	Auth           bson.Raw  `bson:"auth,omitempty"`
	CircuitBreaker bson.Raw  `bson:"circuit_breaker,omitempty"`
	Cache          bson.Raw  `bson:"cache,omitempty"`
	TrafficPolicy  bson.Raw  `bson:"traffic_policy,omitempty"`
	Transform      bson.Raw  `bson:"transform,omitempty"`
	Metadata       bson.Raw  `bson:"metadata,omitempty"`
	CreatedAt      time.Time `bson:"created_at"`
	UpdatedAt      time.Time `bson:"updated_at"`
}

func newRouteDoc(r *bastion.Route) routeDoc {
	targetsJSON, _ := json.Marshal(r.Targets)
	headersJSON, _ := json.Marshal(r.Headers)
	metadataJSON, _ := json.Marshal(r.Metadata)

	doc := routeDoc{
		ID:          r.ID,
		Path:        r.Path,
		Methods:     r.Methods,
		Targets:     mustBSONFromJSON(targetsJSON),
		StripPrefix: r.StripPrefix,
		AddPrefix:   r.AddPrefix,
		RewritePath: r.RewritePath,
		Headers:     mustBSONFromJSON(headersJSON),
		Protocol:    string(r.Protocol),
		Source:      string(r.Source),
		ServiceName: r.ServiceName,
		Priority:    r.Priority,
		Version:     r.Version,
		Enabled:     r.Enabled,
		Metadata:    mustBSONFromJSON(metadataJSON),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}

	if r.Retry != nil {
		b, _ := json.Marshal(r.Retry)
		doc.Retry = mustBSONFromJSON(b)
	}
	if r.Timeout != nil {
		b, _ := json.Marshal(r.Timeout)
		doc.Timeout = mustBSONFromJSON(b)
	}
	if r.RateLimit != nil {
		b, _ := json.Marshal(r.RateLimit)
		doc.RateLimit = mustBSONFromJSON(b)
	}
	if r.Auth != nil {
		b, _ := json.Marshal(r.Auth)
		doc.Auth = mustBSONFromJSON(b)
	}
	if r.CircuitBreaker != nil {
		b, _ := json.Marshal(r.CircuitBreaker)
		doc.CircuitBreaker = mustBSONFromJSON(b)
	}
	if r.Cache != nil {
		b, _ := json.Marshal(r.Cache)
		doc.Cache = mustBSONFromJSON(b)
	}
	if r.TrafficPolicy != nil {
		b, _ := json.Marshal(r.TrafficPolicy)
		doc.TrafficPolicy = mustBSONFromJSON(b)
	}
	if r.Transform != nil {
		b, _ := json.Marshal(r.Transform)
		doc.Transform = mustBSONFromJSON(b)
	}

	return doc
}

func (d routeDoc) toRoute() *bastion.Route {
	r := &bastion.Route{
		ID:          d.ID,
		Path:        d.Path,
		Methods:     d.Methods,
		StripPrefix: d.StripPrefix,
		AddPrefix:   d.AddPrefix,
		RewritePath: d.RewritePath,
		Protocol:    bastion.RouteProtocol(d.Protocol),
		Source:      bastion.RouteSource(d.Source),
		ServiceName: d.ServiceName,
		Priority:    d.Priority,
		Version:     d.Version,
		Enabled:     d.Enabled,
		CreatedAt:   d.CreatedAt,
		UpdatedAt:   d.UpdatedAt,
	}

	unmarshalBSONRaw(d.Targets, &r.Targets)
	unmarshalBSONRaw(d.Headers, &r.Headers)
	unmarshalBSONRaw(d.Metadata, &r.Metadata)

	if len(d.Retry) > 0 {
		r.Retry = &bastion.RetryConfig{}
		unmarshalBSONRaw(d.Retry, r.Retry)
	}
	if len(d.Timeout) > 0 {
		r.Timeout = &bastion.TimeoutConfig{}
		unmarshalBSONRaw(d.Timeout, r.Timeout)
	}
	if len(d.RateLimit) > 0 {
		r.RateLimit = &bastion.RateLimitConfig{}
		unmarshalBSONRaw(d.RateLimit, r.RateLimit)
	}
	if len(d.Auth) > 0 {
		r.Auth = &bastion.RouteAuthConfig{}
		unmarshalBSONRaw(d.Auth, r.Auth)
	}
	if len(d.CircuitBreaker) > 0 {
		r.CircuitBreaker = &bastion.CBConfig{}
		unmarshalBSONRaw(d.CircuitBreaker, r.CircuitBreaker)
	}
	if len(d.Cache) > 0 {
		r.Cache = &bastion.RouteCacheConfig{}
		unmarshalBSONRaw(d.Cache, r.Cache)
	}
	if len(d.TrafficPolicy) > 0 {
		r.TrafficPolicy = &bastion.TrafficPolicy{}
		unmarshalBSONRaw(d.TrafficPolicy, r.TrafficPolicy)
	}
	if len(d.Transform) > 0 {
		r.Transform = &bastion.TransformConfig{}
		unmarshalBSONRaw(d.Transform, r.Transform)
	}

	return r
}

type cbDoc struct {
	TargetID        string    `bson:"_id"`
	State           string    `bson:"state"`
	FailureCount    int       `bson:"failure_count"`
	SuccessCount    int       `bson:"success_count"`
	LastFailure     time.Time `bson:"last_failure"`
	LastStateChange time.Time `bson:"last_state_change"`
	UpdatedAt       time.Time `bson:"updated_at"`
}

func newCBDoc(snap *bastion.CircuitBreakerSnapshot) cbDoc {
	return cbDoc{
		TargetID:        snap.TargetID,
		State:           string(snap.State),
		FailureCount:    snap.FailureCount,
		SuccessCount:    snap.SuccessCount,
		LastFailure:     snap.LastFailure,
		LastStateChange: snap.LastStateChange,
		UpdatedAt:       snap.UpdatedAt,
	}
}

func (d cbDoc) toSnapshot() *bastion.CircuitBreakerSnapshot {
	return &bastion.CircuitBreakerSnapshot{
		TargetID:        d.TargetID,
		State:           bastion.CircuitState(d.State),
		FailureCount:    d.FailureCount,
		SuccessCount:    d.SuccessCount,
		LastFailure:     d.LastFailure,
		LastStateChange: d.LastStateChange,
		UpdatedAt:       d.UpdatedAt,
	}
}

type healthDoc struct {
	TargetID  string    `bson:"target_id"`
	TargetURL string    `bson:"target_url"`
	Healthy   bool      `bson:"healthy"`
	LatencyNs int64     `bson:"latency_ns"`
	Error     string    `bson:"error"`
	CheckedAt time.Time `bson:"checked_at"`
}

type cacheDoc struct {
	Key       string    `bson:"_id"`
	Value     []byte    `bson:"value"`
	ExpiresAt time.Time `bson:"expires_at"`
}

type rateLimitDoc struct {
	Key      string    `bson:"_id"`
	Tokens   float64   `bson:"tokens"`
	LastTime time.Time `bson:"last_time"`
}

// --- Helpers ---

func isNoDocuments(err error) bool {
	return errors.Is(err, mongo.ErrNoDocuments)
}

func mustBSONFromJSON(data []byte) bson.Raw {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}

	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}

	raw, err := bson.Marshal(bson.M{"v": v})
	if err != nil {
		return nil
	}

	// Extract the "v" field value
	doc := bson.Raw(raw)
	val := doc.Lookup("v")
	if val.Value == nil {
		return nil
	}

	return val.Value
}

func unmarshalBSONRaw(raw bson.Raw, dst any) {
	if len(raw) == 0 {
		return
	}

	// Convert BSON → JSON → Go struct for compatibility
	data, err := bson.MarshalExtJSON(bson.M{"v": raw}, false, false)
	if err != nil {
		return
	}

	var wrapper struct {
		V json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return
	}

	json.Unmarshal(wrapper.V, dst)
}

// migrationIndexes returns the index definitions for all bastion collections.
func migrationIndexes() map[string][]mongo.IndexModel {
	return map[string][]mongo.IndexModel{
		colRoutes: {
			{Keys: bson.D{{Key: "path", Value: 1}}},
			{Keys: bson.D{{Key: "priority", Value: -1}}},
			{Keys: bson.D{{Key: "service_name", Value: 1}}},
			{Keys: bson.D{{Key: "enabled", Value: 1}}},
		},
		colCBStates: {
			{Keys: bson.D{{Key: "state", Value: 1}}},
			{Keys: bson.D{{Key: "updated_at", Value: 1}}},
		},
		colHealthChecks: {
			{Keys: bson.D{{Key: "target_id", Value: 1}, {Key: "checked_at", Value: -1}}},
			{Keys: bson.D{{Key: "checked_at", Value: -1}}},
		},
		colAuditEvents: {
			{Keys: bson.D{{Key: "action", Value: 1}}},
			{Keys: bson.D{{Key: "created_at", Value: -1}}},
		},
		colCacheEntries: {
			{Keys: bson.D{{Key: "expires_at", Value: 1}}},
		},
		colRateLimits: {
			{Keys: bson.D{{Key: "last_time", Value: 1}}},
		},
	}
}
