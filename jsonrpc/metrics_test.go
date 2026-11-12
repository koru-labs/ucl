package jsonrpc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Fully-qualified names of the two histograms, as the registry exposes them.
const (
	durationMetric = metricNamespace + "_" + jsonRPCMetric + "_request_duration_seconds"
	sizeMetric     = metricNamespace + "_" + jsonRPCMetric + "_request_response_size_bytes"
)

// withMetricsSink points the global metrics sink at an in-memory one for the
// duration of the test and returns it.
//
// The sink is process-global, so these tests must not call t.Parallel(): they
// would otherwise collect each other's measurements. Go runs every serial test
// to completion before any parallel one resumes, which is what keeps them apart
// from the rest of the package.
//
// It is restored to a blackhole afterwards rather than left installed. A live
// sink makes every instrumented path in the binary do real work, which is enough
// to perturb the timing-sensitive tests elsewhere in this package.
func withMetricsSink(tb testing.TB) *metrics.InmemSink {
	tb.Helper()

	sink := metrics.NewInmemSink(time.Hour, 2*time.Hour)

	cfg := metrics.DefaultConfig("test")
	cfg.EnableHostname = false
	cfg.EnableRuntimeMetrics = false

	_, err := metrics.NewGlobal(cfg, sink)
	require.NoError(tb, err)

	tb.Cleanup(func() {
		_, cleanupErr := metrics.NewGlobal(cfg, &metrics.BlackholeSink{})
		require.NoError(tb, cleanupErr)
	})

	return sink
}

// metricKey renders a metric the way the in-memory sink flattens it: the config
// prefix, the key segments joined by dots, then each label as ";name=value" in
// the order it was emitted.
func metricKey(key []string, labels ...metrics.Label) string {
	flat := "test." + strings.Join(key, ".")

	for _, l := range labels {
		flat += ";" + l.Name + "=" + l.Value
	}

	return flat
}

func methodLabel(method string) metrics.Label {
	return metrics.Label{Name: labelMethod, Value: method}
}

func reasonLabel(reason string) metrics.Label {
	return metrics.Label{Name: labelReason, Value: reason}
}

// snapshot is the sink's contents, merged across intervals. The caller must not
// hold an interval lock, so the maps are copied out.
type snapshot struct {
	counters map[string]metrics.SampledValue
	samples  map[string]metrics.SampledValue
	gauges   map[string]metrics.GaugeValue
}

// readSink merges every interval the sink is holding rather than reading only
// the first. InmemSink rotates on wall-clock boundaries aligned to its interval,
// so a test that happens to straddle one has its measurements split across two
// intervals: reading just one would silently lose half of them, as a rare and
// very confusing flake.
func readSink(tb testing.TB, sink *metrics.InmemSink) snapshot {
	tb.Helper()

	data := sink.Data()
	require.NotEmpty(tb, data)

	snap := snapshot{
		counters: map[string]metrics.SampledValue{},
		samples:  map[string]metrics.SampledValue{},
		gauges:   map[string]metrics.GaugeValue{},
	}

	for _, intv := range data {
		intv.RLock()

		// Counters and samples accumulate; a key present in two intervals holds
		// part of the total in each.
		for k, v := range intv.Counters {
			snap.counters[k] = mergeSampled(snap.counters[k], v)
		}

		for k, v := range intv.Samples {
			snap.samples[k] = mergeSampled(snap.samples[k], v)
		}

		// Gauges are instantaneous, so the newest interval wins. Intervals come
		// back oldest first.
		for k, v := range intv.Gauges {
			snap.gauges[k] = v
		}

		intv.RUnlock()
	}

	return snap
}

// mergeSampled adds v's totals to into. SampledValue embeds *AggregateSample, so
// the zero value carries a nil pointer and must not be dereferenced; the merge
// also builds a fresh sample rather than writing through the pointer the sink
// handed back.
// count and sum read a counter or sample total, returning zero when the key is
// absent. Reading .Count straight off a map miss dereferences SampledValue's nil
// embedded *AggregateSample and panics, which buries the failing assertion.
func count(snap map[string]metrics.SampledValue, key string) int {
	if v, ok := snap[key]; ok && v.AggregateSample != nil {
		return v.Count
	}

	return 0
}

