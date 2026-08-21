// Package observability wires up OpenTelemetry tracing for the node.
// Metrics stay on hashicorp/go-metrics; there is no MeterProvider here.
package observability

import (
	"context"
	"os"

	"github.com/hashicorp/go-hclog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "polygon-edge/observability"

const defaultServiceName = "ucl-node"

// defaultServiceNamespace groups the node with the rest of the chain's services.
const defaultServiceNamespace = "ucl"

// Tracer returns the node's tracer. Safe to call before InitObservability;
// it returns a no-op tracer until the global provider is set.
func Tracer() trace.Tracer {
	return otel.Tracer(instrumentationName)
}

// InitObservability sets up tracing and the global W3C trace-context propagator, and
// routes the SDK's internal error reporting into logger.
// Configuration follows standard OTEL_* env vars. With no OTLP endpoint configured,
// spans are still created for log correlation but nothing is exported.
// The returned shutdown function should be called on process exit.
func InitObservability(
	ctx context.Context,
	version string,
	logger hclog.Logger,
) (func(context.Context) error, error) {
	// The SDK reports its own failures - most commonly "traces export: context
	// deadline exceeded" when the collector is unreachable - through a global handler
	// that writes plain text to stderr via the standard log package. With JSON logging
	// on, those lines arrive at the aggregator as unparseable noise, and an export
	// outage is otherwise entirely invisible. Route them into the node's logger.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Error("OpenTelemetry SDK error", "err", err.Error())
	}))

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

// buildResource describes this process to the backend.
//
// Defaults are only applied for attributes the environment has not already supplied.
// resource.Default() includes the OTEL_RESOURCE_ATTRIBUTES detector, and resource.Merge
// is last-value-wins, so unconditionally passing our fallbacks as the second argument
// would silently beat anything an operator set through the standard OTel variable.
func buildResource(ctx context.Context, version string) (*resource.Resource, error) {
	// resource.Default() memoises behind a sync.Once, so it reads the environment once
	// per process. That is correct here (this runs once at start-up) but means the
	// merge logic has to be tested through buildResourceFrom instead.
	return buildResourceFrom(resource.Default(), version)
}

// buildResourceFrom layers this service's defaults on top of base, skipping any attribute
// base already carries.
func buildResourceFrom(base *resource.Resource, version string) (*resource.Resource, error) {
	present := make(map[attribute.Key]struct{}, len(base.Attributes()))
	for _, attr := range base.Attributes() {
		present[attr.Key] = struct{}{}
	}

	// resource.Default() always sets service.name (to "unknown_service:<binary>" when
	// nothing configured it), so presence alone cannot distinguish a real value from
	// that placeholder. OTEL_SERVICE_NAME is handled explicitly instead.
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = defaultServiceName
	}

	attrs := []attribute.KeyValue{semconv.ServiceName(serviceName)}

	if _, ok := present[semconv.ServiceNamespaceKey]; !ok {
		namespace := os.Getenv("OTEL_SERVICE_NAMESPACE")
		if namespace == "" {
			namespace = defaultServiceNamespace
		}

		attrs = append(attrs, semconv.ServiceNamespace(namespace))
	}

	if _, ok := present[semconv.ServiceInstanceIDKey]; !ok {
		attrs = append(attrs, semconv.ServiceInstanceID(instanceID()))
	}

	if _, ok := present[semconv.ServiceVersionKey]; !ok {
		attrs = append(attrs, semconv.ServiceVersion(version))
	}

	if _, ok := present[semconv.DeploymentEnvironmentKey]; !ok {
		env := os.Getenv("ENV")
		if env == "" {
			env = "dev"
		}

		attrs = append(attrs, semconv.DeploymentEnvironment(env))
	}

	// Schemaless avoids a Schema URL conflict with resource.Default(), whose
	// bundled semconv version differs from the one imported here.
	return resource.Merge(base, resource.NewSchemaless(attrs...))
}

func newTracerProvider(ctx context.Context, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	// No exporter configured. Spans must still carry valid trace IDs so logs stay
	// correlatable, but there is nothing to export them to, and the default sampler
	// would make every JSON-RPC request allocate a fully recording span only to
	// discard it. A non-recording span still carries a generated trace ID, which is
	// all LogFields needs.
	//
	// This is the one place an explicit sampler is justified, and it defers to
	// OTEL_TRACES_SAMPLER when an operator has set it: elsewhere, passing WithSampler
	// would override the environment, since NewTracerProvider applies env options
	// before explicit ones.
	if endpoint == "" {
		opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
		if _, explicit := os.LookupEnv("OTEL_TRACES_SAMPLER"); !explicit {
			opts = append(opts, sdktrace.WithSampler(sdktrace.NeverSample()))
		}

		return sdktrace.NewTracerProvider(opts...), nil
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

// instanceID identifies this particular node process. resource.Default only detects
// service.instance.id behind an experimental flag (OTEL_GO_X_RESOURCE), so it is set
// explicitly: without it, spans from every validator in the network look identical in
// the backend and there is no way to tell which one produced a block.
//
// The node's own identity (peer ID, validator address) is not usable here because
// InitObservability runs before setupSecretsManager, so deployments that want to search
// by validator should set OTEL_SERVICE_INSTANCE_ID explicitly.
func instanceID() string {
	if id := os.Getenv("OTEL_SERVICE_INSTANCE_ID"); id != "" {
		return id
	}

	// In a container this is the pod/container name, which is what an operator
	// searches by.
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}

	return "unknown"
}
