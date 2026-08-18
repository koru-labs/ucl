package state

import (
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/require"
)

// arAddr and arHash build deterministic, order-preserving fixtures: under
// bytes.Compare, arAddr(1) < arAddr(2) < arAddr(3) and likewise for hashes.
// The "ar" prefix avoids clashing with fixtures declared in other test files
// of package state.
func arAddr(b byte) types.Address {
	var a types.Address

	a[len(a)-1] = b

	return a
}

func arHash(b byte) types.Hash {
	var h types.Hash

	h[len(h)-1] = b

	return h
}

// AccountAccessRecord
func TestAccountAccessRecord_New(t *testing.T) {
	t.Parallel()

	r := NewAccountAccessRecord()

	require.NotNil(t, r.StorageChanges)
	require.NotNil(t, r.BalanceChanges)
	require.NotNil(t, r.NonceChanges)
	require.NotNil(t, r.CodeChanges)

	require.Empty(t, r.StorageChanges)
	require.Empty(t, r.BalanceChanges)
	require.Empty(t, r.NonceChanges)
	require.Empty(t, r.CodeChanges)
}

func TestAccountAccessRecord_RecordStorageChange(t *testing.T) {
	t.Parallel()

	r := NewAccountAccessRecord()
	slot1, slot2 := arHash(0x10), arHash(0x20)
	val1, val2 := arHash(0xA1), arHash(0xA2)

	r.RecordStorageChange(1, slot1, val1)
	r.RecordStorageChange(2, slot1, val2) // same slot, later tx
	r.RecordStorageChange(1, slot2, val1) // different slot, same tx

	require.Equal(t, val1, r.StorageChanges[slot1][1])
	require.Equal(t, val2, r.StorageChanges[slot1][2])
	require.Equal(t, val1, r.StorageChanges[slot2][1])
	require.Len(t, r.StorageChanges[slot1], 2)
	require.Len(t, r.StorageChanges[slot2], 1)
}

func TestAccountAccessRecord_RecordStorageChange_SameTxOverwrites(t *testing.T) {
	t.Parallel()

	r := NewAccountAccessRecord()
	slot := arHash(0x10)

	r.RecordStorageChange(1, slot, arHash(0xA1))
	r.RecordStorageChange(1, slot, arHash(0xA2)) // last write wins for a tx index

	require.Len(t, r.StorageChanges[slot], 1)
	require.Equal(t, arHash(0xA2), r.StorageChanges[slot][1])
}

func TestAccountAccessRecord_RecordBalanceChange_Clones(t *testing.T) {
	t.Parallel()

	r := NewAccountAccessRecord()
	b := big.NewInt(100)

	r.RecordBalanceChange(1, b)
	b.SetInt64(999) // mutate the caller's big.Int after recording

	require.Equal(t, big.NewInt(100), r.BalanceChanges[1],
		"recorded balance must be a clone, not aliased to the caller's big.Int")
}

func TestAccountAccessRecord_RecordNonceChange(t *testing.T) {
	t.Parallel()

	r := NewAccountAccessRecord()
	r.RecordNonceChange(3, 42)

	require.Equal(t, uint64(42), r.NonceChanges[3])
}

func TestAccountAccessRecord_RecordCodeChange_Clones(t *testing.T) {
	t.Parallel()

	r := NewAccountAccessRecord()
	code := []byte{0x01, 0x02, 0x03}

	r.RecordCodeChange(1, code)
	code[0] = 0xFF // mutate the caller's slice after recording

	require.Equal(t, []byte{0x01, 0x02, 0x03}, r.CodeChanges[1],
		"recorded code must be a clone")
}

// BlockAccessRecord
func TestBlockAccessRecord_GetOrCreate(t *testing.T) {
	t.Parallel()

	r := NewBlockAccessRecord()

	first := r.GetOrCreate(arAddr(1))
	require.NotNil(t, first)

	second := r.GetOrCreate(arAddr(1))
	require.Same(t, first, second,
		"GetOrCreate must return the same instance for a repeated address")
}

func TestBlockAccessRecord_Insert_Nil(t *testing.T) {
	t.Parallel()

	r := NewBlockAccessRecord()

	require.NotPanics(t, func() { r.Insert(nil) })
	require.Empty(t, r)
}

