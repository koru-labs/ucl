package jsonrpc

import (
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWSConn stands in for *websocket.Conn so the write path can be driven deterministically,
// without having to fill a real TCP send window.
type fakeWSConn struct {
	mu        sync.Mutex
	deadlines []time.Time
	writes    [][]byte
	writeErr  error

	// release gates WriteMessage. When nil, writes complete immediately.
	release chan struct{}

	// writeEntered is signalled every time WriteMessage is entered
	writeEntered chan struct{}

	pings   atomic.Int64
	pingErr error
	pingMu  sync.Mutex
	closed  atomic.Bool
}

func newFakeWSConn() *fakeWSConn {
	return &fakeWSConn{
		writeEntered: make(chan struct{}, 1),
	}
}

func (f *fakeWSConn) SetWriteDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deadlines = append(f.deadlines, t)

	return nil
}

func (f *fakeWSConn) WriteMessage(_ int, data []byte) error {
	select {
	case f.writeEntered <- struct{}{}:
	default:
	}

	if f.release != nil {
		<-f.release
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.writeErr != nil {
		return f.writeErr
	}

	f.writes = append(f.writes, data)

	return nil
}

func (f *fakeWSConn) WriteControl(messageType int, _ []byte, _ time.Time) error {
	if messageType == websocket.PingMessage {
		f.pings.Add(1)
	}

	f.pingMu.Lock()
	defer f.pingMu.Unlock()

	return f.pingErr
}

func (f *fakeWSConn) setPingErr(err error) {
	f.pingMu.Lock()
	defer f.pingMu.Unlock()

	f.pingErr = err
}

func (f *fakeWSConn) pingCount() int64 {
	return f.pings.Load()
}

func (f *fakeWSConn) Close() error {
	f.closed.Store(true)

	return nil
}

func (f *fakeWSConn) snapshot() ([]time.Time, [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]time.Time{}, f.deadlines...), append([][]byte{}, f.writes...)
}

func (f *fakeWSConn) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.writes)
}

// waitFor polls cond until it holds or the timeout elapses
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()

	deadline := time.Now().UTC().Add(timeout)

	for time.Now().UTC().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal(msg)
}

func TestWSWrapper_SetsDeadlineForEveryWrite(t *testing.T) {
	t.Parallel()

	conn := newFakeWSConn()
	wrapper := newWSWrapper(conn, hclog.NewNullLogger(), wsPingPeriod)

	t.Cleanup(func() {
		_ = wrapper.Close()
	})

	before := time.Now().UTC()

	for i := 0; i < 3; i++ {
		require.NoError(t, wrapper.WriteMessage(websocket.TextMessage, []byte("payload")))
	}

	waitFor(t, 5*time.Second, "writer did not drain the queue", func() bool {
		return conn.writeCount() == 3
	})

	deadlines, writes := conn.snapshot()

	// every single write must be bounded by a deadline, otherwise a peer that stops
	// reading its socket blocks the writer forever
	require.Len(t, writes, 3)
	require.Len(t, deadlines, 3)

	for _, deadline := range deadlines {
		assert.WithinRange(t,
			deadline,
			before.Add(wsWriteDeadline),
			time.Now().UTC().Add(wsWriteDeadline),
		)
	}
}

func TestWSWrapper_ClosesConnectionOnWriteError(t *testing.T) {
	t.Parallel()

	conn := newFakeWSConn()
	conn.writeErr = os.ErrDeadlineExceeded

	wrapper := newWSWrapper(conn, hclog.NewNullLogger(), wsPingPeriod)

	t.Cleanup(func() {
		_ = wrapper.Close()
	})

	require.NoError(t, wrapper.WriteMessage(websocket.TextMessage, []byte("payload")))

	// a write that exceeds its deadline must drop the connection, which in turn unblocks
	// the read loop so the connection's filters get cleaned up
	waitFor(t, 5*time.Second, "connection was not closed after a write timeout", conn.closed.Load)

	assert.ErrorIs(t, wrapper.WriteMessage(websocket.TextMessage, []byte("payload")), net.ErrClosed)
}

