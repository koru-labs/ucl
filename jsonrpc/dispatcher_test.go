package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/txpool/proto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	oneEther = new(big.Int).Mul(
		big.NewInt(1),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
)

func toArgUint64Ptr(value uint64) *argUint64 {
	argValue := argUint64(value)

	return &argValue
}

func toArgBytesPtr(value []byte) *argBytes {
	argValue := argBytes(value)

	return &argValue
}

func expectJSONResult(data []byte, v interface{}) error {
	var resp SuccessResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}

	if resp.Error != nil {
		return resp.Error
	}

	if err := json.Unmarshal(resp.Result, v); err != nil {
		return err
	}

	return nil
}

func expectBatchJSONResult(data []byte, v interface{}) error {
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}

	return nil
}

func TestDispatcher_HandleWebsocketConnection_EthSubscribe(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	dispatcher := newTestDispatcher(t,
		hclog.NewNullLogger(),
		store,
		&dispatcherParams{
			chainID:                 0,
			priceLimit:              0,
			jsonRPCBatchLengthLimit: 20,
			blockRangeLimit:         1000,
		},
	)

	t.Run("clients should be able to receive \"newHeads\" event through eth_subscribe", func(t *testing.T) {
		t.Parallel()

		mockConnection, msgCh := newMockWsConnWithMsgCh()

		req := []byte(`{
		"method": "eth_subscribe",
		"params": ["newHeads"]
	}`)
		_, err := dispatcher.HandleWs(req, mockConnection)
		require.NoError(t, err)

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
			t.Fatal("\"newHeads\" event not received in 2 seconds")
		}
	})

	t.Run("clients should be able to receive \"newPendingTransactions\" event through eth_subscribe", func(t *testing.T) {
		t.Parallel()

		mockConnection, msgCh := newMockWsConnWithMsgCh()

		req := []byte(`{
		"method": "eth_subscribe",
		"params": ["newPendingTransactions"]
	}`)
		_, err := dispatcher.HandleWs(req, mockConnection)
		require.NoError(t, err)

		store.emitTxPoolEvent(proto.EventType_ADDED, "evt1")

		select {
		case <-msgCh:
		case <-time.After(2 * time.Second):
			t.Fatal("\"newPendingTransactions\" event not received in 2 seconds")
		}
	})
}

func TestDispatcher_WebsocketConnection_RequestFormats(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	dispatcher := newTestDispatcher(t,
		hclog.NewNullLogger(),
		store,
		&dispatcherParams{
			chainID:                 0,
			priceLimit:              0,
			jsonRPCBatchLengthLimit: 20,
			blockRangeLimit:         1000,
		},
	)

	mockConnection, _ := newMockWsConnWithMsgCh()

	cases := []struct {
		msg         []byte
		expectError bool
	}{
		{
			[]byte(`{
				"method": "eth_subscribe",
				"params": ["newHeads"],
				"id": "abc"
			}`),
			false,
		},
		{
			[]byte(`{
				"method": "eth_subscribe",
				"params": ["newHeads"],
				"id": null
			}`),
			false,
		},
		{
			[]byte(`{
				"method": "eth_subscribe",
				"params": ["newHeads"],
				"id": 2.1
			}`),
			true,
		},
		{
			[]byte(`{
				"method": "eth_subscribe",
				"params": ["newHeads"]
			}`),
			false,
		},
		{
			[]byte(`{
				"method": "eth_subscribe",
				"params": ["newHeads"],
				"id": 2.0
			}`),
			false,
		},
	}
	for _, c := range cases {
		data, err := dispatcher.HandleWs(c.msg, mockConnection)
		resp := new(SuccessResponse)

		merr := json.Unmarshal(data, resp)
		if merr != nil {
			t.Fatal("Invalid response")
		}

		if !c.expectError && (resp.Error != nil || err != nil) {
			t.Fatal("Error unexpected but found")
		}

		if c.expectError && (resp.Error == nil && err == nil) {
			t.Fatal("Error expected but not found")
		}
	}
}

type mockService struct {
	msgCh chan interface{}
}

func (m *mockService) Block(f BlockNumber) (interface{}, error) {
	m.msgCh <- f

	return nil, nil
}

func (m *mockService) Type(addr types.Address) (interface{}, error) {
	m.msgCh <- addr

	return nil, nil
}

func (m *mockService) BlockPtr(_ string, f *BlockNumber) (interface{}, error) {
	if f == nil {
		m.msgCh <- nil
	} else {
		m.msgCh <- *f
	}

	return nil, nil
}

func (m *mockService) Filter(f LogQuery) (interface{}, error) {
	m.msgCh <- f

	return nil, nil
}

