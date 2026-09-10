package jsonrpc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestWSLimiter_RejectsConnectionPastCeiling(t *testing.T) {
	t.Parallel()

	l := newWSLimiter(2, 0, 0)

	require.True(t, l.tryAddConn())
	require.True(t, l.tryAddConn())
	require.False(t, l.tryAddConn())
	require.EqualValues(t, 2, l.connCount())

	l.removeConn()
	require.True(t, l.tryAddConn())
	require.EqualValues(t, 2, l.connCount())
}

func TestWSLimiter_ZeroCeilingsAreUnlimited(t *testing.T) {
	t.Parallel()

	l := newWSLimiter(0, 0, 0)

	for i := 0; i < 32; i++ {
		require.True(t, l.tryAddConn())
		require.True(t, l.tryAcquireInFlight())
	}

	require.EqualValues(t, 32, l.connCount())
}

func TestWSLimiter_NilIsUnlimited(t *testing.T) {
	t.Parallel()

	var l *wsLimiter

	require.True(t, l.tryAddConn())
	require.True(t, l.tryAcquireInFlight())
	l.removeConn()
	l.releaseInFlight()
}

func TestWSLimiter_InFlightSemaphore(t *testing.T) {
	t.Parallel()

	l := newWSLimiter(0, 2, 0)

	require.True(t, l.tryAcquireInFlight())
	require.True(t, l.tryAcquireInFlight())
	require.False(t, l.tryAcquireInFlight())

	l.releaseInFlight()
	require.True(t, l.tryAcquireInFlight())
}

func TestWSConnSlots_RejectsPastCeiling(t *testing.T) {
	t.Parallel()

	s := newWSConnSlots(2)

	require.True(t, s.try())
	require.True(t, s.try())
	require.False(t, s.try())

	s.release()
	require.True(t, s.try())
}

type blockingWSDispatcher struct {
	started chan struct{}
	release chan struct{}
	current atomic.Int64
	peak    atomic.Int64
	calls   atomic.Int64
}

func newBlockingWSDispatcher() *blockingWSDispatcher {
	return &blockingWSDispatcher{
		started: make(chan struct{}, 64),
		release: make(chan struct{}),
	}
}

func (d *blockingWSDispatcher) RemoveFilterByWs(wsConn) {}

func (d *blockingWSDispatcher) RefreshFilterTimeouts(wsConn) {}

func (d *blockingWSDispatcher) Handle(context.Context, []byte) ([]byte, error) {
	return nil, nil
}

func (d *blockingWSDispatcher) HandleWs(_ []byte, _ wsConn) ([]byte, error) {
	d.calls.Add(1)

	n := d.current.Add(1)

	for {
		peak := d.peak.Load()
		if n <= peak {
			break
		}

		if d.peak.CompareAndSwap(peak, n) {
			break
		}
	}

	select {
	case d.started <- struct{}{}:
	default:
	}

	<-d.release
	d.current.Add(-1)

	return []byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`), nil
}

func newLimitedWSServer(t *testing.T, limiter *wsLimiter, d dispatcher) string {
	t.Helper()

	j := &JSONRPC{
		logger:     hclog.NewNullLogger(),
		config:     &Config{WebSocketReadLimit: 8192},
		dispatcher: d,
		wsLimiter:  limiter,
	}

	srv := httptest.NewServer(http.HandlerFunc(j.handleWs))
	t.Cleanup(srv.Close)

	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()

	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = ws.Close()
	})

	return ws
}

func writeWSRequest(t *testing.T, ws *websocket.Conn, id int) {
	t.Helper()

	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"eth_blockNumber","params":[]}`, id)
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(req)))
}

func readWSResponse(t *testing.T, ws *websocket.Conn) *SuccessResponse {
	t.Helper()

	require.NoError(t, ws.SetReadDeadline(time.Now().UTC().Add(5*time.Second)))

	_, raw, err := ws.ReadMessage()
	require.NoError(t, err)

	var resp SuccessResponse
	require.NoError(t, jsonIt.Unmarshal(raw, &resp))

	return &resp
}

func waitStarted(t *testing.T, d *blockingWSDispatcher, n int) {
	t.Helper()

	deadline := time.Now().UTC().Add(5 * time.Second)

	for i := 0; i < n; i++ {
		select {
		case <-d.started:
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timed out waiting for handler %d of %d to start", i+1, n)
		}
	}
}

func TestWS_PerConnectionInFlightLimitRejectsWithoutSpawning(t *testing.T) {
	t.Parallel()

	d := newBlockingWSDispatcher()
	ws := dialWS(t, newLimitedWSServer(t, newWSLimiter(0, 0, 2), d))

	writeWSRequest(t, ws, 1)
	writeWSRequest(t, ws, 2)
	waitStarted(t, d, 2)

	for id := 3; id <= 5; id++ {
		writeWSRequest(t, ws, id)
	}

	for i := 0; i < 3; i++ {
		resp := readWSResponse(t, ws)
		require.NotNil(t, resp.Error, "request past the per-connection ceiling should be rejected")
		require.Equal(t, -32005, resp.Error.Code)
		require.Contains(t, resp.Error.Message, "in-flight")
	}

	require.EqualValues(t, 2, d.calls.Load(), "rejected frames must not start a handler")
	require.EqualValues(t, 2, d.peak.Load())

	close(d.release)

	for i := 0; i < 2; i++ {
		resp := readWSResponse(t, ws)
		require.Nil(t, resp.Error)
	}
}

