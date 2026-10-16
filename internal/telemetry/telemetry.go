// Package telemetry holds bastion's tracing helpers: the tracer, root spans
// for background work, error recording that never copies an error's text,
// and the client span every outbound HTTP call gets.
package telemetry

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope of every span bastion starts.
const ScopeName = "github.com/xraph/bastion"

// Tracer returns bastion's tracer from the global provider. It is looked up
// on each call, so a provider installed later (forge.New installs one, and
// tests swap them) is the one used.
func Tracer() trace.Tracer {
	return otel.Tracer(ScopeName)
}

// Start starts a span as a child of whatever span ctx carries.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}

// Detach returns ctx without its span, keeping its values and cancellation.
// A loop or goroutine that outlives the code that started it makes its ctx
// with Detach first. context.WithCancel, WithoutCancel and WithTimeout all
// keep the parent's span, so without it every call the loop makes before
// its own root span exists (and every query grove traces) would land under
// a span that ended long ago: Forge's boot span for anything started in
// Start, the server span for anything a request handler started.
func Detach(ctx context.Context) context.Context {
	return trace.ContextWithSpanContext(ctx, trace.SpanContext{})
}

// StartRoot starts the span for one unit of background work as the root of
// a new trace. Background loops run on the context Start was given, which
// carries Forge's boot span, and their spans must not hang off it.
func StartRoot(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	opts = append(opts, trace.WithNewRoot())

	return Tracer().Start(ctx, name, opts...)
}

// Fail marks span as failed. class is a short fixed description of what
// went wrong. err adds an exception event carrying only its Go type: error
// text from drivers and libraries quotes URLs, hosts and values, so it is
// never recorded.
func Fail(span trace.Span, class string, err error) {
	span.SetStatus(codes.Error, class)

	if err != nil {
		span.AddEvent("exception", trace.WithAttributes(
			attribute.String("exception.type", ErrorType(err)),
		))
	}
}

// ErrorType returns the Go type of the innermost error in err's chain, such
// as "*net.OpError" or "syscall.Errno". A joined error reports its own type.
func ErrorType(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			break
		}

		err = next
	}

	return fmt.Sprintf("%T", err)
}
