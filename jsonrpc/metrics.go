package jsonrpc

import (
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/go-metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const segRequest = "request"

// Counter and gauge keys, recorded through go-metrics. Its sink prefixes these
// with the process namespace, so a call counter surfaces as
// edge_json_rpc_request_total.
//
// Counters are cumulative and gauges are instantaneous, so neither is affected
// by the sink's sampling window; the two distributions are registered separately
// below, for the reason given there.
var (
	keyRequests         = []string{jsonRPCMetric, segRequest, "total"}
	keyRequestErrors    = []string{jsonRPCMetric, segRequest, "errors"}
	keyRequestsInFlight = []string{jsonRPCMetric, segRequest, "in_flight"}
	keyRejected         = []string{jsonRPCMetric, "rejected"}
	keyLiveFilters      = []string{jsonRPCMetric, "filters", "live"}
	keyWSConnections    = []string{jsonRPCMetric, "ws", "connections"}
)

// requestDurationBuckets is roughly log-spaced from 100us to the 30s request
// timeout. The low end is deliberately fine: in-memory lookups like
// eth_blockNumber are tens of microseconds, and with a coarser floor every cheap
// method's p99 pins to the first bucket's upper bound, which hides a regression
// until it crosses a millisecond.
var requestDurationBuckets = []float64{
	0.0001, 0.00025, 0.0005, 0.001, 0.005, 0.025, 0.1, 0.5, 2.5, 10, 30,
}

// metricNamespace must match the ServiceName go-metrics is configured with in
// Server.setupTelemetry, so the histograms below land beside the go-metrics
// series under one edge_json_rpc_ prefix.
const metricNamespace = "edge"

// Latency and response size are real Prometheus histograms, registered straight
// with client_golang instead of going through go-metrics.
//
// go-metrics has only one distribution primitive, AddSample, and its Prometheus
// sink renders it as a Summary with MaxAge hardcoded to 10 seconds. The fleet
// scrapes every 15 seconds, so any method without traffic in the last 10 seconds
// reports every quantile as NaN while _sum and _count keep climbing: a p99 panel
// built on it is blank for all but continuously-hot methods, and an alert on it
// would never fire. Histogram buckets are cumulative counters, so they cannot
// decay out from under a scrape, and unlike a summary's precomputed quantiles
// they aggregate across nodes — histogram_quantile over sum by (le) is a fleet
// p99, which is what alerting on a p99 regression needs.
//
// Bucket sets are deliberately short: every bucket is a time series per method.
var (
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: jsonRPCMetric,
		Name:      "request_duration_seconds",
		Help:      "JSON-RPC request latency in seconds, measured end to end, by method.",
		Buckets:   requestDurationBuckets,
	}, []string{labelMethod})

	responseSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricNamespace,
		Subsystem: jsonRPCMetric,
		Name:      "request_response_size_bytes",
		Help:      "JSON-RPC response payload size in bytes, by method.",
		// A small result through to the 25 MiB response cap.
		Buckets: []float64{512, 4096, 32768, 262144, 2097152, 16777216},
	}, []string{labelMethod})
)

// Label names.
const (
	labelMethod = "method"
	labelReason = "reason"
	labelCode   = "code"
)

// Rejection reasons, labelling keyRejected. Deliberately a closed set: every
// value here is chosen by this package, never derived from request content, so
// a client cannot mint new label values by varying what it sends.
const (
	rejectMethodNotFound   = "method_not_found"
	rejectMalformedRequest = "malformed_request"
	rejectBatchTooLong     = "batch_too_long"
	rejectBatchCostTooHigh = "batch_cost_too_high"
	rejectBatchDeadline    = "batch_deadline_exceeded"
	rejectResponseTooLarge = "response_too_large"
	rejectBodyTooLarge     = "body_too_large"
	rejectWSFrameTooLarge  = "ws_frame_too_large"
	rejectDebugConcurrency = "debug_concurrency_limit"
	rejectWSConnLimit      = "ws_connection_limit"
	rejectWSInFlight       = "ws_in_flight_limit"
	rejectWSInFlightConn   = "ws_in_flight_limit_per_conn"
	rejectFilterLimit      = "filter_limit"
	rejectFilterLimitConn  = "filter_limit_per_conn"
)

// incrRejected counts a request turned away without being dispatched. The
// method is deliberately absent: these rejections happen before the method is
// resolved, or regardless of it, and an unresolved method cannot be a label.
func incrRejected(reason string) {
	metrics.IncrCounterWithLabels(keyRejected, 1, []metrics.Label{
		{Name: labelReason, Value: reason},
	})
}

// incrRejectedN counts n requests turned away for the same reason at once, as a
// batch does when it abandons everything still queued behind a limit.
func incrRejectedN(reason string, n int) {
	if n <= 0 {
		return
	}

	metrics.IncrCounterWithLabels(keyRejected, float32(n), []metrics.Label{
		{Name: labelReason, Value: reason},
	})
}