func sum(snap map[string]metrics.SampledValue, key string) float64 {
	if v, ok := snap[key]; ok && v.AggregateSample != nil {
		return v.Sum
	}

	return 0
}

func mergeSampled(into, v metrics.SampledValue) metrics.SampledValue {
	if into.AggregateSample == nil {
		return v
	}

	if v.AggregateSample == nil {
		return into
	}

	merged := *into.AggregateSample
	merged.Count += v.Count
	merged.Sum += v.Sum

	return metrics.SampledValue{
		Name:            into.Name,
		Labels:          into.Labels,
		AggregateSample: &merged,
	}
}

// histogram reads one method's histogram out of the default registry, which is
// the registry the node's /metrics endpoint gathers. promauto registers these at
// package init, so counts are cumulative for the whole test binary and tests
// compare before/after rather than absolute values.
func histogram(tb testing.TB, name, method string) (count uint64, sum float64) {
	tb.Helper()

	fams, err := prometheus.DefaultGatherer.Gather()
	require.NoError(tb, err)

	for _, f := range fams {
		if f.GetName() != name {
			continue
		}

		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == labelMethod && l.GetValue() == method {
					h := m.GetHistogram()

					return h.GetSampleCount(), h.GetSampleSum()
				}
			}
		}
	}

	return 0, 0
}

// bucketLE returns the cumulative count in the histogram bucket with the given
// upper bound, so a test can prove latency landed in a real bucket rather than
// only moving _sum and _count.
func bucketLE(tb testing.TB, name, method string, le float64) uint64 {
	tb.Helper()

	fams, err := prometheus.DefaultGatherer.Gather()
	require.NoError(tb, err)

	for _, f := range fams {
		if f.GetName() != name {
			continue
		}

		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() != labelMethod || l.GetValue() != method {
					continue
				}

				for _, b := range m.GetHistogram().GetBucket() {
					if b.GetUpperBound() == le {
						return b.GetCumulativeCount()
					}
				}
			}
		}
	}

	return 0
}

func TestMetricsDispatchedRequest(t *testing.T) {
	sink := withMetricsSink(t)

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
		chainID: 1,
	})

	const method = "eth_blockNumber"

	durCount0, _ := histogram(t, durationMetric, method)
	sizeCount0, sizeSum0 := histogram(t, sizeMetric, method)
	// The widest finite bucket: any call that did not hit the request timeout
	// lands in it, so the assertion proves buckets are wired without depending
	// on how fast this machine happens to be.
	widestBucket := requestDurationBuckets[len(requestDurationBuckets)-1]
	bucketBefore := bucketLE(t, durationMetric, method, widestBucket)

	_, err := d.Handle(context.Background(),
		[]byte(`{"id":1,"jsonrpc":"2.0","method":"`+method+`","params":[]}`))
	require.NoError(t, err)

	snap := readSink(t, sink)
	label := methodLabel(method)

	durCount1, _ := histogram(t, durationMetric, method)
	sizeCount1, sizeSum1 := histogram(t, sizeMetric, method)

	calls, ok := snap.counters[metricKey(keyRequests, label)]
	require.True(t, ok, "expected a call counter labelled with the method")
	require.NotNil(t, calls.AggregateSample)
	require.Equal(t, 1, calls.Count)

	// Latency and response size are histograms, so observations accumulate into
	// cumulative buckets instead of overwriting one another.
	require.Equal(t, durCount0+1, durCount1, "expected one latency observation")
	require.Equal(t, sizeCount0+1, sizeCount1, "expected one response-size observation")
	require.Greater(t, sizeSum1, sizeSum0, "expected a non-zero response size")

	// The call must land in a real bucket, not only in _sum/_count:
	// histogram_quantile reads buckets, so an empty bucket set means no p99.
	require.Equal(t, bucketBefore+1, bucketLE(t, durationMetric, method, widestBucket),
		"expected the call to be counted in a latency bucket")

	// The call has returned, so nothing is in flight for the method.
	inFlight, ok := snap.gauges[metricKey(keyRequestsInFlight, label)]
	require.True(t, ok, "expected an in-flight gauge labelled with the method")
	require.Equal(t, float32(0), inFlight.Value)

	_, errored := snap.counters[metricKey(keyRequestErrors, label, metrics.Label{Name: labelCode, Value: "-32600"})]
	require.False(t, errored, "a successful call must not count an error")
}

