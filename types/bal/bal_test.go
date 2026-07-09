package bal

import (
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/require"
)

var (
	addrA = types.Address{0x0a}
	addrB = types.Address{0x0b}

	slotX = types.Hash{0x01}
	slotY = types.Hash{0x02}
	slotZ = types.Hash{0x03}

	valV = types.Hash{0xff}
)

// A write to a slot must remove any prior read of that slot, and subsequent
// reads of a written slot must be ignored.
func TestAccountRecord_StorageWriteDropsRead(t *testing.T) {
	t.Parallel()

	a := NewAccountAccessRecord()

	a.RecordStorageRead(slotX)
	require.Contains(t, a.StorageReads, slotX)

	a.RecordStorageWrite(1, slotX, valV)
	require.NotContains(t, a.StorageReads, slotX, "write must drop the read")
	require.Contains(t, a.StorageWrites, slotX)

	// reading an already-written slot is a no-op
	a.RecordStorageRead(slotX)
	require.NotContains(t, a.StorageReads, slotX)
}

func TestAccountRecord_BalanceChangeIsCopied(t *testing.T) {
	t.Parallel()

	a := NewAccountAccessRecord()

	src := big.NewInt(100)
	a.RecordBalanceChange(1, src)

	// mutating the source must not affect the stored value
	src.SetInt64(999)
	require.Equal(t, int64(100), a.BalanceChanges[1].Int64())
}

func TestBlockRecord_Merge_DisjointAndOverlap(t *testing.T) {
	t.Parallel()

	base := NewBlockAccessListRecord()
	baseA := base.GetOrCreate(addrA)
	baseA.RecordStorageRead(slotX) // will be superseded by other's write
	baseA.RecordBalanceChange(1, big.NewInt(10))

	other := NewBlockAccessListRecord()
	otherA := other.GetOrCreate(addrA)
	otherA.RecordStorageWrite(2, slotX, valV)
	otherB := other.GetOrCreate(addrB) // disjoint account
	otherB.RecordNonceChange(1, 3)

	base.Merge(other)

	// addrA: read on slotX replaced by a write, balance preserved
	mergedA := base.Accounts[addrA]
	require.Contains(t, mergedA.StorageWrites, slotX)
	require.NotContains(t, mergedA.StorageReads, slotX,
		"a slot written by the merged record must leave the read set")
	require.Equal(t, int64(10), mergedA.BalanceChanges[1].Int64())

	// addrB: whole account brought over
	mergedB := base.Accounts[addrB]
	require.NotNil(t, mergedB)
	require.Equal(t, uint64(3), mergedB.NonceChanges[1])
}

// If the base already has a write for a slot, a read of the same slot coming in
// via Merge must NOT be added to the read set.
func TestBlockRecord_Merge_ReadNotAddedWhenAlreadyWritten(t *testing.T) {
	t.Parallel()

	base := NewBlockAccessListRecord()
	base.GetOrCreate(addrA).RecordStorageWrite(1, slotX, valV)

	other := NewBlockAccessListRecord()
	other.GetOrCreate(addrA).RecordStorageRead(slotX)

	base.Merge(other)

	acc := base.Accounts[addrA]
	require.Contains(t, acc.StorageWrites, slotX)
	require.NotContains(t, acc.StorageReads, slotX)
}

func TestBlockRecord_Merge_Nil(t *testing.T) {
	t.Parallel()

	base := NewBlockAccessListRecord()
	base.GetOrCreate(addrA).RecordNonceChange(1, 9)

	require.NotPanics(t, func() { base.Merge(nil) })
	require.Equal(t, uint64(9), base.Accounts[addrA].NonceChanges[1])
}

func TestBlockRecord_Copy_DeepIndependence(t *testing.T) {
	t.Parallel()

	rec := NewBlockAccessListRecord()
	a := rec.GetOrCreate(addrA)
	a.RecordStorageWrite(1, slotX, valV)
	a.RecordBalanceChange(1, big.NewInt(50))
	a.RecordCodeChange(1, []byte{0xAA, 0xBB})

	cp := rec.Copy()

	// mutate the original after copying
	a.RecordStorageWrite(1, slotX, types.Hash{0x01})
	a.BalanceChanges[1].SetInt64(9999)
	a.CodeChanges[1][0] = 0x00

	cpA := cp.Accounts[addrA]
	require.Equal(t, valV, cpA.StorageWrites[slotX][1], "copy storage must be independent")
	require.Equal(t, int64(50), cpA.BalanceChanges[1].Int64(), "copy balance must be independent")
	require.Equal(t, []byte{0xAA, 0xBB}, cpA.CodeChanges[1], "copy code must be independent")
}

