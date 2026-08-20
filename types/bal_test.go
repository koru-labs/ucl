package types

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate_DuplicateAccount(t *testing.T) {
	addr := StringToAddress("0x1")

	bal := BlockAccessRecord{
		{Address: addr},
		{Address: addr},
	}

	require.Error(t, bal.Validate())
}

func TestValidate_DuplicateStorageSlot(t *testing.T) {
	addr := StringToAddress("0x1")
	slot := StringToHash("0x1")

	bal := BlockAccessRecord{
		{
			Address: addr,
			StorageChanges: []StorageChange{
				{Slot: slot},
				{Slot: slot},
			},
		},
	}

	require.Error(t, bal.Validate())
}

func TestValidate_DuplicateSlotTxIndex(t *testing.T) {
	addr := StringToAddress("0x1")
	slot := StringToHash("0x1")

	bal := BlockAccessRecord{
		{
			Address: addr,
			StorageChanges: []StorageChange{
				{
					Slot: slot,
					SlotChanges: []SlotChange{
						{TxIndex: 5},
						{TxIndex: 5},
					},
				},
			},
		},
	}

	require.Error(t, bal.Validate())
}

func TestValidate_DuplicateBalanceTxIndex(t *testing.T) {
	addr := StringToAddress("0x1")

	bal := BlockAccessRecord{
		{
			Address: addr,
			BalanceChanges: []BalanceChange{
				{TxIndex: 1},
				{TxIndex: 1},
			},
		},
	}

	require.Error(t, bal.Validate())
}

func TestSlotBefore_MissingSlot(t *testing.T) {
	addr := StringToAddress("0x1")

	bal := BlockAccessRecord{
		{
			Address: addr,
		},
	}

	_, ok := bal.SlotBefore(addr, StringToHash("0x123"), 10)
	require.False(t, ok)
}

func TestBalanceBefore_NoBalanceChanges(t *testing.T) {
	addr := StringToAddress("0x1")

	bal := BlockAccessRecord{
		{
			Address: addr,
		},
	}

	_, ok := bal.BalanceBefore(addr, 100)
	require.False(t, ok)
}

func TestHashChanges(t *testing.T) {
	bal1 := BlockAccessRecord{
		{
			Address: StringToAddress("0x1"),
		},
	}

	bal2 := BlockAccessRecord{
		{
			Address: StringToAddress("0x2"),
		},
	}

	require.NotEqual(t, bal1.Hash(), bal2.Hash())
}

func TestEmptyBlockAccessRecord(t *testing.T) {
	var bal BlockAccessRecord

	require.NoError(t, bal.Validate())

	require.Equal(t, bal.Hash(), bal.Hash())
}

func TestBalanceBefore_MultipleAccounts(t *testing.T) {
	addr1 := StringToAddress("0x1")
	addr2 := StringToAddress("0x2")

	bal := BlockAccessRecord{
		{
			Address: addr1,
			BalanceChanges: []BalanceChange{
				{TxIndex: 1, Balance: big.NewInt(10)},
			},
		},
		{
			Address: addr2,
			BalanceChanges: []BalanceChange{
				{TxIndex: 2, Balance: big.NewInt(20)},
			},
		},
	}

	got, ok := bal.BalanceBefore(addr2, 3)

	require.True(t, ok)
	require.Equal(t, int64(20), got.Int64())
}

func TestValidate_EmptySlices(t *testing.T) {
	bal := BlockAccessRecord{
		{
			Address: StringToAddress("0x1"),
		},
	}

	require.NoError(t, bal.Validate())
}

func TestBlockAccessRecordBalanceBefore(t *testing.T) {
	addr := StringToAddress("0x1")

	bal := BlockAccessRecord{
		{
			Address: addr,
			BalanceChanges: []BalanceChange{
				{TxIndex: 2, Balance: big.NewInt(20)},
				{TxIndex: 5, Balance: big.NewInt(50)},
				{TxIndex: 9, Balance: big.NewInt(90)},
			},
		},
	}

	tests := []struct {
		name    string
		tx      uint64
		found   bool
		balance int64
	}{
		{"before first", 1, false, 0},
		{"exact first", 2, false, 0},
		{"between", 3, true, 20},
		{"exact second", 5, true, 20},
		{"after last", 100, true, 90},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := bal.BalanceBefore(addr, tt.tx)
			require.Equal(t, tt.found, ok)

			if ok {
				require.Equal(t, tt.balance, got.Int64())
			}
		})
	}
}

func TestBlockAccessRecordNonceBefore(t *testing.T) {
	addr := StringToAddress("0x1")

	bal := BlockAccessRecord{
		{
			Address: addr,
			NonceChanges: []NonceChange{
				{TxIndex: 1, Nonce: 7},
				{TxIndex: 3, Nonce: 8},
				{TxIndex: 6, Nonce: 9},
			},
		},
	}

	tests := []struct {
		tx    uint64
		ok    bool
		nonce uint64
	}{
		{0, false, 0},
		{1, false, 0},
		{2, true, 7},
		{3, true, 7},
		{10, true, 9},
	}

	for _, tt := range tests {
		got, ok := bal.NonceBefore(addr, tt.tx)
		require.Equal(t, tt.ok, ok)

		if ok {
			require.Equal(t, tt.nonce, got)
		}
	}
}