// TestMetricsConcurrentCallsAccumulate is the regression this replaced a gauge
// for: concurrent calls to one method used to overwrite each other, leaving a
// single last-write-wins value with no percentile derivable from it.
func TestMetricsConcurrentCallsAccumulate(t *testing.T) {
	sink := withMetricsSink(t)

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})

	const (
		method = "eth_blockNumber"
		calls  = 16
	)

	durCount0, _ := histogram(t, durationMetric, method)

	var wg sync.WaitGroup

	wg.Add(calls)

	for i := 0; i < calls; i++ {
		go func() {
			defer wg.Done()

			_, err := d.Handle(context.Background(),
				[]byte(`{"id":1,"jsonrpc":"2.0","method":"`+method+`","params":[]}`))
			require.NoError(t, err)
		}()
	}

	wg.Wait()

	snap := readSink(t, sink)
	label := methodLabel(method)

	durCount1, _ := histogram(t, durationMetric, method)

	require.Equal(t, float64(calls), sum(snap.counters, metricKey(keyRequests, label)))
	require.Equal(t, durCount0+uint64(calls), durCount1, "every concurrent call must be observed")
	require.Equal(t, float32(0), snap.gauges[metricKey(keyRequestsInFlight, label)].Value)
}

func TestMetricsRequestError(t *testing.T) {
	sink := withMetricsSink(t)

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})

	const method = "eth_getBlockByNumber"

	// Bad params fail after the method resolves, so this is a per-method error
	// rather than a rejection.
	_, err := d.Handle(context.Background(),
		[]byte(`{"id":1,"jsonrpc":"2.0","method":"`+method+`","params":["not-a-block"]}`))
	require.NoError(t, err)

	snap := readSink(t, sink)

	errs, ok := snap.counters[metricKey(keyRequestErrors,
		methodLabel(method), metrics.Label{Name: labelCode, Value: "-32602"})]
	require.True(t, ok, "expected an error counter labelled with the method and code")
	require.Equal(t, 1, errs.Count)
}

// TestMetricsHistogramsAreLazy guards the series budget: resolving a histogram
// child creates it, and a created child is exported, so a method must stay
// absent from the metrics until something actually calls it.
func TestMetricsHistogramsAreLazy(t *testing.T) {
	_ = withMetricsSink(t)

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})
	require.NoError(t, d.registerService("lazy", &lazyProbeService{}))

	// Registered, never called: no series of its own. histogram returns zero for
	// a method with no child at all, which is exactly the state being asserted.
	require.Zero(t, mustHistogramCount(t, "lazy_untouched"))
	require.Zero(t, bucketLE(t, durationMetric, "lazy_untouched",
		requestDurationBuckets[len(requestDurationBuckets)-1]),
		"an uncalled method must not publish buckets")

	before := mustHistogramCount(t, "lazy_touched")

	_, err := d.Handle(context.Background(),
		[]byte(`{"id":1,"jsonrpc":"2.0","method":"lazy_touched","params":[]}`))
	require.NoError(t, err)

	// Called: now it exists and has been observed exactly once more.
	require.Equal(t, before+1, mustHistogramCount(t, "lazy_touched"))

	// The uncalled sibling still publishes nothing.
	require.Zero(t, mustHistogramCount(t, "lazy_untouched"))
}

