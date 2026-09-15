package jsonrpc

import (
	"testing"

	"github.com/0xPolygon/polygon-edge/gasprice"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// recordingFeeHistoryStore captures the block number eth_feeHistory forwards to the gas helper,
// which clamps it to the chain head itself.
type recordingFeeHistoryStore struct {
	*mockStore

	gotNewestBlock uint64
}

func (s *recordingFeeHistoryStore) FeeHistory(_ uint64, newestBlock uint64, _ []float64) (
	*gasprice.FeeHistoryReturn, error,
) {
	s.gotNewestBlock = newestBlock

	return &gasprice.FeeHistoryReturn{
		OldestBlock:   1,
		BaseFeePerGas: []uint64{1},
		GasUsedRatio:  []float64{0.5},
		Reward:        [][]uint64{{1}},
	}, nil
}

// TestEth_AboveHeadBlockNumber pins the response of every eth_* endpoint that resolves a block
// number, for a number past the chain head. These are the endpoints whose resolver gained a
// chain-height check, so this is the contract that must not drift.
func TestEth_AboveHeadBlockNumber(t *testing.T) {
	const aboveHead = BlockNumber(50)

	store := newMockBlockStore()
	for i := 0; i < 10; i++ {
		store.add(newTestBlock(uint64(i), hash1))
	}

	eth := newTestEthEndpoint(store)

	t.Run("eth_getBlockByNumber answers null", func(t *testing.T) {
		res, err := eth.GetBlockByNumber(aboveHead, false)
		require.NoError(t, err)
		require.Nil(t, res)
	})

	t.Run("eth_getHeaderByNumber answers null", func(t *testing.T) {
		res, err := eth.GetHeaderByNumber(aboveHead)
		require.NoError(t, err)
		require.Nil(t, res)
	})

	t.Run("eth_getBlockTransactionCountByNumber answers null", func(t *testing.T) {
		res, err := eth.GetBlockTransactionCountByNumber(aboveHead)
		require.NoError(t, err)
		require.Nil(t, res)
	})

	t.Run("eth_getTransactionByBlockNumberAndIndex answers null", func(t *testing.T) {
		res, err := eth.GetTransactionByBlockNumberAndIndex(aboveHead, 0)
		require.NoError(t, err)
		require.Nil(t, res)
	})

	t.Run("eth_getBlockReceipts reports the block as not found", func(t *testing.T) {
		res, err := eth.GetBlockReceipts(aboveHead)
		require.ErrorIs(t, err, ErrBlockNotFound)
		require.Nil(t, res)
	})

	t.Run("eth_feeHistory forwards the number and lets the gas helper clamp it", func(t *testing.T) {
		feeStore := &recordingFeeHistoryStore{mockStore: newMockStore()}
		feeStore.header.Number = 10

		res, err := newTestEthEndpoint(feeStore).FeeHistory(10, aboveHead, []float64{10, 20})
		require.NoError(t, err)
		require.NotNil(t, res)
		require.Equal(t, uint64(aboveHead), feeStore.gotNewestBlock)
	})
}

// TestDebug_AboveHeadBlockNumber pins that the debug_* endpoints resolving a block number reject
// a number past the chain head. They reported it as a missing block before the height check and
// report it as an out-of-range number now; either way the request fails and nothing is traced.
func TestDebug_AboveHeadBlockNumber(t *testing.T) {
	const aboveHead = BlockNumber(50)

	store := &debugEndpointMockStore{
		headerFn: func() *types.Header {
			return createTestHeader(10, nil)
		},
		getBlockByNumberFn: func(uint64, bool) (*types.Block, bool) {
			return nil, false
		},
		getHeaderByNumberFn: func(uint64) (*types.Header, bool) {
			return nil, false
		},
	}

	endpoint := NewDebug(store, 100000, 0, 0)

	t.Run("debug_traceBlockByNumber fails", func(t *testing.T) {
		res, err := endpoint.TraceBlockByNumber(aboveHead, &TraceConfig{})
		require.Error(t, err)
		require.Nil(t, res)
		t.Logf("error: %v", err)
	})

	t.Run("debug_dumpBlock fails", func(t *testing.T) {
		res, err := endpoint.DumpBlock(aboveHead)
		require.Error(t, err)
		require.Nil(t, res)
		t.Logf("error: %v", err)
	})

	t.Run("debug_getAccessibleState fails", func(t *testing.T) {
		res, err := endpoint.GetAccessibleState(aboveHead, 0)
		require.Error(t, err)
		require.Equal(t, 0, res)
		t.Logf("error: %v", err)
	})
}

// TestEth_GetLogs_AboveHeadToBlock pins eth_getLogs truncating a range that runs past the head
// rather than failing, which is why the log filter keeps the unvalidated resolver.
func TestEth_GetLogs_AboveHeadToBlock(t *testing.T) {
	store := newMockBlockStore()
	store.setupLogs()

	for i := 1; i <= 3; i++ {
		block := newTestBlock(uint64(i), hash1)
		block.Transactions = []*types.Transaction{newTestTransaction(uint64(i), addr0)}
		store.add(block)
	}

	f := NewFilterManager(hclog.NewNullLogger(), store, 1000, FilterLimits{})
	t.Cleanup(f.Close)

	// the head is #3, so the scan has to stop there instead of rejecting the query
	logs, err := f.GetLogsForQuery(&LogQuery{
		fromBlock: 1,
		toBlock:   500,
		Addresses: []types.Address{addr0},
	})
	require.NoError(t, err)
	require.NotNil(t, logs)
}
