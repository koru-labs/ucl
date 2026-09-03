package jsonrpc

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/txpool/proto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_GetLogsForQuery(t *testing.T) {
	t.Parallel()

	blockHash := types.StringToHash("1")

	// Topics we're searching for
	topic1 := types.StringToHash("4")
	topic2 := types.StringToHash("5")
	topic3 := types.StringToHash("6")

	var topics = [][]types.Hash{{topic1}, {topic2}, {topic3}}

	testTable := []struct {
		name           string
		query          *LogQuery
		expectedLength int
		expectedError  error
	}{
		{
			"Found matching logs, fromBlock < toBlock",
			&LogQuery{
				fromBlock: 1,
				toBlock:   3,
				Topics:    topics,
			},
			3,
			nil,
		},
		{
			"Found matching logs, fromBlock == toBlock",
			&LogQuery{
				fromBlock: 2,
				toBlock:   2,
				Topics:    topics,
			},
			1,
			nil,
		},
		{
			"Found matching logs, BlockHash present",
			&LogQuery{
				BlockHash: &blockHash,
				Topics:    topics,
			},
			1,
			nil,
		},
		{
			"No logs found",
			&LogQuery{
				fromBlock: 4,
				toBlock:   5,
				Topics:    topics,
			},
			0,
			nil,
		},
		{
			"Invalid block range",
			&LogQuery{
				fromBlock: 10,
				toBlock:   5,
				Topics:    topics,
			},
			0,
			ErrIncorrectBlockRange,
		},
		{
			"Block range too high",
			&LogQuery{
				fromBlock: 10,
				toBlock:   1012,
				Topics:    topics,
			},
			0,
			ErrBlockRangeTooHigh,
		},
	}

	// setup test
	store := &mockBlockStore{
		topics: []types.Hash{topic1, topic2, topic3},
	}
	store.setupLogs()

	blocks := make([]*types.Block, 5)

	for i := range blocks {
		blocks[i] = &types.Block{
			Header: &types.Header{
				Number: uint64(i),
				Hash:   types.StringToHash(strconv.Itoa(i)),
			},
			Transactions: []*types.Transaction{
				{
					Value: big.NewInt(10),
				},
				{
					Value: big.NewInt(11),
				},
				{
					Value: big.NewInt(12),
				},
			},
		}
	}

	store.appendBlocksToStore(blocks)

	f := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})

	t.Cleanup(func() {
		defer f.Close()
	})

	for _, testCase := range testTable {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			foundLogs, logError := f.GetLogsForQuery(testCase.query)

			if testCase.expectedError != nil {
				assert.ErrorIs(t, logError, testCase.expectedError)

				return
			}

			assert.NoError(t, logError)
			assert.Lenf(t, foundLogs, testCase.expectedLength, "Invalid number of logs found")
		})
	}
}

func Test_getLogsFromBlock(t *testing.T) {
	t.Parallel()

	numOfLogs := 4

	block := &types.Block{
		Header: &types.Header{Hash: types.StringToHash("someHash"), Number: 1},
		Transactions: []*types.Transaction{
			createTestTransaction(types.StringToHash("tx1")),
			createTestTransaction(types.StringToHash("tx2")),
			createTestTransaction(types.StringToHash("tx3")),
		},
	}

	// setup test with block with 3 transactions and 4 logs
	store := &mockBlockStore{receipts: map[types.Hash][]*types.Receipt{
		block.Header.Hash: {
			{
				// transaction 1 logs
				Logs: []*types.Log{
					{
						Topics: []types.Hash{
							hash1,
						},
					},
					{
						Topics: []types.Hash{
							hash2,
						},
					},
				},
			},
			{
				// transaction 2 logs
				Logs: []*types.Log{
					{
						Topics: []types.Hash{
							hash3,
						},
					},
				},
			},
			{
				// transaction 3 logs
				Logs: []*types.Log{
					{
						Topics: []types.Hash{
							hash4,
						},
					},
				},
			},
		},
	}}

	store.appendBlocksToStore([]*types.Block{block})

	f := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})

	t.Cleanup(func() {
		defer f.Close()
	})

	logs, err := f.getLogsFromBlock(&LogQuery{
		fromBlock: 1,
		toBlock:   1,
	}, block)

	require.NoError(t, err)
	require.Len(t, logs, numOfLogs)

	for i := 0; i < numOfLogs; i++ {
		require.Equal(t, uint64(i), uint64(logs[i].LogIndex))
	}
}