func mustHistogramCount(tb testing.TB, method string) uint64 {
	tb.Helper()

	count, _ := histogram(tb, durationMetric, method)

	return count
}

type lazyProbeService struct{}

func (s *lazyProbeService) Touched() (interface{}, error)   { return "ok", nil }
func (s *lazyProbeService) Untouched() (interface{}, error) { return "ok", nil }

func TestMetricsRejections(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		params *dispatcherParams
		reason string
	}{
		{
			name:   "unknown method",
			body:   `{"id":1,"jsonrpc":"2.0","method":"eth_notAThing","params":[]}`,
			params: &dispatcherParams{},
			reason: rejectMethodNotFound,
		},
		{
			name:   "malformed body",
			body:   `{"id":1,`,
			params: &dispatcherParams{},
			reason: rejectMalformedRequest,
		},
		{
			name:   "empty body",
			body:   `   `,
			params: &dispatcherParams{},
			reason: rejectMalformedRequest,
		},
		{
			name: "batch too long",
			body: `[{"id":1,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
				{"id":2,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]}]`,
			params: &dispatcherParams{jsonRPCBatchLengthLimit: 1},
			reason: rejectBatchTooLong,
		},
		{
			name:   "batch cost too high",
			body:   `[{"id":1,"jsonrpc":"2.0","method":"eth_getLogs","params":[]}]`,
			params: &dispatcherParams{batchCostLimit: 1},
			reason: rejectBatchCostTooHigh,
		},
		{
			name:   "response too large",
			body:   `{"id":1,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]}`,
			params: &dispatcherParams{maxResponseSize: 1},
			reason: rejectResponseTooLarge,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := withMetricsSink(t)

			d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), test.params)

			_, err := d.Handle(context.Background(), []byte(test.body))
			require.NoError(t, err)

			snap := readSink(t, sink)

			rejected, ok := snap.counters[metricKey(keyRejected, reasonLabel(test.reason))]
			require.True(t, ok, "expected a rejection counted as %q", test.reason)
			require.Positive(t, rejected.Count)
		})
	}
}

// TestMetricsBatchDeadlineCountsEveryShedRequest checks the shed-load count
// reflects requests turned away, not the batches that carried them.
func TestMetricsBatchDeadlineCountsEveryShedRequest(t *testing.T) {
	sink := withMetricsSink(t)

	svc := &testRPCService{}
	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
		requestTimeout: 80 * time.Millisecond,
	})
	require.NoError(t, d.registerService("test", svc))

	body := []byte(`[
		{"id":1,"jsonrpc":"2.0","method":"test_sleep","params":["0x32"]},
		{"id":2,"jsonrpc":"2.0","method":"test_sleep","params":["0x32"]},
		{"id":3,"jsonrpc":"2.0","method":"test_sleep","params":["0x32"]}
	]`)

	_, err := d.Handle(context.Background(), body)
	require.NoError(t, err)

	snap := readSink(t, sink)

	shed, ok := snap.counters[metricKey(keyRejected, reasonLabel(rejectBatchDeadline))]
	require.True(t, ok, "expected the shed batch requests to be counted")
	require.Positive(t, shed.Sum)
}

// TestMetricsUnknownMethodIsNotALabel guards the cardinality bound: method names
// come from the client, so an unresolved one must never reach a label.
func TestMetricsUnknownMethodIsNotALabel(t *testing.T) {
	sink := withMetricsSink(t)

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})

	const junk = "eth_cardinalityBomb"

	_, err := d.Handle(context.Background(),
		[]byte(`{"id":1,"jsonrpc":"2.0","method":"`+junk+`","params":[]}`))
	require.NoError(t, err)

	snap := readSink(t, sink)

	for key := range snap.counters {
		require.NotContains(t, key, junk)
	}

	for key := range snap.samples {
		require.NotContains(t, key, junk)
	}

	for key := range snap.gauges {
		require.NotContains(t, key, junk)
	}

	// The request is still visible, as a rejection.
	require.Positive(t, count(snap.counters, metricKey(keyRejected, reasonLabel(rejectMethodNotFound))))
}