func TestDispatcherFuncDecode(t *testing.T) {
	t.Parallel()

	srv := &mockService{msgCh: make(chan interface{}, 10)}

	dispatcher := newTestDispatcher(t,
		hclog.NewNullLogger(),
		newMockStore(),
		&dispatcherParams{
			chainID:                 0,
			priceLimit:              0,
			jsonRPCBatchLengthLimit: 20,
			blockRangeLimit:         1000,
		},
	)

	require.NoError(t, dispatcher.registerService("mock", srv))

	handleReq := func(typ string, msg string) interface{} {
		_, err := dispatcher.handleReq(context.Background(), Request{
			Method: "mock_" + typ,
			Params: []byte(msg),
		})
		assert.NoError(t, err)

		return <-srv.msgCh
	}

	addr1 := types.Address{0x1}

	cases := []struct {
		typ string
		msg string
		res interface{}
	}{
		{
			"block",
			`["earliest"]`,
			EarliestBlockNumber,
		},
		{
			"block",
			`["latest"]`,
			LatestBlockNumber,
		},
		{
			"block",
			`["0x1"]`,
			BlockNumber(1),
		},
		{
			"type",
			`["` + addr1.String() + `"]`,
			addr1,
		},
		{
			"blockPtr",
			`["a"]`,
			nil,
		},
		{
			"blockPtr",
			`["a", "latest"]`,
			LatestBlockNumber,
		},
		{
			"filter",
			`[{"fromBlock": "pending", "toBlock": "earliest"}]`,
			LogQuery{fromBlock: LatestBlockNumber, toBlock: EarliestBlockNumber}, // pending == latest
		},
	}

	for _, c := range cases {
		res := handleReq(c.typ, c.msg)
		if !reflect.DeepEqual(res, c.res) {
			t.Fatal("no tx pool events received in the predefined time slot")
		}
	}
}

func TestDispatcherBatchRequest(t *testing.T) {
	t.Parallel()

	type caseData struct {
		name          string
		desc          string
		dispatcher    *Dispatcher
		reqBody       []byte
		err           *ObjectError
		batchResponse []*SuccessResponse
	}

	mock := &mockWsConn{
		WriteMessageFn: func(i int, b []byte) error {
			return nil
		},
	}

	cases := []caseData{
		{
			"leading-whitespace",
			"test with leading whitespace (\"  \\t\\n\\n\\r\\)",
			newTestDispatcher(t,
				hclog.NewNullLogger(),
				newMockStore(),
				&dispatcherParams{
					chainID:                 0,
					priceLimit:              0,
					jsonRPCBatchLengthLimit: 20,
					blockRangeLimit:         1000,
				},
			),
			append([]byte{0x20, 0x20, 0x09, 0x0A, 0x0A, 0x0D}, []byte(`[
				{"id":1,"jsonrpc":"2.0","method":"eth_getBalance","params":["0x1", true]},
				{"id":2,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x2", true]},
				{"id":3,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x3", true]},
				{"id":4,"jsonrpc":"2.0","method": "web3_sha3","params": ["0x68656c6c6f20776f726c64"]}]`)...),
			nil,
			[]*SuccessResponse{
				{Error: &ObjectError{Code: -32602, Message: "Invalid Params"}},
				{Error: nil},
				{Error: nil},
				{Error: nil}},
		},
		{
			"valid-batch-req",
			"test with batch req length within batchRequestLengthLimit",
			newTestDispatcher(t,
				hclog.NewNullLogger(),
				newMockStore(),
				&dispatcherParams{
					chainID:                 0,
					priceLimit:              0,
					jsonRPCBatchLengthLimit: 10,
					blockRangeLimit:         1000,
				},
			),
			[]byte(`[
				{"id":1,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":2,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":3,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":4,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":5,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":6,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]}]`),
			nil,
			[]*SuccessResponse{
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil}},
		},
		{
			"invalid-batch-req",
			"test with batch req length exceeding batchRequestLengthLimit",
			newTestDispatcher(t,
				hclog.NewNullLogger(),
				newMockStore(),
				&dispatcherParams{
					chainID:                 0,
					priceLimit:              0,
					jsonRPCBatchLengthLimit: 3,
					blockRangeLimit:         1000,
				},
			),
			[]byte(`[
				{"id":1,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":2,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":3,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":4,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":5,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":6,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]}]`),
			&ObjectError{Code: -32600, Message: "Batch request length too long"},
			nil,
		},
		{
			"no-limits",
			"test when limits are not set",
			newTestDispatcher(t,
				hclog.NewNullLogger(),
				newMockStore(),
				&dispatcherParams{
					chainID:                 0,
					priceLimit:              0,
					jsonRPCBatchLengthLimit: 0,
					blockRangeLimit:         0,
				}),
			[]byte(`[
				{"id":1,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":2,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":3,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":4,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":5,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":6,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":7,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":8,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":9,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":10,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":11,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
				{"id":12,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]}]`,
			),
			nil,
			[]*SuccessResponse{
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
				{Error: nil},
			},
		},
	}

	check := func(c caseData, res []byte) {
		if c.err != nil {
			var resp ErrorResponse

			assert.NoError(t, expectBatchJSONResult(res, &resp))
			assert.Equal(t, c.err, resp.Error)
		} else {
			var batchResp []SuccessResponse

			assert.NoError(t, expectBatchJSONResult(res, &batchResp))

			switch c.name {
			case "leading-whitespace":
				assert.Len(t, batchResp, 4)

				for index, resp := range batchResp {
					assert.Equal(t, c.batchResponse[index].Error, resp.Error)
				}
			case "valid-batch-req":
				assert.Len(t, batchResp, 6)

				for index, resp := range batchResp {
					assert.Equal(t, c.batchResponse[index].Error, resp.Error)
				}
			case "no-limits":
				assert.Len(t, batchResp, 12)

				for index, resp := range batchResp {
					assert.Equal(t, c.batchResponse[index].Error, resp.Error)
				}
			}
		}
	}

	for _, c := range cases {
		c := c

		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			res, _ := c.dispatcher.HandleWs(c.reqBody, mock)

			check(c, res)

			res, _ = c.dispatcher.Handle(context.Background(), c.reqBody)

			check(c, res)
		})
	}
}

