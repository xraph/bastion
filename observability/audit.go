package observability

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/xraph/forge"
)

// AuditConfig configures audit logging.
type AuditConfig struct {
	// Enabled enables audit logging.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// BufferSize is the channel buffer for async audit events.
	BufferSize int `json:"bufferSize" yaml:"buffer_size"`
}

// AuditAction represents the type of admin action taken.
type AuditAction string

const (
	AuditRouteCreated    AuditAction = "route.created"
	AuditRouteUpdated    AuditAction = "route.updated"
	AuditRouteDeleted    AuditAction = "route.deleted"
	AuditRouteToggled    AuditAction = "route.toggled"
	AuditConfigChanged   AuditAction = "config.changed"
	AuditDiscoveryForced AuditAction = "discovery.forced"
	AuditCircuitReset    AuditAction = "circuit.reset"
	AuditCacheCleared    AuditAction = "cache.cleared"
)

// AuditEvent represents an auditable action in the gateway.
type AuditEvent struct {
	Timestamp time.Time      `json:"timestamp"`
	Action    AuditAction    `json:"action"`
	Actor     string         `json:"actor"`
	Resource  string         `json:"resource"`
	Detail    map[string]any `json:"detail,omitempty"`
	Result    string         `json:"result"` // "success" or "failure"
	Error     string         `json:"error,omitempty"`
}

// AuditSink is the interface for consuming audit events.
type AuditSink interface {
	// Write persists an audit event.
	Write(ctx context.Context, event *AuditEvent) error
}

// AuditLogger captures and dispatches administrative audit events.
type AuditLogger struct {
	config AuditConfig
	logger forge.Logger
	sinks  []AuditSink
	events chan *AuditEvent
	wg     sync.WaitGroup
	stopCh chan struct{}
}

// NewAuditLogger creates a new audit logger.
func NewAuditLogger(config AuditConfig, logger forge.Logger) *AuditLogger {
	if config.BufferSize <= 0 {
		config.BufferSize = 256
	}

	return &AuditLogger{
		config: config,
		logger: logger,
		events: make(chan *AuditEvent, config.BufferSize),
		stopCh: make(chan struct{}),
	}
}

// AddSink adds an audit sink for consuming events.
func (al *AuditLogger) AddSink(sink AuditSink) {
	al.sinks = append(al.sinks, sink)
}

// Start begins processing audit events asynchronously.
func (al *AuditLogger) Start() {
	if !al.config.Enabled {
		return
	}

	al.wg.Add(1)

	go al.loop()
}

// Stop flushes remaining events and stops the audit logger.
func (al *AuditLogger) Stop() {
	close(al.stopCh)
	al.wg.Wait()
}

// Record emits an audit event. Non-blocking; drops events if buffer is full.
func (al *AuditLogger) Record(action AuditAction, actor, resource string, detail map[string]any, err error) {
	if !al.config.Enabled {
		return
	}

	event := &AuditEvent{
		Timestamp: time.Now(),
		Action:    action,
		Actor:     actor,
		Resource:  resource,
		Detail:    detail,
		Result:    "success",
	}

	if err != nil {
		event.Result = "failure"
		event.Error = err.Error()
	}

	select {
	case al.events <- event:
	default:
		al.logger.Warn("audit log buffer full, dropping event",
			forge.F("action", string(action)),
			forge.F("resource", resource),
		)
	}
}

func (al *AuditLogger) loop() {
	defer al.wg.Done()

	for {
		select {
		case event := <-al.events:
			al.dispatch(event)
		case <-al.stopCh:
			// Drain remaining events
			for {
				select {
				case event := <-al.events:
					al.dispatch(event)
				default:
					return
				}
			}
		}
	}
}

func (al *AuditLogger) dispatch(event *AuditEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Always log to the main logger
	al.logger.Info("audit",
		forge.F("action", string(event.Action)),
		forge.F("actor", event.Actor),
		forge.F("resource", event.Resource),
		forge.F("result", event.Result),
	)

	// Dispatch to external sinks
	for _, sink := range al.sinks {
		if err := sink.Write(ctx, event); err != nil {
			al.logger.Warn("audit sink write failed",
				forge.F("error", err),
			)
		}
	}
}

// LogAuditSink writes audit events as structured log entries.
type LogAuditSink struct {
	logger forge.Logger
}

// NewLogAuditSink creates a sink that writes to a forge.Logger.
func NewLogAuditSink(logger forge.Logger) *LogAuditSink {
	return &LogAuditSink{logger: logger}
}

// Write logs the audit event.
func (s *LogAuditSink) Write(_ context.Context, event *AuditEvent) error {
	data, _ := json.Marshal(event)
	s.logger.Info("audit_event", forge.F("event", string(data)))

	return nil
}
