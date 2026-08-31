package txpool

import (
	"math/big"
	"path/filepath"
	"testing"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/require"
)

func testJournalTx(nonce uint64, from types.Address) *types.Transaction {
	to := types.StringToAddress("11")

	tx := &types.Transaction{
		Type:       types.LegacyTx,
		GasPrice:   big.NewInt(1),
		Nonce:      nonce,
		Gas:        21000,
		To:         &to,
		From:       from,
		Value:      big.NewInt(1),
		V:          big.NewInt(27),
		R:          big.NewInt(1),
		S:          big.NewInt(2),
		TxPoolTime: 10,
	}
	tx.ComputeHash(0)

	return tx
}

func TestJournalRemoveAndRestore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	journalPath := filepath.Join(dir, "transactions.rlp")
	removedPath := filepath.Join(dir, "removed.rlp")

	tx1 := testJournalTx(1, types.StringToAddress("1"))
	tx2 := testJournalTx(2, types.StringToAddress("2"))

	require.NoError(t, WriteJournalFile(journalPath, []*types.Transaction{tx1, tx2}))

	moved, err := RemoveFromJournal(journalPath, removedPath, []types.Hash{tx1.Hash})
	require.NoError(t, err)
	require.Len(t, moved, 1)
	require.Equal(t, tx1.Hash, moved[0].Hash)

	live, err := ReadJournalFile(journalPath)
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.Equal(t, tx2.Hash, txHash(live[0]))

	quarantine, err := ReadJournalFile(removedPath)
	require.NoError(t, err)
	require.Len(t, quarantine, 1)
	require.Equal(t, tx1.Hash, txHash(quarantine[0]))

	restored, err := RestoreToJournal(journalPath, removedPath, []types.Hash{tx1.Hash})
	require.NoError(t, err)
	require.Len(t, restored, 1)

	live, err = ReadJournalFile(journalPath)
	require.NoError(t, err)
	require.Len(t, live, 2)

	quarantine, err = ReadJournalFile(removedPath)
	require.NoError(t, err)
	require.Empty(t, quarantine)
}

func TestJournalRemoveMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := RemoveFromJournal(
		filepath.Join(dir, "transactions.rlp"),
		filepath.Join(dir, "removed.rlp"),
		[]types.Hash{types.StringToHash("0x01")},
	)
	require.Error(t, err)
}