func TestBlockAccessRecord_Insert_AllFields(t *testing.T) {
	t.Parallel()

	const txIndex uint64 = 5
	addr := arAddr(1)
	slot := arHash(0x10)
	val := arHash(0xAA)

	rec := NewTxAccessRecorder(txIndex)
	rec.RecordBalanceChange(addr, big.NewInt(100))
	rec.RecordNonceChange(addr, 7)
	rec.RecordCodeChange(addr, []byte{0xCA, 0xFE})
	rec.RecordStorageChange(addr, slot, val)

	block := NewBlockAccessRecord()
	block.Insert(rec)

	acc := block[addr]
	require.NotNil(t, acc)
	require.Equal(t, big.NewInt(100), acc.BalanceChanges[txIndex])
	require.Equal(t, uint64(7), acc.NonceChanges[txIndex])
	require.Equal(t, []byte{0xCA, 0xFE}, acc.CodeChanges[txIndex])
	require.Equal(t, val, acc.StorageChanges[slot][txIndex])
}

func TestBlockAccessRecord_Insert_SkipsUnsetFields(t *testing.T) {
	t.Parallel()

	addr := arAddr(1)
	slot := arHash(0x10)

	rec := NewTxAccessRecorder(1)
	rec.RecordStorageChange(addr, slot, arHash(0xAA)) // only storage touched

	block := NewBlockAccessRecord()
	block.Insert(rec)

	acc := block[addr]
	require.NotNil(t, acc)
	require.Empty(t, acc.BalanceChanges, "unset balance must not be inserted")
	require.Empty(t, acc.NonceChanges, "unset nonce must not be inserted")
	require.Empty(t, acc.CodeChanges, "unset code must not be inserted")
	require.Contains(t, acc.StorageChanges, slot)
}

func TestBlockAccessRecord_Insert_ZeroNonceIsRecorded(t *testing.T) {
	t.Parallel()

	addr := arAddr(1)

	rec := NewTxAccessRecorder(1)
	rec.RecordNonceChange(addr, 0) // explicit zero differs from "not set"

	block := NewBlockAccessRecord()
	block.Insert(rec)

	got, ok := block[addr].NonceChanges[1]
	require.True(t, ok, "an explicitly recorded zero nonce must be present")
	require.Equal(t, uint64(0), got)
}

func TestBlockAccessRecord_Insert_MultipleTxAccumulate(t *testing.T) {
	t.Parallel()

	addr := arAddr(1)
	block := NewBlockAccessRecord()

	rec1 := NewTxAccessRecorder(1)
	rec1.RecordBalanceChange(addr, big.NewInt(10))
	block.Insert(rec1)

	rec2 := NewTxAccessRecorder(2)
	rec2.RecordBalanceChange(addr, big.NewInt(20))
	block.Insert(rec2)

	acc := block[addr]
	require.Equal(t, big.NewInt(10), acc.BalanceChanges[1])
	require.Equal(t, big.NewInt(20), acc.BalanceChanges[2])
}

func TestBlockAccessRecord_Pack_Empty(t *testing.T) {
	t.Parallel()

	require.Empty(t, NewBlockAccessRecord().Pack())
}

