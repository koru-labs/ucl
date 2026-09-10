package jsonrpc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// filterCount reports how many filters the manager is holding, which is what a leak shows up in
func filterCount(m *FilterManager) int {
	m.RLock()
	defer m.RUnlock()

	return len(m.filters)
}

// newTestWSServer starts a web socket endpoint backed by the real handleWs read loop, so that
// connection teardown goes through the same path as in production
func newTestWSServer(t *testing.T, limits FilterLimits) (string, *FilterManager) {
	t.Helper()

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
		jsonRPCBatchLengthLimit: 20,
		blockRangeLimit:         1000,
		filterLimits:            limits,
	})

	j := &JSONRPC{
		logger:     hclog.NewNullLogger(),
		config:     &Config{WebSocketReadLimit: 8192},
		dispatcher: d,
	}

	srv := httptest.NewServer(http.HandlerFunc(j.handleWs))
	t.Cleanup(srv.Close)

	return "ws" + strings.TrimPrefix(srv.URL, "http"), d.filterManager
}

// subscribe issues one eth_subscribe over the connection and returns the response
func subscribe(t *testing.T, ws *websocket.Conn, id int, method string) *SuccessResponse {
	t.Helper()

	params := fmt.Sprintf("[%q]", method)
	if method == "logs" {
		params = fmt.Sprintf("[%q, {}]", method)
	}

	req := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%d,"method":"eth_subscribe","params":%s}`, id, params,
	)

	require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(req)))

	_, raw, err := ws.ReadMessage()
	require.NoError(t, err)

	var resp SuccessResponse
	require.NoError(t, jsonIt.Unmarshal(raw, &resp))

	return &resp
}

// subscribeID issues one eth_subscribe and returns the installed filter ID
func subscribeID(t *testing.T, ws *websocket.Conn, id int, method string) string {
	t.Helper()

	resp := subscribe(t, ws, id, method)
	require.Nil(t, resp.Error)

	var filterID string
	require.NoError(t, jsonIt.Unmarshal(resp.Result, &filterID))

	return filterID
}

// filterExpiry reports when the filter is due to be reaped by the timeout heap
func filterExpiry(t *testing.T, m *FilterManager, id string) time.Time {
	t.Helper()

	m.RLock()
	defer m.RUnlock()

	filter, ok := m.filters[id]
	require.True(t, ok, "filter %s is gone", id)

	return filter.getFilterBase().expiresAt
}

// TestPoC_WSFilterLeakOnDisconnect is the inverted proof of concept. A client opens one
// connection, subscribes in a loop and disconnects; not a single filter may survive it.
func TestPoC_WSFilterLeakOnDisconnect(t *testing.T) {
	t.Parallel()

	const subscriptions = 100

	wsURL, m := newTestWSServer(t, FilterLimits{})

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	// A mix of subscription kinds on purpose. Log filters are the ones that leak for good:
	// they only write when a log matches their query, so a query that matches nothing never
	// fails a write and the closed connection is never noticed.
	kinds := []string{"newHeads", "logs", "newPendingTransactions"}

	for i := 0; i < subscriptions; i++ {
		resp := subscribe(t, ws, i, kinds[i%len(kinds)])
		require.Nil(t, resp.Error, "subscription %d was rejected", i)
	}

	require.Equal(t, subscriptions, filterCount(m), "every subscription should be installed")

	require.NoError(t, ws.Close())

	waitFor(t, 5*time.Second, "filters leaked after the client disconnected", func() bool {
		return filterCount(m) == 0
	})
}

func TestFilterManager_RemoveFilterByWsRemovesEveryFilter(t *testing.T) {
	t.Parallel()

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 1000, FilterLimits{})
	t.Cleanup(m.Close)

	go m.Run()

	conn, _ := newMockWsConnWithMsgCh()

	for i := 0; i < 50; i++ {
		_, err := m.NewBlockFilter(conn)
		require.NoError(t, err)

		_, err = m.NewLogFilter(&LogQuery{}, conn)
		require.NoError(t, err)
	}

	require.Equal(t, 100, filterCount(m))

	m.RemoveFilterByWs(conn)

	require.Zero(t, filterCount(m))
	require.Empty(t, conn.GetFilterIDs(), "connection should no longer reference any filter")
}

func TestFilterManager_PerConnectionLimit(t *testing.T) {
	t.Parallel()

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 1000, FilterLimits{
		PerConnection: 3,
	})
	t.Cleanup(m.Close)

	go m.Run()

	conn, _ := newMockWsConnWithMsgCh()

	for i := 0; i < 3; i++ {
		_, err := m.NewBlockFilter(conn)
		require.NoError(t, err)
	}

	_, err := m.NewBlockFilter(conn)
	require.ErrorIs(t, err, ErrConnectionFilterLimitExceeded)

	// the limit is per connection, so a second client is unaffected by the first one
	other, _ := newMockWsConnWithMsgCh()
	_, err = m.NewBlockFilter(other)
	require.NoError(t, err)

	// and a filter without a connection, created over HTTP, is not subject to it either
	_, err = m.NewBlockFilter(nil)
	require.NoError(t, err)
}

func TestFilterManager_GlobalLimit(t *testing.T) {
	t.Parallel()

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 1000, FilterLimits{
		Global: 4,
	})
	t.Cleanup(m.Close)

	go m.Run()

	conn, _ := newMockWsConnWithMsgCh()

	for i := 0; i < 2; i++ {
		_, err := m.NewBlockFilter(conn)
		require.NoError(t, err)

		_, err = m.NewBlockFilter(nil)
		require.NoError(t, err)
	}

	// the ceiling spans connections and HTTP clients alike, otherwise it would be trivial
	// to sidestep by spreading the filters around
	_, err := m.NewBlockFilter(conn)
	require.ErrorIs(t, err, ErrFilterLimitExceeded)

	_, err = m.NewBlockFilter(nil)
	require.ErrorIs(t, err, ErrFilterLimitExceeded)

	_, err = m.NewLogFilter(&LogQuery{}, nil)
	require.ErrorIs(t, err, ErrFilterLimitExceeded)
}

func TestFilterManager_RejectedFilterLeavesNoTrace(t *testing.T) {
	t.Parallel()

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 1000, FilterLimits{
		PerConnection: 1,
	})
	t.Cleanup(m.Close)

	go m.Run()

	conn, _ := newMockWsConnWithMsgCh()

	accepted, err := m.NewBlockFilter(conn)
	require.NoError(t, err)

	_, err = m.NewBlockFilter(conn)
	require.ErrorIs(t, err, ErrConnectionFilterLimitExceeded)

	// a rejected subscription must not be registered anywhere, or a client hammering the
	// limit would consume its own allowance with filters that do not exist
	require.Equal(t, 1, filterCount(m))
	require.Equal(t, []string{accepted}, conn.GetFilterIDs())
}

func TestFilterManager_UnsubscribeFreesTheAllowance(t *testing.T) {
	t.Parallel()

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 1000, FilterLimits{
		PerConnection: 2,
	})
	t.Cleanup(m.Close)

	go m.Run()

	conn, _ := newMockWsConnWithMsgCh()

	// a client that subscribes and unsubscribes in a loop stays within its allowance
	// indefinitely, which requires the connection's set to shrink on uninstall
	for i := 0; i < 100; i++ {
		id, err := m.NewBlockFilter(conn)
		require.NoError(t, err, "subscription %d was rejected after %d churn cycles", i, i)
		require.True(t, m.Uninstall(id))
	}

	require.Zero(t, filterCount(m))
	require.Empty(t, conn.GetFilterIDs())
}

func TestFilterManager_WSFilterExpiresWhenPeerStopsProvingLiveness(t *testing.T) {
	t.Parallel()

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 1000, FilterLimits{})
	t.Cleanup(m.Close)

	m.timeout = 500 * time.Millisecond

	go m.Run()

	conn, _ := newMockWsConnWithMsgCh()

	// A subscription over a connection that has gone silent has to expire like any other
	// filter. This is what reclaims filters from a peer that vanished without closing its
	// socket, where the read loop never returns and never gets to remove them.
	id, err := m.NewLogFilter(&LogQuery{}, conn)
	require.NoError(t, err)
	require.True(t, m.Exists(id))

	waitFor(t, 5*time.Second, "web socket filter never expired", func() bool {
		return !m.Exists(id)
	})
}

func TestFilterManager_RefreshFilterTimeoutsKeepsSubscriptionAlive(t *testing.T) {
	t.Parallel()

	m := NewFilterManager(hclog.NewNullLogger(), newMockStore(), 1000, FilterLimits{})
	t.Cleanup(m.Close)

	m.timeout = 500 * time.Millisecond

	go m.Run()

	conn, _ := newMockWsConnWithMsgCh()

	id, err := m.NewLogFilter(&LogQuery{}, conn)
	require.NoError(t, err)

	// a peer answering the keepalive pings must keep its subscription, for longer than the
	// timeout would otherwise allow
	deadline := time.Now().UTC().Add(2 * time.Second)
	for time.Now().UTC().Before(deadline) {
		m.RefreshFilterTimeouts(conn)
		require.True(t, m.Exists(id), "a live subscription was reaped")

		time.Sleep(100 * time.Millisecond)
	}
}

func TestWS_PongRefreshesSubscriptionExpiry(t *testing.T) {
	t.Parallel()

	wsURL, m := newTestWSServer(t, FilterLimits{})

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = ws.Close()
	})

	id := subscribeID(t, ws, 1, "newHeads")
	before := filterExpiry(t, m, id)

	// Every subscription is on the timeout heap, so a live one only stays installed because
	// the peer keeps answering the keepalive. Send the pong a conformant client would send
	// and check the server really does push the expiry back.
	require.NoError(t, ws.WriteControl(
		websocket.PongMessage, nil, time.Now().UTC().Add(time.Second),
	))

	waitFor(t, 5*time.Second, "pong did not refresh the subscription expiry", func() bool {
		return filterExpiry(t, m, id).After(before)
	})
}

func TestWS_SubscribeBeyondLimitReturnsClearError(t *testing.T) {
	t.Parallel()

	wsURL, m := newTestWSServer(t, FilterLimits{PerConnection: 2})

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = ws.Close()
	})

	for i := 0; i < 2; i++ {
		require.Nil(t, subscribe(t, ws, i, "newHeads").Error)
	}

	resp := subscribe(t, ws, 2, "newHeads")

	require.NotNil(t, resp.Error, "subscription past the limit should be rejected")
	require.Equal(t, -32005, resp.Error.Code)
	require.Contains(t, resp.Error.Message, "filter limit")

	require.Equal(t, 2, filterCount(m))
}