func TestBlockAccessRecordCodeBefore(t *testing.T) {
	addr := StringToAddress("0x1")

	bal := BlockAccessRecord{
		{
			Address: addr,
			CodeChanges: []CodeChange{
				{TxIndex: 4, Code: []byte{1}},
				{TxIndex: 7, Code: []byte{2}},
			},
		},
	}

	code, ok := bal.CodeBefore(addr, 4)
	require.False(t, ok)
	require.Nil(t, code)

	code, ok = bal.CodeBefore(addr, 6)
	require.True(t, ok)
	require.Equal(t, []byte{1}, code)

	code, ok = bal.CodeBefore(addr, 100)
	require.True(t, ok)
	require.Equal(t, []byte{2}, code)
}

func TestBlockAccessRecordSlotBefore(t *testing.T) {
	addr := StringToAddress("0x1")

	slot := StringToHash("0xaa")

	v1 := StringToHash("0x11")
	v2 := StringToHash("0x22")

	bal := BlockAccessRecord{
		{
			Address: addr,
			StorageChanges: []StorageChange{
				{
					Slot: slot,
					SlotChanges: []SlotChange{
						{TxIndex: 2, Value: v1},
						{TxIndex: 5, Value: v2},
					},
				},
			},
		},
	}

	_, ok := bal.SlotBefore(addr, slot, 2)
	require.False(t, ok)

	got, ok := bal.SlotBefore(addr, slot, 4)
	require.True(t, ok)
	require.Equal(t, v1, got)

	got, ok = bal.SlotBefore(addr, slot, 100)
	require.True(t, ok)
	require.Equal(t, v2, got)
}

func TestLookupMissingAccount(t *testing.T) {
	bal := BlockAccessRecord{}

	_, ok := bal.BalanceBefore(StringToAddress("0x1"), 1)
	require.False(t, ok)

	_, ok = bal.NonceBefore(StringToAddress("0x1"), 1)
	require.False(t, ok)

	_, ok = bal.CodeBefore(StringToAddress("0x1"), 1)
	require.False(t, ok)

	_, ok = bal.SlotBefore(StringToAddress("0x1"), StringToHash("0x1"), 1)
	require.False(t, ok)
}

func TestHashDeterministic(t *testing.T) {
	bal := BlockAccessRecord{
		{
			Address: StringToAddress("0x1"),
			BalanceChanges: []BalanceChange{
				{
					TxIndex: 1,
					Balance: big.NewInt(100),
				},
			},
		},
	}

	require.Equal(t, bal.Hash(), bal.Hash())
}

func TestValidate(t *testing.T) {
	addr1 := StringToAddress("0x1")
	addr2 := StringToAddress("0x2")

	slot1 := StringToHash("0x1")
	slot2 := StringToHash("0x2")

	tests := []struct {
		name  string
		build func() BlockAccessRecord
		ok    bool
	}{
		{
			name: "valid",
			ok:   true,
			build: func() BlockAccessRecord {
				return BlockAccessRecord{
					{
						Address: addr1,
						StorageChanges: []StorageChange{
							{
								Slot: slot1,
								SlotChanges: []SlotChange{
									{TxIndex: 1},
									{TxIndex: 2},
								},
							},
						},
						BalanceChanges: []BalanceChange{
							{TxIndex: 1},
							{TxIndex: 2},
						},
						NonceChanges: []NonceChange{
							{TxIndex: 1},
							{TxIndex: 2},
						},
						CodeChanges: []CodeChange{
							{TxIndex: 1},
							{TxIndex: 2},
						},
					},
					{
						Address: addr2,
					},
				}
			},
		},
		{
			name: "accounts not sorted",
			ok:   false,
			build: func() BlockAccessRecord {
				return BlockAccessRecord{
					{Address: addr2},
					{Address: addr1},
				}
			},
		},
		{
			name: "storage slots not sorted",
			ok:   false,
			build: func() BlockAccessRecord {
				return BlockAccessRecord{
					{
						Address: addr1,
						StorageChanges: []StorageChange{
							{Slot: slot2},
							{Slot: slot1},
						},
					},
				}
			},
		},
		{
			name: "slot changes unordered",
			ok:   false,
			build: func() BlockAccessRecord {
				return BlockAccessRecord{
					{
						Address: addr1,
						StorageChanges: []StorageChange{
							{
								Slot: slot1,
								SlotChanges: []SlotChange{
									{TxIndex: 2},
									{TxIndex: 1},
								},
							},
						},
					},
				}
			},
		},
		{
			name: "balance unordered",
			ok:   false,
			build: func() BlockAccessRecord {
				return BlockAccessRecord{
					{
						Address: addr1,
						BalanceChanges: []BalanceChange{
							{TxIndex: 2},
							{TxIndex: 1},
						},
					},
				}
			},
		},
		{
			name: "nonce unordered",
			ok:   false,
			build: func() BlockAccessRecord {
				return BlockAccessRecord{
					{
						Address: addr1,
						NonceChanges: []NonceChange{
							{TxIndex: 2},
							{TxIndex: 1},
						},
					},
				}
			},
		},
		{
			name: "code unordered",
			ok:   false,
			build: func() BlockAccessRecord {
				return BlockAccessRecord{
					{
						Address: addr1,
						CodeChanges: []CodeChange{
							{TxIndex: 2},
							{TxIndex: 1},
						},
					},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build().Validate()
			if tt.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