func TestWSWrapper_WriteMessageDoesNotBlockOnStalledPeer(t *testing.T) {
	t.Parallel()

	conn := newFakeWSConn()
	conn.release = make(chan struct{})

	wrapper := newWSWrapper(conn, hclog.NewNullLogger(), wsPingPeriod)

	t.Cleanup(func() {
		close(conn.release)

		_ = wrapper.Close()
	})

	// the first message is picked up by the writer, which then stalls inside the connection
	require.NoError(t, wrapper.WriteMessage(websocket.TextMessage, []byte("payload")))

	select {
	case <-conn.writeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer never reached the connection")
	}

	// with the writer stalled, the queue absorbs exactly its capacity
	for i := 0; i < wsOutboundQueueSize; i++ {
		require.NoErrorf(t, wrapper.WriteMessage(websocket.TextMessage, []byte("payload")),
			"queue rejected message %d, expected it to hold %d", i, wsOutboundQueueSize)
	}

	// and then rejects instead of blocking, so the caller can evict the peer
	done := make(chan error, 1)

	go func() {
		done <- wrapper.WriteMessage(websocket.TextMessage, []byte("payload"))
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, errWSWriteQueueFull)
	case <-time.After(5 * time.Second):
		t.Fatal("WriteMessage blocked on a stalled peer")
	}
}

func TestWSWrapper_CloseStopsWriter(t *testing.T) {
	t.Parallel()

	conn := newFakeWSConn()
	wrapper := newWSWrapper(conn, hclog.NewNullLogger(), wsPingPeriod)

	require.NoError(t, wrapper.Close())
	assert.True(t, conn.closed.Load())

	// Close is idempotent, since both the read loop and the writer can trigger it
	require.NoError(t, wrapper.Close())

	assert.ErrorIs(t, wrapper.WriteMessage(websocket.TextMessage, []byte("payload")), net.ErrClosed)

	time.Sleep(50 * time.Millisecond)

	assert.Zero(t, conn.writeCount(), "writer kept writing after the connection was closed")
}

func TestWSWrapper_FilterIDsAreConcurrencySafe(t *testing.T) {
	t.Parallel()

	wrapper := newWSWrapper(newFakeWSConn(), hclog.NewNullLogger(), wsPingPeriod)

	t.Cleanup(func() {
		_ = wrapper.Close()
	})

	var wg sync.WaitGroup

	// the filter set is written by the request handler goroutines and read by the
	// FilterManager goroutine, so it needs synchronization
	wg.Add(3)

	go func() {
		defer wg.Done()

		for i := 0; i < 100; i++ {
			wrapper.AddFilterID(strconv.Itoa(i))
		}
	}()

	go func() {
		defer wg.Done()

		for i := 0; i < 100; i++ {
			wrapper.RemoveFilterID(strconv.Itoa(i))
		}
	}()

	go func() {
		defer wg.Done()

		for i := 0; i < 100; i++ {
			_ = wrapper.GetFilterIDs()
		}
	}()

	wg.Wait()
}

func TestWSWrapper_TracksEveryFilterOfTheConnection(t *testing.T) {
	t.Parallel()

	wrapper := newWSWrapper(newFakeWSConn(), hclog.NewNullLogger(), wsPingPeriod)

	t.Cleanup(func() {
		_ = wrapper.Close()
	})

	for i := 0; i < 10; i++ {
		wrapper.AddFilterID(strconv.Itoa(i))
	}

	require.Len(t, wrapper.GetFilterIDs(), 10, "connection must remember every subscription")

	// an unsubscribe has to shrink the set, otherwise a client that subscribes and
	// unsubscribes in a loop would exhaust its own allowance
	wrapper.RemoveFilterID("4")
	require.Len(t, wrapper.GetFilterIDs(), 9)
	require.NotContains(t, wrapper.GetFilterIDs(), "4")
}

func TestWSWrapper_PingsThePeerPeriodically(t *testing.T) {
	t.Parallel()

	conn := newFakeWSConn()
	// pings are what stop a live subscription from being reaped by the filter timeout, so
	// the keepalive has to keep firing on its own for as long as the connection is up
	wrapper := newWSWrapper(conn, hclog.NewNullLogger(), 5*time.Millisecond)

	t.Cleanup(func() {
		_ = wrapper.Close()
	})

	waitFor(t, 2*time.Second, "writer did not keep pinging the peer", func() bool {
		return conn.pingCount() >= 3
	})
}

func TestWSWrapper_ClosesConnectionWhenPingFails(t *testing.T) {
	t.Parallel()

	conn := newFakeWSConn()
	conn.setPingErr(os.ErrDeadlineExceeded)

	// a peer that has stopped reading also stops absorbing pings, so a ping that cannot be
	// written has to tear the connection down instead of being retried forever
	wrapper := newWSWrapper(conn, hclog.NewNullLogger(), 5*time.Millisecond)

	t.Cleanup(func() {
		_ = wrapper.Close()
	})

	waitFor(t, 2*time.Second, "failed ping did not close the connection", func() bool {
		return conn.closed.Load()
	})
}

// *websocket.Conn must keep satisfying the interface the wrapper writes through
var _ wsConnection = (*websocket.Conn)(nil)