func TestWS_GlobalInFlightLimitRejectsAcrossConnections(t *testing.T) {
	t.Parallel()

	d := newBlockingWSDispatcher()
	url := newLimitedWSServer(t, newWSLimiter(0, 2, 8), d)

	a := dialWS(t, url)
	b := dialWS(t, url)

	writeWSRequest(t, a, 1)
	writeWSRequest(t, a, 2)
	waitStarted(t, d, 2)

	writeWSRequest(t, b, 1)

	resp := readWSResponse(t, b)
	require.NotNil(t, resp.Error)
	require.Equal(t, -32005, resp.Error.Code)
	require.EqualValues(t, 2, d.calls.Load())

	close(d.release)

	for i := 0; i < 2; i++ {
		require.Nil(t, readWSResponse(t, a).Error)
	}
}

func TestWS_InFlightSlotIsReusedAfterHandlerReturns(t *testing.T) {
	t.Parallel()

	d := newBlockingWSDispatcher()
	ws := dialWS(t, newLimitedWSServer(t, newWSLimiter(0, 0, 1), d))

	writeWSRequest(t, ws, 1)
	waitStarted(t, d, 1)

	writeWSRequest(t, ws, 2)
	require.Equal(t, -32005, readWSResponse(t, ws).Error.Code)

	close(d.release)
	require.Nil(t, readWSResponse(t, ws).Error)

	d2Release := make(chan struct{})
	d.release = d2Release

	writeWSRequest(t, ws, 3)
	waitStarted(t, d, 1)
	require.EqualValues(t, 2, d.calls.Load(), "a finished handler must free its slot")

	close(d2Release)
	require.Nil(t, readWSResponse(t, ws).Error)
}

func TestWS_ConnectionLimitRejectsHandshake(t *testing.T) {
	t.Parallel()

	limiter := newWSLimiter(2, 0, 0)
	d := newBlockingWSDispatcher()
	url := newLimitedWSServer(t, limiter, d)

	first := dialWS(t, url)
	_ = dialWS(t, url)

	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.EqualValues(t, 2, limiter.connCount())

	require.NoError(t, first.Close())

	waitFor(t, 5*time.Second, "closed connection did not free its slot", func() bool {
		return limiter.connCount() == 1
	})

	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = ws.Close()
	})
}

func TestWS_FailedUpgradeReleasesConnectionSlot(t *testing.T) {
	t.Parallel()

	limiter := newWSLimiter(1, 0, 0)
	j := &JSONRPC{
		logger:     hclog.NewNullLogger(),
		config:     &Config{WebSocketReadLimit: 8192},
		dispatcher: newBlockingWSDispatcher(),
		wsLimiter:  limiter,
	}

	srv := httptest.NewServer(http.HandlerFunc(j.handleWs))
	t.Cleanup(srv.Close)

	httpResp, err := http.Get(srv.URL)
	require.NoError(t, err)

	_ = httpResp.Body.Close()

	require.EqualValues(t, 0, limiter.connCount(), "a failed upgrade must not consume the connection ceiling")

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = ws.Close()
	})
}

// TestWS_RequestFloodDoesNotGrowGoroutines is the acceptance check: one connection
// streaming cheap requests must not create one goroutine per frame.
func TestWS_RequestFloodDoesNotGrowGoroutines(t *testing.T) {
	const (
		perConn = 4
		flood   = 200
	)

	d := newBlockingWSDispatcher()
	ws := dialWS(t, newLimitedWSServer(t, newWSLimiter(0, 0, perConn), d))

	for id := 0; id < flood; id++ {
		writeWSRequest(t, ws, id)
	}

	waitFor(t, 5*time.Second, "handlers did not reach the per-connection ceiling", func() bool {
		return d.peak.Load() == perConn
	})

	require.EqualValues(t, perConn, d.peak.Load())
	require.EqualValues(t, perConn, d.calls.Load(), "a flood must not spawn a handler per frame")

	// the extra frames were rejected from the read loop, so the client sees limit errors
	// rather than hanging until the blocked handlers finish
	rejected := 0

	deadline := time.Now().UTC().Add(5 * time.Second)

	for rejected < flood-perConn {
		require.NoError(t, ws.SetReadDeadline(deadline))

		resp := readWSResponse(t, ws)
		if resp.Error != nil {
			require.Equal(t, -32005, resp.Error.Code)

			rejected++
		}
	}

	close(d.release)
}