func TestBlockAccessRecord_Pack_SortsEverythingAscending(t *testing.T) {
	t.Parallel()

	block := NewBlockAccessRecord()

	aHi := arAddr(0x03)
	aLo := arAddr(0x01)

	// Populate the high account with out-of-order slots and tx indices.
	hi := block.GetOrCreate(aHi)
	hi.RecordStorageChange(2, arHash(0x20), arHash(0xB2))
	hi.RecordStorageChange(1, arHash(0x20), arHash(0xB1))
	hi.RecordStorageChange(1, arHash(0x10), arHash(0xA1))
	hi.RecordBalanceChange(2, big.NewInt(2))
	hi.RecordBalanceChange(1, big.NewInt(1))
	hi.RecordNonceChange(2, 20)
	hi.RecordNonceChange(1, 10)
	hi.RecordCodeChange(2, []byte{0x02})
	hi.RecordCodeChange(1, []byte{0x01})

	// A second, lexicographically smaller account inserted afterwards.
	block.GetOrCreate(aLo).RecordNonceChange(1, 99)

	packed := block.Pack()
	require.Len(t, packed, 2)

	// (1) accounts ordered lexicographically by address.
	require.Equal(t, aLo, packed[0].Address)
	require.Equal(t, aHi, packed[1].Address)

	hiPacked := packed[1]

	// (2) storage slots ordered lexicographically by slot hash.
	require.Len(t, hiPacked.StorageChanges, 2)
	require.Equal(t, arHash(0x10), hiPacked.StorageChanges[0].Slot)
	require.Equal(t, arHash(0x20), hiPacked.StorageChanges[1].Slot)

	// (3) per-slot changes ascending by tx index, with the right values.
	slot20 := hiPacked.StorageChanges[1].SlotChanges
	require.Len(t, slot20, 2)
	require.Equal(t, uint64(1), slot20[0].TxIndex)
	require.Equal(t, uint64(2), slot20[1].TxIndex)
	require.Equal(t, arHash(0xB1), slot20[0].Value)
	require.Equal(t, arHash(0xB2), slot20[1].Value)

	// Balance changes ascending by tx index.
	require.Len(t, hiPacked.BalanceChanges, 2)
	require.Equal(t, uint64(1), hiPacked.BalanceChanges[0].TxIndex)
	require.Equal(t, uint64(2), hiPacked.BalanceChanges[1].TxIndex)
	require.Equal(t, big.NewInt(1), hiPacked.BalanceChanges[0].Balance)
	require.Equal(t, big.NewInt(2), hiPacked.BalanceChanges[1].Balance)

	// Nonce changes ascending by tx index.
	require.Len(t, hiPacked.NonceChanges, 2)
	require.Equal(t, uint64(1), hiPacked.NonceChanges[0].TxIndex)
	require.Equal(t, uint64(2), hiPacked.NonceChanges[1].TxIndex)

	// Code changes ascending by tx index.
	require.Len(t, hiPacked.CodeChanges, 2)
	require.Equal(t, uint64(1), hiPacked.CodeChanges[0].TxIndex)
	require.Equal(t, uint64(2), hiPacked.CodeChanges[1].TxIndex)
}

func TestBlockAccessRecord_Pack_ClonesBalanceAndCode(t *testing.T) {
	t.Parallel()

	addr := arAddr(1)

	block := NewBlockAccessRecord()
	acc := block.GetOrCreate(addr)
	acc.RecordBalanceChange(1, big.NewInt(100))
	acc.RecordCodeChange(1, []byte{0x01, 0x02})

	packed := block.Pack()

	// Mutate the source after packing; the packed copy must be unaffected.
	block[addr].BalanceChanges[1].SetInt64(999)
	block[addr].CodeChanges[1][0] = 0xFF

	require.Equal(t, big.NewInt(100), packed[0].BalanceChanges[0].Balance)
	require.Equal(t, []byte{0x01, 0x02}, packed[0].CodeChanges[0].Code)
}

// TxAccessRecorder: construction, getters, nil-safety
func TestTxAccessRecorder_NilReceiver(t *testing.T) {
	t.Parallel()

	var r *TxAccessRecorder

	require.NotPanics(t, func() {
		r.Snapshot()
		r.Commit()
		r.Revert()
		r.RecordStorageChange(arAddr(1), arHash(1), arHash(2))
		r.RecordBalanceChange(arAddr(1), big.NewInt(1))
		r.RecordNonceChange(arAddr(1), 1)
		r.RecordCodeChange(arAddr(1), []byte{0x01})
	})

	_, ok := r.GetStorage(arAddr(1), arHash(1))
	require.False(t, ok)
	_, ok = r.GetBalance(arAddr(1))
	require.False(t, ok)
	_, ok = r.GetNonce(arAddr(1))
	require.False(t, ok)
	_, ok = r.GetCode(arAddr(1))
	require.False(t, ok)
}