func Test_GetLogFilterFromID(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	go m.Run()

	logFilter := &LogQuery{
		Addresses: []types.Address{addr1},
		toBlock:   10,
		fromBlock: 0,
	}

	id, err := m.NewLogFilter(logFilter, &MockClosedWSConnection{})
	require.NoError(t, err)

	retrivedLogFilter, err := m.GetLogFilterFromID(id)
	assert.NoError(t, err)
	assert.Equal(t, logFilter, retrivedLogFilter.query)
}

func TestFilterLog(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	go m.Run()

	id, err := m.NewLogFilter(&LogQuery{
		Topics: [][]types.Hash{
			{hash1},
		},
	}, nil)
	require.NoError(t, err)

	store.emitEvent(&mockEvent{
		NewChain: []*mockHeader{
			{
				header: &types.Header{
					Hash: hash1,
				},
				receipts: []*types.Receipt{
					{
						Logs: []*types.Log{
							{
								Topics: []types.Hash{
									hash1,
								},
							},
						},
						TxHash: hash3,
					},
				},
			},
		},
		OldChain: []*mockHeader{
			{
				header: &types.Header{
					Hash: hash2,
				},
				receipts: []*types.Receipt{
					{
						Logs: []*types.Log{
							{
								Topics: []types.Hash{
									hash1,
								},
							},
						},
						TxHash: hash3,
					},
				},
			},
		},
	})

	time.Sleep(500 * time.Millisecond)

	if _, fetchErr := m.GetFilterChanges(id); fetchErr != nil {
		t.Fatalf("Unable to get filter changes, %v", fetchErr)
	}
}

func TestFilterBlock(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	go m.Run()

	// add block filter
	id, err := m.NewBlockFilter(nil)
	require.NoError(t, err)

	// emit two events
	store.emitEvent(&mockEvent{
		NewChain: []*mockHeader{
			{
				header: &types.Header{
					Hash: types.StringToHash("1"),
				},
			},
			{
				header: &types.Header{
					Hash: types.StringToHash("2"),
				},
			},
		},
	})

	store.emitEvent(&mockEvent{
		NewChain: []*mockHeader{
			{
				header: &types.Header{
					Hash: types.StringToHash("3"),
				},
			},
		},
	})

	// we need to wait for the manager to process the data
	time.Sleep(500 * time.Millisecond)

	if _, fetchErr := m.GetFilterChanges(id); fetchErr != nil {
		t.Fatalf("Unable to get filter changes, %v", fetchErr)
	}

	// emit one more event, it should not return the
	// first three hashes
	store.emitEvent(&mockEvent{
		NewChain: []*mockHeader{
			{
				header: &types.Header{
					Hash: types.StringToHash("4"),
				},
			},
		},
	})

	time.Sleep(500 * time.Millisecond)

	if _, fetchErr := m.GetFilterChanges(id); fetchErr != nil {
		t.Fatalf("Unable to get filter changes, %v", fetchErr)
	}
}

func TestFilterPendingTx(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	go m.Run()

	// add pending tx filter
	id, err := m.NewPendingTxFilter(nil)
	require.NoError(t, err)

	// emit two events
	store.emitTxPoolEvent(proto.EventType_ADDED, "evt1")
	store.emitTxPoolEvent(proto.EventType_ADDED, "evt2")

	// we need to wait for the manager to process the data
	time.Sleep(500 * time.Millisecond)

	var res interface{}

	var fetchErr error

	if res, fetchErr = m.GetFilterChanges(id); fetchErr != nil {
		t.Fatalf("Unable to get filter changes, %v", fetchErr)
	}

	txHashes, ok := res.([]string)
	require.True(t, ok)
	require.Equal(t, 2, len(txHashes))
	require.Equal(t, "evt1", txHashes[0])
	require.Equal(t, "evt2", txHashes[1])

	// emit one more event, it should not return the
	// first two hashes
	store.emitTxPoolEvent(proto.EventType_ADDED, "evt3")
	time.Sleep(500 * time.Millisecond)

	if res, fetchErr = m.GetFilterChanges(id); fetchErr != nil {
		t.Fatalf("Unable to get filter changes, %v", fetchErr)
	}

	txHashes, ok = res.([]string)
	require.True(t, ok)
	require.NotNil(t, txHashes)
	require.Equal(t, 1, len(txHashes))
	require.Equal(t, "evt3", txHashes[0])
}

