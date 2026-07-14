// Package observability wires up OpenTelemetry tracing for the node.
// Metrics stay on hashicorp/go-metrics; there is no MeterProvider here.
package observability

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "polygon-edge/observability"

const defaultServiceName = "ucl-node"

// Tracer returns the node's tracer. Safe to call before InitObservability;
// it returns a no-op tracer until the global provider is set.
func Tracer() trace.Tracer {
	return otel.Tracer(instrumentationName)
}

// InitObservability sets up tracing and the global W3C trace-context propagator.
// Configuration follows standard OTEL_* env vars. With no OTLP endpoint configured,
// spans are still created for log correlation but nothing is exported.
// The returned shutdown function should be called on process exit.
func InitObservability(ctx context.Context, version string) (func(context.Context) error, error) {
	res, err := buildResource(ctx, version)
	if err != nil {
		return nil, err
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	tp, err := newTracerProvider(ctx, res)
	if err != nil {
		return nil, err
	}

	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

func buildResource(ctx context.Context, version string) (*resource.Resource, error) {
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = defaultServiceName
	}

	env := os.Getenv("ENV")
	if env == "" {
		env = "dev"
	}

	// Schemaless avoids a Schema URL conflict with resource.Default(), whose
	// bundled semconv version differs from the one imported here.
	return resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
			semconv.DeploymentEnvironment(env),
		),
	)
}

func newTracerProvider(ctx context.Context, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	// No exporter: spans still get valid trace IDs and honour inbound traceparent.
	if endpoint == "" {
		return sdktrace.NewTracerProvider(sdktrace.WithResource(res)), nil
	}

	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	), nil
}