func TestToEncodingObj_Sorting(t *testing.T) {
	t.Parallel()

	rec := NewBlockAccessListRecord()

	// insert accounts and slots out of order on purpose
	b := rec.GetOrCreate(addrB)
	b.RecordStorageRead(slotZ)

	a := rec.GetOrCreate(addrA)
	a.RecordStorageWrite(3, slotY, valV) // higher slot, higher index first
	a.RecordStorageWrite(1, slotY, valV)
	a.RecordStorageWrite(1, slotX, valV) // lower slot

	enc := rec.ToEncodingObj()

	// accounts sorted ascending by address
	require.Len(t, enc, 2)
	require.Equal(t, addrA, enc[0].Address)
	require.Equal(t, addrB, enc[1].Address)

	// slots within an account sorted ascending
	require.Equal(t, slotX, enc[0].StorageChanges[0].Slot)
	require.Equal(t, slotY, enc[0].StorageChanges[1].Slot)

	// writes within a slot sorted ascending by block access index
	yWrites := enc[0].StorageChanges[1].SlotChanges
	require.Len(t, yWrites, 2)
	require.Equal(t, uint32(1), yWrites[0].BlockAccessIndex)
	require.Equal(t, uint32(3), yWrites[1].BlockAccessIndex)
}

func fullEncodingObj(t *testing.T) BlockAccessList {
	t.Helper()

	rec := NewBlockAccessListRecord()

	a := rec.GetOrCreate(addrA)
	a.RecordStorageWrite(1, slotX, valV)
	a.RecordStorageWrite(2, slotX, types.Hash{0xee})
	a.RecordStorageRead(slotY)
	a.RecordBalanceChange(1, big.NewInt(1_000_000))
	a.RecordNonceChange(1, 7)
	a.RecordCodeChange(2, []byte{0xAA, 0xBB, 0xCC})

	b := rec.GetOrCreate(addrB)
	b.RecordStorageRead(slotZ)

	return rec.ToEncodingObj()
}

func TestBlockAccessList_RLPRoundTrip(t *testing.T) {
	t.Parallel()

	enc := fullEncodingObj(t)
	data := enc.MarshalRLP()

	var got BlockAccessList
	require.NoError(t, got.UnmarshalRLP(data))

	// byte-level round-trip is the robust check (avoids nil-vs-empty slice noise)
	require.Equal(t, data, got.MarshalRLP())

	// spot-check a few decoded fields
	require.Len(t, got, 2)
	require.Equal(t, addrA, got[0].Address)
	require.Equal(t, uint64(7), got[0].NonceChanges[0].PostNonce)
	require.Equal(t, 0, got[0].BalanceChanges[0].PostBalance.Cmp(big.NewInt(1_000_000)))
	require.Equal(t, []byte{0xAA, 0xBB, 0xCC}, got[0].CodeChanges[0].NewCode)
}

func TestBlockAccessList_EmptyRLPRoundTrip(t *testing.T) {
	t.Parallel()

	var empty BlockAccessList
	data := empty.MarshalRLP()

	var got BlockAccessList
	require.NoError(t, got.UnmarshalRLP(data))
	require.Len(t, got, 0)
}

func TestBlockAccessList_Hash_Deterministic(t *testing.T) {
	t.Parallel()

	enc := fullEncodingObj(t)

	require.Equal(t, enc.Hash(), enc.Copy().Hash(), "identical content -> identical hash")

	// the canonical empty-list hash is stable across empty instances
	require.Equal(t, BlockAccessList{}.Hash(), BlockAccessList(nil).Hash())

	// a non-empty list must differ from the empty one
	require.NotEqual(t, BlockAccessList{}.Hash(), enc.Hash())
}

func TestBlockAccessList_Validate_OK(t *testing.T) {
	t.Parallel()

	bl := BlockAccessList{
		{
			Address:      addrA,
			StorageReads: []types.Hash{slotX},
		},
	}

	require.NoError(t, bl.Validate(30_000_000, 1))
}

func TestBlockAccessList_Validate_AccountsNotSorted(t *testing.T) {
	t.Parallel()

	bl := BlockAccessList{
		{Address: addrB},
		{Address: addrA}, // out of order
	}

	require.Error(t, bl.Validate(30_000_000, 1))
}

func TestBlockAccessList_Validate_ReadWriteConflict(t *testing.T) {
	t.Parallel()

	bl := BlockAccessList{
		{
			Address: addrA,
			StorageChanges: []SlotChanges{
				{Slot: slotX, SlotChanges: []StorageWrite{{BlockAccessIndex: 1, PostValue: valV}}},
			},
			StorageReads: []types.Hash{slotX}, // same slot appears as read AND write
		},
	}

	require.Error(t, bl.Validate(30_000_000, 1),
		"a slot must not be reported in both read and write sets")
}

func TestBlockAccessList_Validate_SizeLimitExceeded(t *testing.T) {
	t.Parallel()

	// one account (1 item) + one storage read (1 item) = 2 items.
	// gasLimit 2000 / BALItemCost(2000) = limit 1 -> must fail.
	bl := BlockAccessList{
		{
			Address:      addrA,
			StorageReads: []types.Hash{slotX},
		},
	}

	require.ErrorContains(t, bl.Validate(2000, 1), "size")
}