func TestFilterTimeout(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	m.timeout = 2 * time.Second

	go m.Run()

	// add block filter
	id, err := m.NewBlockFilter(nil)
	require.NoError(t, err)

	assert.True(t, m.Exists(id))
	time.Sleep(3 * time.Second)
	assert.False(t, m.Exists(id))
}

func TestRemoveFilterByWebsocket(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	mock, _ := newMockWsConnWithMsgCh()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	go m.Run()

	id, err := m.NewBlockFilter(mock)
	require.NoError(t, err)

	m.RemoveFilterByWs(mock)

	// false because filter was removed
	assert.False(t, m.Exists(id))
}

func Test_flushWsFilters(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})

	t.Cleanup(func() {
		m.Close()
	})

	go m.Run()

	runTest := func(t *testing.T, flushErr error, shouldExist bool) {
		t.Helper()

		mock := &mockWsConn{
			WriteMessageFn: func(i int, b []byte) error {
				return flushErr
			},
		}

		id, err := m.NewBlockFilter(mock)
		require.NoError(t, err)

		// emit event
		store.emitEvent(&mockEvent{
			NewChain: []*mockHeader{
				{
					header: &types.Header{
						Hash: types.StringToHash("1"),
					},
				},
			},
		})

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				t.Errorf("timeout for filter existence check, expected=%t, actual=%t", shouldExist, m.Exists(id))

				return
			default:
				if shouldExist == m.Exists(id) {
					return
				}
			}
		}
	}

	t.Run("should remove if sendUpdates returns websocket.ErrCloseSent", func(t *testing.T) {
		t.Parallel()

		runTest(t, websocket.ErrCloseSent, false)
	})

	t.Run("should remove if sendUpdates returns net.ErrClosed", func(t *testing.T) {
		t.Parallel()

		runTest(t, net.ErrClosed, false)
	})

	t.Run("should keep if sendUpdates returns unknown error", func(t *testing.T) {
		t.Parallel()

		runTest(t, errors.New("hoge"), true)
	})

	t.Run("should keep if sendUpdates doesn't return error", func(t *testing.T) {
		t.Parallel()

		runTest(t, nil, true)
	})

	t.Run("should remove if sendUpdates returns errWSWriteQueueFull", func(t *testing.T) {
		t.Parallel()

		runTest(t, errWSWriteQueueFull, false)
	})
}

// newBlockEvent builds the minimal chain event that makes the FilterManager flush block filters
func newBlockEvent(hash string) *mockEvent {
	return &mockEvent{
		NewChain: []*mockHeader{
			{
				header: &types.Header{
					Hash: types.StringToHash(hash),
				},
			},
		},
	}
}

// Test_flushWsFilters_evictsPeerWithFullQueue asserts that a peer which has fallen so far
// behind that its outbound queue overflowed loses its subscription and its connection,
// rather than being buffered for indefinitely.
func Test_flushWsFilters_evictsPeerWithFullQueue(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})

	t.Cleanup(m.Close)

	go m.Run()

	stalled := &mockWsConn{
		WriteMessageFn: func(int, []byte) error {
			return errWSWriteQueueFull
		},
	}

	id, err := m.NewBlockFilter(stalled)
	require.NoError(t, err)

	store.emitEvent(newBlockEvent("1"))

	waitFor(t, 5*time.Second, "filter of the evicted peer was not removed", func() bool {
		return !m.Exists(id)
	})

	assert.True(t, stalled.isClosed(), "peer connection was not closed after its queue overflowed")
}

// Test_flushWsFilters_holdsNoLockAcrossWrite is the regression test for the deadlock where a
// single unresponsive WS peer wedged every FilterManager operation: the flush used to write to
// the peer while holding the read lock, and Go's RWMutex starves new readers as soon as a
// writer queues up.
func Test_flushWsFilters_holdsNoLockAcrossWrite(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})

	t.Cleanup(m.Close)

	go m.Run()

	var (
		releaseWrite = make(chan struct{})
		inWrite      = make(chan struct{}, 1)
	)

	stalled := &mockWsConn{
		WriteMessageFn: func(int, []byte) error {
			select {
			case inWrite <- struct{}{}:
			default:
			}

			<-releaseWrite

			return nil
		},
	}

	stalledID, err := m.NewBlockFilter(stalled)
	require.NoError(t, err)

	defer close(releaseWrite)

	store.emitEvent(newBlockEvent("1"))

	select {
	case <-inWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("flush never reached the peer write")
	}

	// While the write is stuck, both read-lock and write-lock operations must still complete
	done := make(chan struct{})

	go func() {
		defer close(done)

		m.Exists(stalledID)                         // read lock
		otherID, _ := m.NewBlockFilter(&mockWsConn{ // write lock
			WriteMessageFn: func(int, []byte) error { return nil },
		})
		m.Uninstall(otherID) // write lock
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FilterManager operations blocked while a write to a peer was in flight")
	}
}