func TestTxAccessRecorder_RecordAndGet(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(3)
	addr := arAddr(1)

	r.RecordStorageChange(addr, arHash(1), arHash(0xAA))
	r.RecordBalanceChange(addr, big.NewInt(500))
	r.RecordNonceChange(addr, 9)
	r.RecordCodeChange(addr, []byte{0xCA, 0xFE})

	gotStorage, ok := r.GetStorage(addr, arHash(1))
	require.True(t, ok)
	require.Equal(t, arHash(0xAA), gotStorage)

	gotBal, ok := r.GetBalance(addr)
	require.True(t, ok)
	require.Equal(t, big.NewInt(500), gotBal)

	gotNonce, ok := r.GetNonce(addr)
	require.True(t, ok)
	require.Equal(t, uint64(9), gotNonce)

	gotCode, ok := r.GetCode(addr)
	require.True(t, ok)
	require.Equal(t, []byte{0xCA, 0xFE}, gotCode)
}

func TestTxAccessRecorder_Get_NotModified(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	// Account entirely absent.
	_, ok := r.GetStorage(addr, arHash(1))
	require.False(t, ok)
	_, ok = r.GetBalance(addr)
	require.False(t, ok)
	_, ok = r.GetNonce(addr)
	require.False(t, ok)
	_, ok = r.GetCode(addr)
	require.False(t, ok)

	// Account present (via a storage write) but the other fields never set.
	r.RecordStorageChange(addr, arHash(1), arHash(2))

	_, ok = r.GetBalance(addr)
	require.False(t, ok, "balance was never set")
	_, ok = r.GetNonce(addr)
	require.False(t, ok, "nonce was never set")
	_, ok = r.GetCode(addr)
	require.False(t, ok, "code was never set")
	_, ok = r.GetStorage(addr, arHash(0xFF))
	require.False(t, ok, "unwritten slot")
}

func TestTxAccessRecorder_RecordBalance_Clones(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)
	b := big.NewInt(100)

	r.RecordBalanceChange(addr, b)
	b.SetInt64(999)

	got, _ := r.GetBalance(addr)
	require.Equal(t, big.NewInt(100), got)
}

func TestTxAccessRecorder_RecordCode_Clones(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)
	code := []byte{0x01, 0x02}

	r.RecordCodeChange(addr, code)
	code[0] = 0xFF

	got, _ := r.GetCode(addr)
	require.Equal(t, []byte{0x01, 0x02}, got)
}

// TxAccessRecorder: snapshot / commit / revert
func TestTxAccessRecorder_Revert_RemovesNewAccount(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	r.Snapshot()
	r.RecordBalanceChange(addr, big.NewInt(10))
	require.Contains(t, r.current, addr)

	r.Revert()
	require.NotContains(t, r.current, addr,
		"an account first touched after the snapshot must be removed on revert")
}

func TestTxAccessRecorder_Revert_RestoresBalance(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	r.RecordBalanceChange(addr, big.NewInt(10)) // before snapshot
	r.Snapshot()
	r.RecordBalanceChange(addr, big.NewInt(20)) // after snapshot

	r.Revert()

	got, ok := r.GetBalance(addr)
	require.True(t, ok)
	require.Equal(t, big.NewInt(10), got, "balance must be restored to its pre-snapshot value")
}

func TestTxAccessRecorder_Revert_RestoresNonce(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	r.RecordNonceChange(addr, 1)
	r.Snapshot()
	r.RecordNonceChange(addr, 2)

	r.Revert()

	got, ok := r.GetNonce(addr)
	require.True(t, ok)
	require.Equal(t, uint64(1), got)
}

func TestTxAccessRecorder_Revert_RestoresCode(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	r.RecordCodeChange(addr, []byte{0x01})
	r.Snapshot()
	r.RecordCodeChange(addr, []byte{0x02})

	r.Revert()

	got, ok := r.GetCode(addr)
	require.True(t, ok)
	require.Equal(t, []byte{0x01}, got)
}

// A field that was nil before the snapshot must go back to nil (unset) on
// revert, while the account itself -- created before the snapshot -- survives.
func TestTxAccessRecorder_Revert_RestoresUnsetField(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	r.RecordNonceChange(addr, 1) // creates the account, leaves balance unset
	r.Snapshot()
	r.RecordBalanceChange(addr, big.NewInt(50))

	_, ok := r.GetBalance(addr)
	require.True(t, ok)

	r.Revert()

	_, ok = r.GetBalance(addr)
	require.False(t, ok, "balance must return to its pre-snapshot unset state")
	require.Contains(t, r.current, addr, "account created before the snapshot survives")
}