func TestMetricsWSConnectionsAndLimit(t *testing.T) {
	sink := withMetricsSink(t)

	l := newWSLimiter(1, 0, 0)

	require.True(t, l.tryAddConn())
	require.False(t, l.tryAddConn(), "second connection must be refused")

	snap := readSink(t, sink)

	require.Equal(t, float32(1), snap.gauges[metricKey(keyWSConnections)].Value)

	rejected, ok := snap.counters[metricKey(keyRejected, reasonLabel(rejectWSConnLimit))]
	require.True(t, ok, "expected the refused connection to be counted")
	require.Equal(t, 1, rejected.Count)

	l.removeConn()

	require.Equal(t, float32(0), readSink(t, sink).gauges[metricKey(keyWSConnections)].Value)
}

// TestMetricsWSConnectionsConcurrent checks the connection gauge settles on the
// truth after concurrent churn, rather than keeping whichever write landed last.
func TestMetricsWSConnectionsConcurrent(t *testing.T) {
	sink := withMetricsSink(t)

	l := newWSLimiter(0, 0, 0)

	const conns = 32

	var wg sync.WaitGroup

	wg.Add(conns)

	for i := 0; i < conns; i++ {
		go func() {
			defer wg.Done()

			require.True(t, l.tryAddConn())
			l.removeConn()
		}()
	}

	wg.Wait()

	require.Zero(t, l.connCount())
	require.Equal(t, float32(0), readSink(t, sink).gauges[metricKey(keyWSConnections)].Value)
}

func TestMetricsWSInFlightRejections(t *testing.T) {
	sink := withMetricsSink(t)

	l := newWSLimiter(0, 1, 0)

	require.True(t, l.tryAcquireInFlight())
	require.False(t, l.tryAcquireInFlight(), "second in-flight request must be refused")

	slots := newWSConnSlots(1)
	require.True(t, slots.try())
	require.False(t, slots.try(), "second per-connection request must be refused")

	snap := readSink(t, sink)

	require.Equal(t, 1, count(snap.counters, metricKey(keyRejected, reasonLabel(rejectWSInFlight))))
	require.Equal(t, 1, count(snap.counters, metricKey(keyRejected, reasonLabel(rejectWSInFlightConn))))
}

// TestMetricsDebugConcurrencyRejection covers the one rejection path a dev-mode
// node cannot provoke: traces there finish instantly, so the semaphore never
// saturates and the acquire never times out.
func TestMetricsDebugConcurrencyRejection(t *testing.T) {
	sink := withMetricsSink(t)

	th := NewThrottling(1, 50*time.Millisecond)

	release := make(chan struct{})
	admitted := make(chan struct{})

	go func() {
		_, _ = th.AttemptRequest(context.Background(), func() (interface{}, error) {
			close(admitted)
			<-release

			return nil, nil
		})
	}()

	<-admitted

	// The only permit is held, so this one cannot be admitted before its timeout.
	_, err := th.AttemptRequest(context.Background(), func() (interface{}, error) {
		return nil, nil
	})
	require.ErrorIs(t, err, errRequestLimitExceeded)

	close(release)

	snap := readSink(t, sink)

	require.Equal(t, 1, count(snap.counters, metricKey(keyRejected, reasonLabel(rejectDebugConcurrency))))
}

func TestMetricsLiveFilters(t *testing.T) {
	sink := withMetricsSink(t)

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 0, FilterLimits{})

	go m.Run()

	defer m.Close()

	id, err := m.NewBlockFilter(nil)
	require.NoError(t, err)

	require.Equal(t, float32(1), readSink(t, sink).gauges[metricKey(keyLiveFilters)].Value)

	require.True(t, m.Uninstall(id))
	require.Equal(t, float32(0), readSink(t, sink).gauges[metricKey(keyLiveFilters)].Value)
}

