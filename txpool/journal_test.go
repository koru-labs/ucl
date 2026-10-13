package txpool

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

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
			Type:       types.StateTx,
			GasPrice:   big.NewInt(11),
			Nonce:      1,
			Gas:        11,
			To:         &addrTo,
			From:       types.StringToAddress("1"),
			Value:      big.NewInt(1),
			Input:      []byte{1, 2},
			V:          big.NewInt(25),
			S:          big.NewInt(26),
			R:          big.NewInt(27),
			TxPoolTime: 10,
		},
		{
			Type:       types.LegacyTx,
			GasPrice:   big.NewInt(11),
			Nonce:      2,
			Gas:        11,
			To:         &addrTo,
			From:       types.StringToAddress("2"),
			Value:      big.NewInt(2),
			Input:      []byte{1, 2},
			V:          big.NewInt(25),
			S:          big.NewInt(26),
			R:          big.NewInt(27),
			TxPoolTime: 100,
		},
		{
			Type:       types.DynamicFeeTx,
			GasFeeCap:  big.NewInt(12),
			GasTipCap:  big.NewInt(13),
			Nonce:      3,
			Gas:        11,
			To:         &addrTo,
			From:       types.StringToAddress("3"),
			Value:      big.NewInt(3),
			Input:      []byte{1, 2},
			V:          big.NewInt(25),
			S:          big.NewInt(26),
			R:          big.NewInt(27),
			TxPoolTime: 1000,
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
		require.Equal(t, tx.TxPoolTime, resultTxs[i].TxPoolTime)
	}

	require.NoError(t, journal.close())
}

func TestJournalRotate(t *testing.T) {
	t.Parallel()

	const rotateSize = 5

	path := t.TempDir()

	// buffered like the pool's channel; nobody reads it, so the test also
	// proves insert never waits on the consumer
	rotateCh := make(chan struct{}, 1)
	journal := newTxJournal(filepath.Join(path, "test.rlp"), hclog.NewNullLogger(), rotateCh, rotateSize)

	require.NoError(t, journal.rotate([]*types.Transaction{}))

	for i := range rotateSize - 1 {
		require.NoError(t, journal.insert(testJournalTx(uint64(i), addr1)))
	}

	require.Empty(t, rotateCh, "no rotation signal expected below threshold")

	// reaching the threshold signals rotation
	require.NoError(t, journal.insert(testJournalTx(rotateSize, addr1)))
	require.Len(t, rotateCh, 1)

	// going past the threshold keeps signalling (>=) without blocking on the
	// already-full buffer
	require.NoError(t, journal.insert(testJournalTx(rotateSize+1, addr1)))
	require.Len(t, rotateCh, 1)

	// a successful rotation resets the counter and drops the stale signal
	require.NoError(t, journal.rotate([]*types.Transaction{}))
	require.Empty(t, rotateCh)

	require.NoError(t, journal.close())
}

// Loading happens before the rotation consumer starts. A journal holding at
// least rotateSize transactions used to block forever on the signal send while
// holding the journal lock, hanging node startup.
func TestJournalLoadPastRotateSizeDoesNotBlock(t *testing.T) {
	t.Parallel()

	const rotateSize = 2

	path := filepath.Join(t.TempDir(), "test.rlp")

	require.NoError(t, WriteJournalFile(path, []*types.Transaction{
		testJournalTx(1, addr1),
		testJournalTx(2, addr1),
		testJournalTx(3, addr1),
	}))

	// unbuffered with no reader: the worst case for a blocking send
	journal := newTxJournal(path, hclog.NewNullLogger(), make(chan struct{}), rotateSize)

	done := make(chan error, 1)

	go func() {
		done <- journal.load(journal.insert)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("journal load blocked on the rotation signal")
	}
}

// A failed rotation leaves no open writer. Inserts must report it and keep
// requesting rotation so the journal recovers once the cause is gone.
func TestJournalRecoversAfterFailedRotation(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "missing")
	rotateCh := make(chan struct{}, 1)
	journal := newTxJournal(filepath.Join(dir, "test.rlp"), hclog.NewNullLogger(), rotateCh, journalRotate)

	// the directory does not exist, so the replacement file cannot be created
	require.Error(t, journal.rotate(nil))

	require.ErrorIs(t, journal.insert(testJournalTx(1, addr1)), errNoActiveJournal)
	require.Len(t, rotateCh, 1, "insert without a writer must request a rotation retry")
	<-rotateCh

	// the cause goes away and the retried rotation succeeds
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, journal.rotate(nil))
	require.NoError(t, journal.insert(testJournalTx(2, addr1)))

	require.NoError(t, journal.close())

	txs, err := ReadJournalFile(filepath.Join(dir, "test.rlp"))
	require.NoError(t, err)
	require.Len(t, txs, 1)
}

// After close, a late rotation must not reopen the file and inserts must not
// request rotations nobody will serve.
func TestJournalClosedStaysClosed(t *testing.T) {
	t.Parallel()

	rotateCh := make(chan struct{}, 1)
	journal := newTxJournal(filepath.Join(t.TempDir(), "test.rlp"), hclog.NewNullLogger(), rotateCh, 1)

	require.NoError(t, journal.rotate(nil))
	require.NoError(t, journal.close())

	require.NoError(t, journal.rotate(nil))
	require.Nil(t, journal.writer)

	require.ErrorIs(t, journal.insert(testJournalTx(1, addr1)), errJournalClosed)
	require.Empty(t, rotateCh)
}

// A broken journal must not turn an accepted transaction into a failed
// submission: the tx is already in the pool and must still be gossiped.
func TestAddTxSucceedsWhenJournalFails(t *testing.T) {
	t.Parallel()

	pool, err := newTestPool()
	require.NoError(t, err)
	pool.SetSigner(&mockSigner{})

	// a rotation into a missing directory fails and leaves no writer
	pool.journal = newTxJournal(
		filepath.Join(t.TempDir(), "missing", "test.rlp"), hclog.NewNullLogger(), pool.journalCh, journalRotate)
	require.Error(t, pool.journal.rotate(nil))

	go func() { <-pool.promoteReqCh }()

	tx := newTx(addr1, 0, 1)
	require.NoError(t, pool.AddTx(tx))

	_, exists := pool.index.get(tx.Hash)
	require.True(t, exists)
	require.Len(t, pool.journalCh, 1, "failed journal write must request a rotation retry")
}