func TestDispatcher_WebsocketConnection_Unsubscribe(t *testing.T) {
	t.Parallel()

	store := newMockStore()
	dispatcher := newTestDispatcher(t,
		hclog.NewNullLogger(),
		store,
		&dispatcherParams{
			chainID:                 0,
			priceLimit:              0,
			jsonRPCBatchLengthLimit: 20,
			blockRangeLimit:         1000,
		},
	)
	mockConn := &mockWsConn{
		WriteMessageFn: func(i int, b []byte) error {
			return nil
		},
	}

	resp := SuccessResponse{}
	reqUnsub := func(n string) []byte {
		return []byte(fmt.Sprintf(`{"method": "eth_unsubscribe", "params": [%s]}`, n))
	}

	// non existing subscription
	r, err := dispatcher.HandleWs(reqUnsub("\"787832\""), mockConn)
	require.NoError(t, err)

	require.NoError(t, json.Unmarshal(r, &resp))
	assert.Equal(t, "false", string(resp.Result))

	r, err = dispatcher.HandleWs([]byte(`{"method": "eth_subscribe", "params": ["newHeads"]}`), mockConn)
	require.NoError(t, err)

	require.NoError(t, json.Unmarshal(r, &resp))

	// existing subscription
	r, err = dispatcher.HandleWs(reqUnsub(string(resp.Result)), mockConn)
	require.NoError(t, err)

	require.NoError(t, json.Unmarshal(r, &resp))
	assert.Equal(t, "true", string(resp.Result))
}

func TestLowerCaseFirstRune(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Debug_CPUProfile", "debug_CPUProfile"},
		{"BlockNumber", "blockNumber"},
		{"CPUProfileNew", "cPUProfileNew"},
		{"", ""},
		{"A", "a"},
		{"a", "a"},
		{"AB", "aB"},
		{"aB", "aB"},
		{"1234", "1234"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := lowerCaseFirstRune(test.input)
			if result != test.expected {
				t.Errorf("lowerCaseFirst(%q) = %q; want %q", test.input, result, test.expected)
			}
		})
	}
}

func newTestDispatcher(tb testing.TB, logger hclog.Logger, store JSONRPCStore, params *dispatcherParams) *Dispatcher {
	tb.Helper()

	d, err := newDispatcher(logger, store, params)
	require.NoError(tb, err)

	return d
}

type testRPCService struct {
	lastCtx context.Context
}

func (s *testRPCService) Echo(ctx context.Context, v argUint64) (interface{}, error) {
	s.lastCtx = ctx

	return v, nil
}

func (s *testRPCService) Bad(_ argUint64) (interface{}, error) {
	return nil, NewInvalidParamsError("bad")
}

func (s *testRPCService) Sleep(_ context.Context, ms argUint64) (interface{}, error) {
	time.Sleep(time.Duration(ms) * time.Millisecond)

	return ms, nil
}

