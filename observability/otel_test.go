package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"go.opentelemetry.io/otel"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// unsetEnv clears a variable for the duration of the test. t.Setenv("X", "") sets it to
// the empty string, which is not the same thing: the SDK treats an empty
// OTEL_TRACES_SAMPLER as an unsupported value rather than as absent.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers cleanup to restore the original value
	_ = os.Unsetenv(key)
}

func TestBuildResourceSetsServiceIdentity(t *testing.T) {
	unsetEnv(t, "OTEL_SERVICE_NAME")
	unsetEnv(t, "OTEL_SERVICE_NAMESPACE")

	res, err := buildResourceFrom(resource.Empty(), "v1.2.3")
	if err != nil {
		t.Fatalf("buildResourceFrom: %v", err)
	}

	got := attrsOf(res)

	want := map[string]string{
		"service.name":      defaultServiceName,
		"service.namespace": defaultServiceNamespace,
		"service.version":   "v1.2.3",
	}

	for key, wantVal := range want {
		if got[key] != wantVal {
			t.Errorf("%s = %q, want %q", key, got[key], wantVal)
		}
	}

	// Without this, spans from every validator in the network are indistinguishable.
	if got["service.instance.id"] == "" {
		t.Error("service.instance.id is unset")
	}
}

// OTEL_RESOURCE_ATTRIBUTES is the standard way to set resource attributes, and it is what
// an operator reading the OpenTelemetry docs will reach for. Our defaults must not beat it.
func TestBuildResourceDefersToResourceAttributesEnv(t *testing.T) {
	unsetEnv(t, "OTEL_SERVICE_NAMESPACE")

	// Stands in for what resource.Default() produces when OTEL_RESOURCE_ATTRIBUTES is
	// set. Testing through resource.Default() directly is not reliable: it memoises
	// behind a sync.Once, so whichever test runs first fixes the value for the process.
	base := resource.NewSchemaless(
		semconv.ServiceNamespace("from-env"),
		semconv.DeploymentEnvironment("from-env"),
		semconv.ServiceInstanceID("from-env"),
	)

	res, err := buildResourceFrom(base, "v1.2.3")
	if err != nil {
		t.Fatalf("buildResourceFrom: %v", err)
	}

	got := attrsOf(res)

	for _, key := range []string{"service.namespace", "deployment.environment", "service.instance.id"} {
		if got[key] != "from-env" {
			t.Errorf("%s = %q, want %q (hardcoded default overrode the environment)", key, got[key], "from-env")
		}
	}
}

func TestInstanceIDPrefersExplicitEnv(t *testing.T) {
	t.Setenv("OTEL_SERVICE_INSTANCE_ID", "validator-3")

	if got := instanceID(); got != "validator-3" {
		t.Errorf("instanceID() = %q, want %q", got, "validator-3")
	}
}

func TestInstanceIDFallsBackToHostname(t *testing.T) {
	unsetEnv(t, "OTEL_SERVICE_INSTANCE_ID")

	if got := instanceID(); got == "" {
		t.Error("instanceID() returned empty; expected a hostname or 'unknown'")
	}
}

// With no collector there is nothing to export to, so spans should not be recorded --
// otherwise every JSON-RPC request builds a full span and throws it away. Trace IDs must
// survive regardless, or log correlation breaks.
func TestNoEndpointSkipsRecordingButKeepsTraceIDs(t *testing.T) {
	unsetEnv(t, "OTEL_EXPORTER_OTLP_ENDPOINT")
	unsetEnv(t, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	unsetEnv(t, "OTEL_TRACES_SAMPLER")

	tp := newProviderForTest(t)

	ctx, span := tp.Tracer("test").Start(context.Background(), "probe")
	defer span.End()

	if span.IsRecording() {
		t.Error("span is recording with no exporter configured; the work is discarded")
	}

	if !span.SpanContext().IsValid() {
		t.Fatal("span context invalid; log correlation would break")
	}

	if fields := LogFields(ctx); len(fields) != 4 {
		t.Fatalf("LogFields = %v, want trace_id and span_id", fields)
	}
}

// An operator who sets the sampler explicitly must still get it, even with no exporter.
func TestExplicitSamplerWinsOverTheNoEndpointDefault(t *testing.T) {
	unsetEnv(t, "OTEL_EXPORTER_OTLP_ENDPOINT")
	unsetEnv(t, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")

	tp := newProviderForTest(t)

	_, span := tp.Tracer("test").Start(context.Background(), "probe")
	defer span.End()

	if !span.IsRecording() {
		t.Error("OTEL_TRACES_SAMPLER=always_on was ignored")
	}
}

// The SDK reads OTEL_TRACES_SAMPLER itself and NewTracerProvider applies environment
// options before explicit ones, so passing sdktrace.WithSampler on the exporting path
// would silently override operator configuration.
func TestSamplerComesFromTheEnvironment(t *testing.T) {
	unsetEnv(t, "OTEL_EXPORTER_OTLP_ENDPOINT")
	unsetEnv(t, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	tp := newProviderForTest(t)

	_, span := tp.Tracer("test").Start(context.Background(), "probe")
	defer span.End()

	if span.SpanContext().IsSampled() {
		t.Error("OTEL_TRACES_SAMPLER=always_off was ignored")
	}
}

func newProviderForTest(t *testing.T) *sdktrace.TracerProvider {
	t.Helper()

	res, err := buildResourceFrom(resource.Empty(), "test")
	if err != nil {
		t.Fatalf("buildResourceFrom: %v", err)
	}

	tp, err := newTracerProvider(context.Background(), res)
	if err != nil {
		t.Fatalf("newTracerProvider: %v", err)
	}

	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	return tp
}

func attrsOf(res interface{ Attributes() []attribute.KeyValue }) map[string]string {
	got := map[string]string{}
	for _, attr := range res.Attributes() {
		got[string(attr.Key)] = attr.Value.AsString()
	}

	return got
}

// The SDK reports export failures through its global error handler, which by default
// writes plain text to stderr via the standard log package. With JSON logging on that
// lands in the aggregator as unparseable noise, and an export outage is otherwise
// invisible. InitObservability must redirect it into the node's logger.
func TestSDKErrorsGoToTheLogger(t *testing.T) {
	unsetEnv(t, "OTEL_EXPORTER_OTLP_ENDPOINT")
	unsetEnv(t, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")

	var buf bytes.Buffer

	logger := hclog.New(&hclog.LoggerOptions{
		Output:     &buf,
		JSONFormat: true,
		Level:      hclog.Debug,
	})

	shutdown, err := InitObservability(context.Background(), "test", logger)
	if err != nil {
		t.Fatalf("InitObservability: %v", err)
	}

	t.Cleanup(func() { _ = shutdown(context.Background()) })

	otel.Handle(errors.New("traces export: context deadline exceeded"))

	if buf.Len() == 0 {
		t.Fatal("SDK error did not reach the logger; it went to stderr as unstructured text")
	}

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("SDK error was not emitted as JSON (%v): %s", err, buf.String())
	}

	if record["@level"] != "error" {
		t.Errorf("@level = %v, want error", record["@level"])
	}

	if !strings.Contains(record["err"].(string), "context deadline exceeded") {
		t.Errorf("err = %v, want the SDK's message", record["err"])
	}
}
