package txpool

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

const journalRotate = 1000

func TestJournalLoad(t *testing.T) {
	t.Parallel()

	path, err := os.MkdirTemp("", "txpool")
	require.NoError(t, err)

	defer os.RemoveAll(path)

	addrTo := types.StringToAddress("11")

	originalTxs := []*types.Transaction{
		{
			Type:     types.StateTx,
			GasPrice: big.NewInt(11),
			Nonce:    1,
			Gas:      11,
			To:       &addrTo,
			From:     types.StringToAddress("1"),
			Value:    big.NewInt(1),
			Input:    []byte{1, 2},
			V:        big.NewInt(25),
			S:        big.NewInt(26),
			R:        big.NewInt(27),
		},
		{
			Type:     types.LegacyTx,
			GasPrice: big.NewInt(11),
			Nonce:    2,
			Gas:      11,
			To:       &addrTo,
			From:     types.StringToAddress("2"),
			Value:    big.NewInt(2),
			Input:    []byte{1, 2},
			V:        big.NewInt(25),
			S:        big.NewInt(26),
			R:        big.NewInt(27),
		},
		{
			Type:      types.DynamicFeeTx,
			GasFeeCap: big.NewInt(12),
			GasTipCap: big.NewInt(13),
			Nonce:     3,
			Gas:       11,
			To:        &addrTo,
			From:      types.StringToAddress("3"),
			Value:     big.NewInt(3),
			Input:     []byte{1, 2},
			V:         big.NewInt(25),
			S:         big.NewInt(26),
			R:         big.NewInt(27),
		},
	}

	// init journal
	journal := newTxJournal(filepath.Join(path, "test.rlp"), hclog.NewNullLogger(), make(chan struct{}), journalRotate)

	// add txs into journal with rotate
	require.NoError(t, journal.rotate(originalTxs))

	// insert into journal
	for _, tx := range originalTxs {
		require.NoError(t, journal.insert(tx))
	}

	resultTxs := make([]*types.Transaction, 0)
	// load journal
	require.NoError(t, journal.load(func(tx *types.Transaction) error {
		resultTxs = append(resultTxs, tx)

		return nil
	}))

	require.Len(t, resultTxs, len(originalTxs)*2)

	for i, tx := range originalTxs {
		require.Equal(t, tx.Hash, resultTxs[i].Hash)
		require.Equal(t, tx.Hash, resultTxs[len(originalTxs)+i].Hash)
	}

	require.NoError(t, journal.close())
}

func TestJournalRotate(t *testing.T) {
	t.Parallel()

	path, err := os.MkdirTemp("", "txpool")
	require.NoError(t, err)

	defer os.RemoveAll(path)

	// create journal
	rotateCh := make(chan struct{})
	journal := newTxJournal(filepath.Join(path, "test.rlp"), hclog.NewNullLogger(), rotateCh, journalRotate)

	// init journal
	require.NoError(t, journal.rotate([]*types.Transaction{}))

	// start routine
	go func() {
		<-rotateCh

		return
	}()

	// add txs into journal until rotate event is fired
	for range journalRotate {
		require.NoError(t, journal.insert(newTx(addr1, 1, 1)))
	}

	require.NoError(t, journal.close())
}