func TestFilterWebsocket(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	mock, msgCh := newMockWsConnWithMsgCh()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	go m.Run()

	id, err := m.NewBlockFilter(mock)
	require.NoError(t, err)

	// we cannot call get filter changes for a websocket filter
	_, err = m.GetFilterChanges(id)
	assert.Equal(t, err, ErrWSFilterDoesNotSupportGetChanges)

	// emit event
	store.emitEvent(&mockEvent{
		NewChain: []*mockHeader{
			{
				header: &types.Header{
					Hash: types.StringToHash("1"),
				},
			},
		},
	})

	select {
	case <-msgCh:
	case <-time.After(2 * time.Second):
		t.Fatal("no new block events received in the predefined time slot")
	}
}

func TestFilterPendingTxWebsocket(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	mock, msgCh := newMockWsConnWithMsgCh()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	go m.Run()

	id, err := m.NewPendingTxFilter(mock)
	require.NoError(t, err)

	// we cannot call get filter changes for a websocket filter
	_, err = m.GetFilterChanges(id)
	assert.Equal(t, err, ErrWSFilterDoesNotSupportGetChanges)

	// emit event
	store.emitTxPoolEvent(proto.EventType_ADDED, "evt1")

	select {
	case <-msgCh:
	case <-time.After(2 * time.Second):
		t.Fatal("no tx pool events received in the predefined time slot")
	}
}

type mockWsConn struct {
	WriteMessageFn func(int, []byte) error

	filterMux sync.Mutex
	filterIDs map[string]struct{}

	closed atomic.Bool
}

func (m *mockWsConn) AddFilterID(id string) {
	m.filterMux.Lock()
	defer m.filterMux.Unlock()

	if m.filterIDs == nil {
		m.filterIDs = make(map[string]struct{})
	}

	m.filterIDs[id] = struct{}{}
}

func (m *mockWsConn) RemoveFilterID(id string) {
	m.filterMux.Lock()
	defer m.filterMux.Unlock()

	delete(m.filterIDs, id)
}

func (m *mockWsConn) GetFilterIDs() []string {
	m.filterMux.Lock()
	defer m.filterMux.Unlock()

	ids := make([]string, 0, len(m.filterIDs))

	for id := range m.filterIDs {
		ids = append(ids, id)
	}

	return ids
}

func (m *mockWsConn) WriteMessage(messageType int, b []byte) error {
	return m.WriteMessageFn(messageType, b)
}

func (m *mockWsConn) Close() error {
	m.closed.Store(true)

	return nil
}

func (m *mockWsConn) isClosed() bool {
	return m.closed.Load()
}

func newMockWsConnWithMsgCh() (*mockWsConn, <-chan []byte) {
	msgCh := make(chan []byte, 1)

	mock := &mockWsConn{
		WriteMessageFn: func(i int, b []byte) error {
			msgCh <- b

			return nil
		},
	}

	return mock, msgCh
}

func TestHeadStream_Basic(t *testing.T) {
	t.Parallel()

	b := newBlockStream(&block{header: header{Hash: types.StringToHash("1")}})

	b.push(&block{header: header{Hash: types.StringToHash("2")}})
	cur := b.getHead()

	b.push(&block{header: header{Hash: types.StringToHash("3")}})
	b.push(&block{header: header{Hash: types.StringToHash("4")}})

	// get the updates, there are two new entries
	updates, next := cur.getUpdates()

	assert.Equal(t, updates[0].Hash.String(), types.StringToHash("3").String())
	assert.Equal(t, updates[1].Hash.String(), types.StringToHash("4").String())

	// there are no new entries
	updates, _ = next.getUpdates()
	assert.Len(t, updates, 0)
}

