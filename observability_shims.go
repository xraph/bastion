package bastion

import (
	"github.com/xraph/forge"

	"github.com/xraph/bastion/observability"
)

// Backward-compatibility aliases for types that moved to the observability
// subpackage. Existing code that references bastion.GatewayMetrics,
// bastion.AccessLogger, bastion.AuditLogger, or bastion.LogAuditSink
// will continue to compile without changes.

// GatewayMetrics is an alias for observability.Metrics.
//
// Deprecated: Use observability.Metrics directly.
type GatewayMetrics = observability.Metrics

// AccessLogger is an alias for observability.AccessLogger.
//
// Deprecated: Use observability.AccessLogger directly.
type AccessLogger = observability.AccessLogger

// AuditLogger is an alias for observability.AuditLogger.
//
// Deprecated: Use observability.AuditLogger directly.
type AuditLogger = observability.AuditLogger

// LogAuditSink is an alias for observability.LogAuditSink.
//
// Deprecated: Use observability.LogAuditSink directly.
type LogAuditSink = observability.LogAuditSink

// NewGatewayMetrics creates a new gateway metrics collector.
//
// Deprecated: Use observability.NewMetrics directly.
func NewGatewayMetrics(metrics forge.Metrics, config MetricsConfig) *GatewayMetrics {
	return observability.NewMetrics(metrics, config)
}

// NewAccessLogger creates a new access logger.
//
// Deprecated: Use observability.NewAccessLogger directly.
func NewAccessLogger(config AccessLogConfig, logger forge.Logger) *AccessLogger {
	return observability.NewAccessLogger(config, logger)
}

// NewAuditLogger creates a new audit logger.
//
// Deprecated: Use observability.NewAuditLogger directly.
func NewAuditLogger(config AuditConfig, logger forge.Logger) *AuditLogger {
	return observability.NewAuditLogger(config, logger)
}

// NewLogAuditSink creates a sink that writes audit events to a forge.Logger.
//
// Deprecated: Use observability.NewLogAuditSink directly.
func NewLogAuditSink(logger forge.Logger) *LogAuditSink {
	return observability.NewLogAuditSink(logger)
}