func TestDispatcher_ContextInjectionAndErrorPassthrough(t *testing.T) {
	t.Parallel()

	svc := &testRPCService{}
	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
		requestTimeout: 2 * time.Second,
	})
	require.NoError(t, d.registerService("test", svc))

	fd := d.serviceMap["test"].funcMap["echo"]
	require.True(t, fd.hasCtx)
	require.Equal(t, 1, fd.numParams())

	res, err := d.Handle(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":1,"method":"test_echo","params":["0x5"]}`,
	))
	require.NoError(t, err)

	var resp SuccessResponse
	require.NoError(t, json.Unmarshal(res, &resp))
	require.Nil(t, resp.Error)
	require.NotNil(t, svc.lastCtx)
	_, ok := svc.lastCtx.Deadline()
	require.True(t, ok)

	res, err = d.Handle(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":2,"method":"test_bad","params":["0x1"]}`,
	))
	require.NoError(t, err)

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal(res, &errResp))
	require.Equal(t, -32602, errResp.Error.Code)
}

func TestMethodCost(t *testing.T) {
	t.Parallel()

	require.Equal(t, uint64(20), MethodCost("eth_estimateGas"))
	require.Equal(t, uint64(5), MethodCost("eth_call"))
	require.Equal(t, uint64(50), MethodCost("debug_unknown"))
	require.Equal(t, uint64(1), MethodCost("net_version"))

	for method, cost := range methodCosts {
		require.LessOrEqual(t, cost, uint64(200), method)
	}
}

func TestDispatcherBatchCostLimit(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
		batchCostLimit: 10,
	})
	mock := &mockWsConn{
		WriteMessageFn: func(int, []byte) error { return nil },
	}

	tooExpensive := []byte(`[
		{"id":1,"jsonrpc":"2.0","method":"eth_estimateGas","params":[{}]},
		{"id":2,"jsonrpc":"2.0","method":"eth_estimateGas","params":[{}]}
	]`)

	checkCostRejected := func(t *testing.T, res []byte) {
		t.Helper()

		var resp ErrorResponse
		require.NoError(t, json.Unmarshal(res, &resp))
		require.Equal(t, -32600, resp.Error.Code)
		require.Equal(t, "Batch request cost too high", resp.Error.Message)
	}

	res, err := d.Handle(context.Background(), tooExpensive)
	require.NoError(t, err)
	checkCostRejected(t, res)

	res, err = d.HandleWs(tooExpensive, mock)
	require.NoError(t, err)
	checkCostRejected(t, res)

	cheap := []byte(`[
		{"id":1,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":2,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":3,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":4,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":5,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":6,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":7,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":8,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":9,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]},
		{"id":10,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]}
	]`)

	res, err = d.Handle(context.Background(), cheap)
	require.NoError(t, err)

	var batch []SuccessResponse
	require.NoError(t, json.Unmarshal(res, &batch))
	require.Len(t, batch, 10)
}

func TestDispatcherBatchDeadline(t *testing.T) {
	t.Parallel()

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

	res, err := d.Handle(context.Background(), body)
	require.NoError(t, err)

	var batch []SuccessResponse
	require.NoError(t, json.Unmarshal(res, &batch))
	require.Len(t, batch, 3)
	require.Nil(t, batch[0].Error)
	require.Equal(t, float64(1), batch[0].ID)
	require.Equal(t, -32000, batch[len(batch)-1].Error.Code)
	require.Equal(t, "batch deadline exceeded", batch[len(batch)-1].Error.Message)
	require.Equal(t, float64(3), batch[len(batch)-1].ID)
}

func TestDispatcherResponseSizeCap(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
		maxResponseSize: 1,
	})

	single := []byte(`{"id":1,"jsonrpc":"2.0","method":"eth_blockNumber","params":[]}`)
	res, err := d.Handle(context.Background(), single)
	require.NoError(t, err)

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal(res, &errResp))
	require.Equal(t, -32005, errResp.Error.Code)
	require.Equal(t, "response too large", errResp.Error.Message)

	d = newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
		maxResponseSize: 180,
	})

	body := []byte(`[
		{"id":1,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
		{"id":2,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]},
		{"id":3,"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["latest", true]}
	]`)

	res, err = d.Handle(context.Background(), body)
	require.NoError(t, err)

	var batch []SuccessResponse
	require.NoError(t, json.Unmarshal(res, &batch))
	require.Len(t, batch, 3)
	require.Equal(t, -32005, batch[1].Error.Code)
	require.Equal(t, "batch response too large", batch[1].Error.Message)
	require.Equal(t, -32005, batch[2].Error.Code)
}