// setLiveFilters publishes the number of filters currently installed node-wide.
// Both call sites already hold the FilterManager write lock, which is what keeps
// this last-write-wins gauge from going stale under concurrent installs.
func setLiveFilters(n int) {
	metrics.SetGauge(keyLiveFilters, float32(n))
}

// setWSConnections publishes the number of open websocket connections. Callers
// must serialise the count with the publish, for the reason given on methodGauge.
func setWSConnections(n int64) {
	metrics.SetGauge(keyWSConnections, float32(n))
}

// methodGauge holds one method's in-flight depth together with the labels every
// metric for that method is tagged with.
//
// The depth and its publish are kept under one lock on purpose. go-metrics
// gauges are last-write-wins and carry no notion of increment, so an atomic
// counter published outside a lock lets two requests interleave count and write:
// the later increment can land in the sink after the final decrement, leaving
// the gauge stuck at a non-zero depth with nothing in flight. For a gauge meant
// to carry an abuse alert, a value that sticks is worse than one that costs a
// mutex.
type methodGauge struct {
	method string

	mu     sync.Mutex
	labels []metrics.Label
	depth  int64

	// Resolved on the method's first call, not at registration. WithLabelValues
	// creates the child, and a created child is exported — so resolving these
	// eagerly would publish a full set of zero-valued buckets for every
	// registered method, debug_* included, which is roughly 2,000 idle series a
	// node never has anything to say about. Deferring keeps a method absent from
	// the metrics until it is actually called, and the Once keeps the hot path to
	// one atomic load afterwards.
	observersOnce sync.Once
	duration      prometheus.Observer
	size          prometheus.Observer
}

func (g *methodGauge) observers() (prometheus.Observer, prometheus.Observer) {
	g.observersOnce.Do(func() {
		g.duration = requestDuration.WithLabelValues(g.method)
		g.size = responseSize.WithLabelValues(g.method)
	})

	return g.duration, g.size
}

func (g *methodGauge) add(delta int64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.depth += delta

	metrics.SetGaugeWithLabels(keyRequestsInFlight, float32(g.depth), g.labels)
}

// methodMetrics records per-method RPC telemetry across a fixed method set.
//
// The set is frozen before the first request is served, which is what makes a
// method label safe: method names arrive from the client, so labelling on an
// arbitrary one lets a caller create an unbounded number of time series just by
// sending junk. A method outside the set gets no labelled series at all — the
// label value is only ever read from this map, never from a request — and such
// traffic is counted as a rejection instead.
type methodMetrics struct {
	methods map[string]*methodGauge
}

// wsOnlyMethods are dispatched by handleSingleWs rather than through the service
// map, so they have to be seeded into the method set by hand.
var wsOnlyMethods = []string{methodEthSubscribe, methodEthUnsubscribe}

func newMethodMetrics() *methodMetrics {
	m := &methodMetrics{methods: make(map[string]*methodGauge)}

	for _, method := range wsOnlyMethods {
		m.add(method)
	}

	return m
}

// addService registers one service's methods, so the metric method set tracks
// the registered method set however registration is ordered.
//
// Setup only, and unsynchronised like the service map it mirrors: every service
// must be registered before the first request is served.
func (m *methodMetrics) addService(serviceName string, funcMap map[string]*funcData) {
	if m == nil {
		return
	}

	for funcName := range funcMap {
		m.add(serviceName + "_" + funcName)
	}
}

func (m *methodMetrics) add(method string) {
	m.methods[method] = &methodGauge{
		method: method,
		labels: []metrics.Label{{Name: labelMethod, Value: method}},
	}
}

// beginRequest records the start of a dispatched call and returns the function
// that records its outcome. The returned function must be called exactly once.
//
// A nil *methodMetrics, or a method outside the registered set, yields an inert
// recorder, so neither needs special casing at the call site.
func (m *methodMetrics) beginRequest(method string) func(size int, err Error) {
	if m == nil {
		return func(int, Error) {}
	}

	g, ok := m.methods[method]
	if !ok {
		return func(int, Error) {}
	}

	metrics.IncrCounterWithLabels(keyRequests, 1, g.labels)
	g.add(1)

	start := time.Now().UTC()

	return func(size int, err Error) {
		g.add(-1)

		// Measured end to end, unlike the endpoint-call-only timing this
		// replaced: param decoding and response marshalling are part of what a
		// caller waits for, and marshalling is where a large response burns its
		// time.
		duration, responseBytes := g.observers()
		duration.Observe(time.Since(start).Seconds())
		responseBytes.Observe(float64(size))

		if err != nil {
			metrics.IncrCounterWithLabels(keyRequestErrors, 1, []metrics.Label{
				{Name: labelMethod, Value: method},
				{Name: labelCode, Value: strconv.Itoa(err.ErrorCode())},
			})
		}
	}
}