func TestHeadStream_Concurrent(t *testing.T) {
	t.Parallel()

	nReaders := 20
	nMessages := 10

	b := newBlockStream(&block{header: header{Number: 0}})

	// All subscribers start from the same point
	head := b.getHead()

	// Write co-routine with jitter
	go func() {
		seed := time.Now().UTC().UnixNano()
		t.Logf("Using seed %d", seed)

		z := rand.NewZipf(rand.New(rand.NewSource(seed)), 1.5, 1.5, 50)

		for i := 0; i < nMessages; i++ {
			b.push(&block{header: header{Number: argUint64(i)}})

			wait := time.Duration(z.Uint64()) * time.Millisecond
			time.Sleep(wait)
		}
	}()

	// Run n subscribers following and verifying
	errCh := make(chan error, nReaders)

	for i := 0; i < nReaders; i++ {
		go func(i int) {
			item := head
			expect := uint64(0)

			for {
				blocks, next := item.getUpdates()

				for _, block := range blocks {
					if num := uint64(block.Number); num != expect {
						errCh <- fmt.Errorf("subscriber %05d bad event want=%d, got=%d", i, expect, num)

						return
					}

					expect++

					if expect == uint64(nMessages) {
						// Succeeded
						errCh <- nil

						return
					}
				}

				item = next
			}
		}(i)
	}

	for i := 0; i < nReaders; i++ {
		err := <-errCh
		assert.NoError(t, err)
	}
}

type MockClosedWSConnection struct{}

func (m *MockClosedWSConnection) AddFilterID(_id string) {}

func (m *MockClosedWSConnection) RemoveFilterID(_id string) {}

func (m *MockClosedWSConnection) GetFilterIDs() []string {
	return nil
}

func (m *MockClosedWSConnection) WriteMessage(_messageType int, _data []byte) error {
	return websocket.ErrCloseSent
}

func (m *MockClosedWSConnection) Close() error {
	return nil
}

func TestClosedFilterDeletion(t *testing.T) {
	t.Parallel()

	store := newMockStore()

	m := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	defer m.Close()

	go m.Run()

	// add block filter
	id, err := m.NewBlockFilter(&MockClosedWSConnection{})
	require.NoError(t, err)

	assert.True(t, m.Exists(id))

	// event is sent to the filter but writing to connection should fail
	err = m.dispatchEvent(&blockchain.Event{
		NewChain: []*types.Header{
			{
				Hash: types.StringToHash("1"),
			},
		},
	})

	// should not return error when the error is websocket.ErrCloseSen because filter is removed instead
	assert.NoError(t, err)

	// false because filter was removed automatically
	assert.False(t, m.Exists(id))
}

func Test_appendLogsToFilters(t *testing.T) {
	t.Parallel()

	numOfLogs := 4

	block := &types.Block{
		Header: &types.Header{Hash: types.StringToHash("someHash"), Number: 1},
		Transactions: []*types.Transaction{
			createTestTransaction(types.StringToHash("tx1")),
			createTestTransaction(types.StringToHash("tx2")),
			createTestTransaction(types.StringToHash("tx3")),
		},
	}

	// setup test with block with 3 transactions and 4 logs
	store := &mockBlockStore{
		receipts: map[types.Hash][]*types.Receipt{
			block.Header.Hash: {
				{
					// transaction 1 logs
					Logs: []*types.Log{
						{
							Topics: []types.Hash{
								hash1,
							},
						},
						{
							Topics: []types.Hash{
								hash2,
							},
						},
					},
				},
				{
					// transaction 2 logs
					Logs: []*types.Log{
						{
							Topics: []types.Hash{
								hash3,
							},
						},
					},
				},
				{
					// transaction 3 logs
					Logs: []*types.Log{
						{
							Topics: []types.Hash{
								hash4,
							},
						},
					},
				},
			},
		}}

	store.appendBlocksToStore([]*types.Block{block})

	f := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})

	logFilter := &logFilter{
		filterBase: newFilterBase(nil),
		query: &LogQuery{
			fromBlock: 1,
			toBlock:   1,
		},
	}

	f.filters = map[string]filter{
		"test": logFilter,
	}

	t.Cleanup(func() {
		defer f.Close()
	})

	txs := []*types.Transaction{
		createTestTransaction(types.StringToHash("tx1")),
		createTestTransaction(types.StringToHash("tx2")),
		createTestTransaction(types.StringToHash("tx3")),
	}

	b := toBlock(&types.Block{Header: block.Header, Transactions: txs}, false)
	err := f.appendLogsToFilters(b)

	require.NoError(t, err)
	require.Len(t, logFilter.logs, numOfLogs)

	for i := 0; i < numOfLogs; i++ {
		require.Equal(t, uint64(i), uint64(logFilter.logs[i].LogIndex))
	}
}