func TestTxAccessRecorder_Revert_Storage_NewDeleted_ExistingRestored(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)
	existing, fresh := arHash(1), arHash(2)

	r.RecordStorageChange(addr, existing, arHash(0xA1)) // before snapshot
	r.Snapshot()
	r.RecordStorageChange(addr, existing, arHash(0xA2)) // overwrite existing slot
	r.RecordStorageChange(addr, fresh, arHash(0xB1))    // brand-new slot

	r.Revert()

	got, ok := r.GetStorage(addr, existing)
	require.True(t, ok)
	require.Equal(t, arHash(0xA1), got, "pre-existing slot restored to its earlier value")

	_, ok = r.GetStorage(addr, fresh)
	require.False(t, ok, "slot first written after the snapshot must be deleted")
}

func TestTxAccessRecorder_Revert_MultipleAccounts(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	a, b := arAddr(1), arAddr(2)

	r.RecordBalanceChange(a, big.NewInt(1)) // a exists before snapshot
	r.Snapshot()
	r.RecordBalanceChange(a, big.NewInt(2))
	r.RecordBalanceChange(b, big.NewInt(3)) // b created after snapshot

	r.Revert()

	got, ok := r.GetBalance(a)
	require.True(t, ok)
	require.Equal(t, big.NewInt(1), got)
	require.NotContains(t, r.current, b, "account created after the snapshot is removed")
}

func TestTxAccessRecorder_Commit_KeepsChanges(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	r.RecordBalanceChange(addr, big.NewInt(10))
	r.Snapshot()
	r.RecordBalanceChange(addr, big.NewInt(20))
	r.Commit()

	got, _ := r.GetBalance(addr)
	require.Equal(t, big.NewInt(20), got, "commit must keep post-snapshot changes")
}

func TestTxAccessRecorder_CommitRevert_NoSnapshot_NoOp(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)
	r.RecordBalanceChange(addr, big.NewInt(10))

	require.NotPanics(t, func() { r.Commit() })
	require.NotPanics(t, func() { r.Revert() })

	got, ok := r.GetBalance(addr)
	require.True(t, ok)
	require.Equal(t, big.NewInt(10), got,
		"commit/revert with no active snapshot must not touch state")
}

// Nested: commit the inner snapshot (keeping its change), then revert the outer
// one -- which must unwind everything, including the account creation.
func TestTxAccessRecorder_NestedSnapshots_CommitInner_RevertOuter(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	r.Snapshot() // outer
	r.RecordBalanceChange(addr, big.NewInt(10))

	r.Snapshot() // inner
	r.RecordBalanceChange(addr, big.NewInt(20))
	r.Commit() // keep inner change

	got, _ := r.GetBalance(addr)
	require.Equal(t, big.NewInt(20), got)

	r.Revert() // unwind the outer snapshot

	require.NotContains(t, r.current, addr,
		"reverting the outer snapshot must undo the account creation entirely")
}

func TestTxAccessRecorder_Revert_ThenRecordAgain(t *testing.T) {
	t.Parallel()

	r := NewTxAccessRecorder(1)
	addr := arAddr(1)

	r.Snapshot()
	r.RecordBalanceChange(addr, big.NewInt(10))
	r.Revert() // account gone, journal empty

	r.RecordBalanceChange(addr, big.NewInt(99)) // must work cleanly afterwards

	got, ok := r.GetBalance(addr)
	require.True(t, ok)
	require.Equal(t, big.NewInt(99), got)
	require.Empty(t, r.snapshots)
}

// End-to-end: what survives a revert on the recorder is exactly what Insert
// merges into the block record.
func TestTxAccessRecorder_Insert_AfterRevert(t *testing.T) {
	t.Parallel()

	const txIndex uint64 = 4
	addr := arAddr(1)

	r := NewTxAccessRecorder(txIndex)
	r.RecordBalanceChange(addr, big.NewInt(10))
	r.Snapshot()
	r.RecordBalanceChange(addr, big.NewInt(20))
	r.Revert() // balance back to 10

	block := NewBlockAccessRecord()
	block.Insert(r)

	require.Equal(t, big.NewInt(10), block[addr].BalanceChanges[txIndex])
}
