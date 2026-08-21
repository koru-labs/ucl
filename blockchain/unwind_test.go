package blockchain

import (
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/memory"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/require"
)

func seedChain(t *testing.T, n uint64) *storage.Storage {
	t.Helper()

	db, err := memory.NewMemoryStorage()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	parent := types.ZeroHash

	for i := uint64(0); i <= n; i++ {
		to := types.StringToAddress("11")
		tx := &types.Transaction{
			Nonce:    i,
			GasPrice: big.NewInt(1),
			Gas:      21000,
			To:       &to,
			Value:    big.NewInt(1),
			V:        big.NewInt(27),
			R:        big.NewInt(1),
			S:        big.NewInt(2),
		}
		tx.ComputeHash(i)

		header := &types.Header{
			Number:     i,
			ParentHash: parent,
			Difficulty: 1,
		}
		header.ComputeHash()

		batch := db.NewWriter()
		batch.PutCanonicalHeader(header, big.NewInt(int64(i+1)))
		batch.PutBody(i, header.Hash, &types.Body{Transactions: []*types.Transaction{tx}})
		batch.PutTxLookup(tx.Hash, i)
		require.NoError(t, batch.WriteBatch())

		parent = header.Hash
	}

	return db
}

func TestUnwindLastBlock(t *testing.T) {
	t.Parallel()

	db := seedChain(t, 3)

	res, err := Unwind(db, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(3), res.FromNumber)
	require.Equal(t, uint64(2), res.ToNumber)
	require.Len(t, res.Removed, 1)
	require.Equal(t, uint64(3), res.Removed[0].Number)
	require.Len(t, res.Removed[0].TxHashes, 1)

	headNum, ok := db.ReadHeadNumber()
	require.True(t, ok)
	require.Equal(t, uint64(2), headNum)

	headHash, ok := db.ReadHeadHash()
	require.True(t, ok)
	require.Equal(t, res.ToHash, headHash)

	_, ok = db.ReadCanonicalHash(3)
	require.False(t, ok)

	_, lookupErr := db.ReadTxLookup(res.Removed[0].TxHashes[0])
	require.ErrorIs(t, lookupErr, storage.ErrNotFound)

	_, blockLookupErr := db.ReadBlockLookup(res.Removed[0].Hash)
	require.ErrorIs(t, blockLookupErr, storage.ErrNotFound)

	// orphan header/body stay on disk
	_, err = db.ReadHeader(res.Removed[0].Number, res.Removed[0].Hash)
	require.NoError(t, err)
}

func TestUnwindMultipleBlocks(t *testing.T) {
	t.Parallel()

	db := seedChain(t, 5)

	res, err := UnwindTo(db, 2)
	require.NoError(t, err)
	require.Equal(t, uint64(5), res.FromNumber)
	require.Equal(t, uint64(2), res.ToNumber)
	require.Len(t, res.Removed, 3)

	headNum, ok := db.ReadHeadNumber()
	require.True(t, ok)
	require.Equal(t, uint64(2), headNum)

	_, ok = db.ReadCanonicalHash(3)
	require.False(t, ok)
	_, ok = db.ReadCanonicalHash(4)
	require.False(t, ok)
	_, ok = db.ReadCanonicalHash(5)
	require.False(t, ok)

	canon, ok := db.ReadCanonicalHash(2)
	require.True(t, ok)
	require.Equal(t, res.ToHash, canon)
}

func TestUnwindPastGenesisFails(t *testing.T) {
	t.Parallel()

	db := seedChain(t, 0)

	_, err := Unwind(db, 1)
	require.Error(t, err)
}

func TestUnwindZeroBlocksFails(t *testing.T) {
	t.Parallel()

	db := seedChain(t, 1)

	_, err := Unwind(db, 0)
	require.Error(t, err)
}

func TestPlanUnwindDoesNotWrite(t *testing.T) {
	t.Parallel()

	db := seedChain(t, 2)

	plan, err := PlanUnwind(db, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(2), plan.FromNumber)
	require.Len(t, plan.Removed, 2)

	headNum, ok := db.ReadHeadNumber()
	require.True(t, ok)
	require.Equal(t, uint64(2), headNum)

	_, ok = db.ReadCanonicalHash(2)
	require.True(t, ok)
}
