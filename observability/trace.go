package observability

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// LogFields returns trace_id/span_id from the active span as hclog key-value pairs.
// Returns nil when ctx has no valid span, so callers can pass it unconditionally.
func LogFields(ctx context.Context) []interface{} {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}

	return []interface{}{
		"trace_id", sc.TraceID().String(),
		"span_id", sc.SpanID().String(),
	}
}
