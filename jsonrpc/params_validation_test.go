package jsonrpc

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// TestPoC_HandleSubscribe_MissingLogParamPanic is the inverted proof of concept.
// eth_subscribe("logs") with no filter object must return Invalid params, not panic.
func TestPoC_HandleSubscribe_MissingLogParamPanic(t *testing.T) {
	t.Parallel()

	d := newTestDispatcher(t, hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
		jsonRPCBatchLengthLimit: 20,
		blockRangeLimit:         1000,
	})
	conn, _ := newMockWsConnWithMsgCh()

	req := Request{
		Method: "eth_subscribe",
		Params: json.RawMessage(`["logs"]`),
	}

	id, err := d.handleSubscribe(req, conn)
	require.Empty(t, id)
	require.NotNil(t, err)
	require.Equal(t, -32602, err.ErrorCode())
	require.Equal(t, "Invalid params", err.Error())
}

func testBlockWithTxs(n int) *types.Block {
	txs := make([]*types.Transaction, n)
	for i := 0; i < n; i++ {
		txs[i] = createTestTransaction(types.BytesToHash([]byte{byte(i + 1)}))
	}

	return &types.Block{
		Header:       createTestHeader(1, nil),
		Transactions: txs,
	}
}

// TestPoC_GetTransactionByBlockAndIndex_OffByOnePanic is the inverted proof of concept.
// index == len(transactions) must return ErrIndexOutOfRange, not read past the slice.
func TestPoC_GetTransactionByBlockAndIndex_OffByOnePanic(t *testing.T) {
	t.Parallel()

	block := testBlockWithTxs(1)

	res, err := GetTransactionByBlockAndIndex(block, argUint64(len(block.Transactions)))
	require.Nil(t, res)
	require.ErrorIs(t, err, ErrIndexOutOfRange)
}

// TestPoC_GetTransactionByBlockAndIndex_OverflowPanic is the inverted proof of concept.
// A uint64 index that truncates to a negative int must be rejected, not used as a slice index.
func TestPoC_GetTransactionByBlockAndIndex_OverflowPanic(t *testing.T) {
	t.Parallel()

	block := testBlockWithTxs(1)

	res, err := GetTransactionByBlockAndIndex(block, argUint64(math.MaxUint64))
	require.Nil(t, res)
	require.ErrorIs(t, err, ErrIndexOutOfRange)
}