func TestMetricsMalformedWSRequestIDRejection(t *testing.T) {
	sink := withMetricsSink(t)

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})

	// A non-integral id is refused by formatID before the method is read.
	res, err := d.HandleWs([]byte(`{"id":1.5,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]}`), nil)
	require.NoError(t, err)
	require.Contains(t, string(res), "Invalid json request")

	snap := readSink(t, sink)

	require.Positive(t, count(snap.counters, metricKey(keyRejected, reasonLabel(rejectMalformedRequest))))
}

func TestMetricsOversizedHTTPBodyRejection(t *testing.T) {
	sink := withMetricsSink(t)

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})
	j := &JSONRPC{
		logger:     hclog.NewNullLogger(),
		config:     &Config{MaxRequestBodySize: 512},
		dispatcher: d,
	}

	srv := httptest.NewServer(http.HandlerFunc(j.handleJSONRPCRequest))
	defer srv.Close()

	body := fmt.Sprintf(`{"id":1,"jsonrpc":"2.0","method":"eth_blockNumber","params":[%q]}`,
		strings.Repeat("x", 2048))

	res, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	require.NoError(t, err)

	defer res.Body.Close()

	snap := readSink(t, sink)

	require.Equal(t, 1, count(snap.counters, metricKey(keyRejected, reasonLabel(rejectBodyTooLarge))))
}

// TestMetricsOversizedWSFrameRejection covers the websocket counterpart of the
// HTTP body cap. gorilla enforces the read limit itself and answers close 1009
// without handing the payload up, so the read error is the only place it can be
// counted.
func TestMetricsOversizedWSFrameRejection(t *testing.T) {
	sink := withMetricsSink(t)

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})
	j := &JSONRPC{
		logger:     hclog.NewNullLogger(),
		config:     &Config{WebSocketReadLimit: 1024},
		dispatcher: d,
	}

	srv := httptest.NewServer(http.HandlerFunc(j.handleWs))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)

	defer conn.Close()

	oversized := fmt.Sprintf(`{"id":1,"jsonrpc":"2.0","method":"eth_blockNumber","params":[%q]}`,
		strings.Repeat("x", 4096))
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(oversized)))

	// The server closes with 1009 rather than replying.
	_, _, err = conn.ReadMessage()
	require.True(t, websocket.IsCloseError(err, websocket.CloseMessageTooBig),
		"expected close 1009, got %v", err)

	// gorilla sends the close frame before the server's read returns, so the
	// client can observe the close before the handler goroutine has counted it.
	require.Eventually(t, func() bool {
		return count(readSink(t, sink).counters,
			metricKey(keyRejected, reasonLabel(rejectWSFrameTooLarge))) == 1
	}, 5*time.Second, 10*time.Millisecond, "oversized frame was never counted")
}

func TestMetricsPerConnectionFilterLimitRejection(t *testing.T) {
	sink := withMetricsSink(t)

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 0, FilterLimits{PerConnection: 1})

	go m.Run()

	defer m.Close()

	conn := &mockWsConn{}

	_, err := m.NewBlockFilter(conn)
	require.NoError(t, err)

	_, err = m.NewBlockFilter(conn)
	require.ErrorIs(t, err, ErrConnectionFilterLimitExceeded)

	snap := readSink(t, sink)

	require.Equal(t, 1, count(snap.counters, metricKey(keyRejected, reasonLabel(rejectFilterLimitConn))))
}

func TestMetricsFilterLimitRejection(t *testing.T) {
	sink := withMetricsSink(t)

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 0, FilterLimits{Global: 1})

	go m.Run()

	defer m.Close()

	_, err := m.NewBlockFilter(nil)
	require.NoError(t, err)

	_, err = m.NewBlockFilter(nil)
	require.ErrorIs(t, err, ErrFilterLimitExceeded)

	snap := readSink(t, sink)

	require.Equal(t, 1, count(snap.counters, metricKey(keyRejected, reasonLabel(rejectFilterLimit))))
}
