package state

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/types"
)

// stubMV is a hand-driven MVMemoryAccess: tests declare exactly what each key resolves to, so
// the estimate (blocked-dependency) path can be exercised deterministically instead of waiting
// for the narrow scheduler race that produces one in a real batch.
type stubMV struct {
	versions map[Key]stubVersion
	reads    int
}

type stubVersion struct {
	val         any
	txIndex     int
	incarnation int
	isEstimate  bool
}

func (s *stubMV) Read(key Key, _ int) (any, int, int, bool, bool) {
	s.reads++

	v, ok := s.versions[key]
	if !ok {
		return nil, 0, 0, false, false
	}

	return v.val, v.txIndex, v.incarnation, v.isEstimate, true
}

// stubSnapshot is a minimal base state: one account holding one storage slot.
type stubSnapshot struct {
	account *Account
	storage map[types.Hash]types.Hash
}

func (s stubSnapshot) GetAccount(types.Address) (*Account, error) { return s.account, nil }
func (s stubSnapshot) GetStorage(_ types.Address, _ types.Hash, key types.Hash) types.Hash {
	return s.storage[key]
}
func (s stubSnapshot) GetCode(types.Hash) ([]byte, bool) { return nil, false }
func (s stubSnapshot) GetRootHash() types.Hash           { return types.Hash{} }

func newStubbedTxn(t *testing.T, mv *stubMV) (*TxnMVCC, stubSnapshot) {
	t.Helper()

	snap := stubSnapshot{
		account: &Account{Balance: big.NewInt(1000), Nonce: 7, CodeHash: types.EmptyCodeHash.Bytes()},
		storage: map[types.Hash]types.Hash{{0x01}: {0xAA}},
	}

	return newTxnMVCC(snap, snap, mv, 5 /*txIndex*/, 0 /*incarnation*/), snap
}

// TestTxnMVCC_EstimateReadLatchesInsteadOfPanicking covers the core of the sticky-error design:
// a read that lands on an unresolved placeholder must not unwind the EVM stack, must hand back a
// well-formed value so execution can run itself out, and must leave behind an unmistakable
// "throw this away" marker.
func TestTxnMVCC_EstimateReadLatchesInsteadOfPanicking(t *testing.T) {
	addr := types.StringToAddress("0xabc")

	t.Run("storage read", func(t *testing.T) {
		mv := &stubMV{versions: map[Key]stubVersion{
			NewStateKey(addr, types.Hash{0x01}): {txIndex: 3, isEstimate: true},
		}}
		txn, snap := newStubbedTxn(t, mv)

		// must not panic, and must fall through to base rather than returning junk
		got := txn.GetState(addr, types.Hash{0x01})
		require.Equal(t, snap.storage[types.Hash{0x01}], got,
			"a blocked read must fall through to base so execution stays on well-formed values")

		blockedOn, ok := txn.BlockedOn()
		require.True(t, ok, "the incarnation must be latched as blocked")
		require.Equal(t, 3, blockedOn, "it must name the tx it has to wait for")
	})

	t.Run("account read", func(t *testing.T) {
		mv := &stubMV{versions: map[Key]stubVersion{
			NewAddressKey(addr): {txIndex: 2, isEstimate: true},
		}}
		txn, _ := newStubbedTxn(t, mv)

		require.Equal(t, uint64(7), txn.GetNonce(addr),
			"a blocked account read must fall through to base")

		blockedOn, ok := txn.BlockedOn()
		require.True(t, ok)
		require.Equal(t, 2, blockedOn)
	})
}

// TestTxnMVCC_BlockedIncarnationNeverValidates is the backstop that makes the sticky error safe:
// even if a driver forgot to consult BlockedOn, validation must refuse the incarnation, so its
// garbage results can never be mistaken for a committed transaction.
//
// The subtle case is the second one. A blocked read gets recorded as having resolved to base, so
// if the transaction it was waiting on re-executes and no longer writes that key at all, the
// read-set matches reality perfectly and every version-comparison check passes. Only the latch
// itself still knows the incarnation ran on a value that never existed.
func TestTxnMVCC_BlockedIncarnationNeverValidates(t *testing.T) {
	addr := types.StringToAddress("0xabc")
	slot := types.Hash{0x01}
	estimateKey := NewStateKey(addr, slot)

	t.Run("while the estimate is still present", func(t *testing.T) {
		mv := &stubMV{versions: map[Key]stubVersion{estimateKey: {txIndex: 3, isEstimate: true}}}
		txn, _ := newStubbedTxn(t, mv)

		require.True(t, txn.Validate(), "a fresh incarnation with no reads is trivially valid")

		txn.GetState(addr, slot)

		require.False(t, txn.Validate(), "a blocked incarnation must never validate")
	})

	t.Run("after the awaited tx re-executes without writing the key", func(t *testing.T) {
		mv := &stubMV{versions: map[Key]stubVersion{estimateKey: {txIndex: 3, isEstimate: true}}}
		txn, _ := newStubbedTxn(t, mv)

		txn.GetState(addr, slot)

		// tx 3 re-ran and this time wrote nothing here, so the placeholder is gone and every
		// key the incarnation recorded now genuinely resolves to base, exactly as recorded
		delete(mv.versions, estimateKey)

		require.False(t, txn.Validate(),
			"the latch must still reject it - nothing in the read-set can reveal the blockage")
	})
}

// TestTxnMVCC_BlockedIncarnationStopsConsultingMV records the performance half of the design: an
// incarnation that is already doomed keeps running (there is no way to unwind the EVM stack
// without a panic) but must stop hammering the shared multi-version store to get there.
func TestTxnMVCC_BlockedIncarnationStopsConsultingMV(t *testing.T) {
	addr := types.StringToAddress("0xabc")
	other := types.StringToAddress("0xdef")
	mv := &stubMV{versions: map[Key]stubVersion{
		NewAddressKey(addr): {txIndex: 1, isEstimate: true},
	}}
	txn, _ := newStubbedTxn(t, mv)

	txn.GetNonce(addr)
	require.Positive(t, mv.reads, "the first read has to reach mv to discover the estimate")

	readsWhenBlocked := mv.reads

	txn.GetNonce(other)
	txn.GetState(other, types.Hash{0x09})
	txn.GetBalance(other)

	require.Equal(t, readsWhenBlocked, mv.reads,
		"once blocked, further reads must skip mv entirely rather than add contention")
}

// TestTxnMVCC_UnblockedReadsAreUnaffected guards against the latch mis-firing: ordinary reads
// resolve normally and leave the incarnation valid.
func TestTxnMVCC_UnblockedReadsAreUnaffected(t *testing.T) {
	addr := types.StringToAddress("0xabc")
	slot := types.Hash{0x01}
	mv := &stubMV{versions: map[Key]stubVersion{
		NewStateKey(addr, slot): {val: types.Hash{0xBB}, txIndex: 3, incarnation: 0},
	}}
	txn, _ := newStubbedTxn(t, mv)

	require.Equal(t, types.Hash{0xBB}, txn.GetState(addr, slot),
		"a normal versioned read must return the version's value, not base")

	_, ok := txn.BlockedOn()
	require.False(t, ok, "a normal read must not latch anything")
	require.True(t, txn.Validate(), "and must leave the incarnation valid")
}
