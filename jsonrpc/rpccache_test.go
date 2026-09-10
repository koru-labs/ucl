package jsonrpc

import (
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func testCachedBlock(nTxs, nReceipts int) (*types.Block, []*types.Receipt) {
	txs := make([]*types.Transaction, nTxs)
	for i := 0; i < nTxs; i++ {
		txs[i] = createTestTransaction(types.BytesToHash([]byte{byte(i + 1)}))
	}

	receipts := make([]*types.Receipt, nReceipts)
	for i := 0; i < nReceipts; i++ {
		receipts[i] = &types.Receipt{Logs: []*types.Log{{}}}
	}

	return &types.Block{
		Header:       createTestHeader(1, nil),
		Transactions: txs,
	}, receipts
}

func TestRPCCache_GetBlockCache_ReceiptsTxsMismatch(t *testing.T) {
	t.Parallel()

	block, receipts := testCachedBlock(12, 1)
	store := newMockBlockStore()
	store.add(block)
	store.receipts[block.Hash()] = receipts

	cache := initRPCCache(store, hclog.NewNullLogger(), time.Minute, 10)

	got, err := cache.getBlockCache(block.Number())
	require.ErrorIs(t, err, ErrReceiptsTxsMismatch)
	require.Nil(t, got)
}

func TestRPCCache_GetBlockCache_MissDoesNotPanic(t *testing.T) {
	t.Parallel()

	block, receipts := testCachedBlock(12, 12)
	store := newMockBlockStore()
	store.add(block)
	store.receipts[block.Hash()] = receipts

	cache := initRPCCache(store, hclog.NewNullLogger(), time.Millisecond, 10)

	got, err := cache.getBlockCache(block.Number())
	require.NoError(t, err)
	require.NotNil(t, got)

	time.Sleep(10 * time.Millisecond)

	got, err = cache.getBlockCache(block.Number())
	require.NoError(t, err)
	require.NotNil(t, got)
}
