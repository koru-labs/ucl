package types

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_BlockAccessRuntime_Merge(t *testing.T) {
	t.Run("merge into empty block access record", func(t *testing.T) {
		dst := NewBlockAccessRuntime()
		src := NewBlockAccessRuntime()

		addr := Address{1}

		src[addr] = NewAccountAccessRuntime()
		src[addr].BalanceChanges[0] = big.NewInt(100)

		dst.Merge(src)

		require.Equal(t, big.NewInt(100), dst[addr].BalanceChanges[0])
	})

	t.Run("merge nil block access record", func(t *testing.T) {
		dst := NewBlockAccessRuntime()

		addr := BytesToAddress([]byte{1})

		dst[addr] = NewAccountAccessRuntime()
		dst[addr].BalanceChanges[0] = big.NewInt(100)

		dst.Merge(nil)

		require.Equal(t, big.NewInt(100), dst[addr].BalanceChanges[0])
	})

	t.Run("merge when there are no merge conflict", func(t *testing.T) {
		dst := NewBlockAccessRuntime()
		src := NewBlockAccessRuntime()

		addr := BytesToAddress([]byte{1})

		dst[addr] = NewAccountAccessRuntime()
		dst[addr].BalanceChanges[0] = big.NewInt(100)
		dst[addr].NonceChanges[0] = 1
		dst[addr].CodeChanges[0] = []byte{1}
		dst[addr].StorageChanges[Hash{}] = map[uint64]Hash{0: BytesToHash([]byte{1})}

		src[addr] = NewAccountAccessRuntime()
		src[addr].BalanceChanges[1] = big.NewInt(200)
		src[addr].NonceChanges[1] = 2
		src[addr].CodeChanges[1] = []byte{2}
		src[addr].StorageChanges[Hash{}] = map[uint64]Hash{1: BytesToHash([]byte{2})}

		dst.Merge(src)

		require.Equal(t, big.NewInt(100), dst[addr].BalanceChanges[0])
		require.Equal(t, big.NewInt(200), dst[addr].BalanceChanges[1])

		require.Equal(t, uint64(1), dst[addr].NonceChanges[0])
		require.Equal(t, uint64(2), dst[addr].NonceChanges[1])

		require.Equal(t, []byte{1}, dst[addr].CodeChanges[0])
		require.Equal(t, []byte{2}, dst[addr].CodeChanges[1])

		require.Equal(t, BytesToHash([]byte{1}), dst[addr].StorageChanges[Hash{}][0])
		require.Equal(t, BytesToHash([]byte{2}), dst[addr].StorageChanges[Hash{}][1])
	})

	t.Run("merge when there are merge conflict", func(t *testing.T) {
		dst := NewBlockAccessRuntime()
		src := NewBlockAccessRuntime()

		addr := BytesToAddress([]byte{1})

		dst[addr] = NewAccountAccessRuntime()
		dst[addr].BalanceChanges[0] = big.NewInt(100)
		dst[addr].NonceChanges[0] = 1
		dst[addr].CodeChanges[0] = []byte{1}
		dst[addr].StorageChanges[Hash{}] = map[uint64]Hash{0: BytesToHash([]byte{1})}

		src[addr] = NewAccountAccessRuntime()
		src[addr].BalanceChanges[0] = big.NewInt(200)
		src[addr].NonceChanges[0] = 2
		src[addr].CodeChanges[0] = []byte{2}
		src[addr].StorageChanges[Hash{}] = map[uint64]Hash{0: BytesToHash([]byte{2})}

		dst.Merge(src)

		require.Equal(t, big.NewInt(200), dst[addr].BalanceChanges[0])
		require.Equal(t, uint64(2), dst[addr].NonceChanges[0])
		require.Equal(t, []byte{2}, dst[addr].CodeChanges[0])
		require.Equal(t, BytesToHash([]byte{2}), dst[addr].StorageChanges[Hash{}][0])
	})

	t.Run("merge when there are a new account in other (src)", func(t *testing.T) {
		dst := NewBlockAccessRuntime()
		src := NewBlockAccessRuntime()

		addr1 := BytesToAddress([]byte{1})
		addr2 := BytesToAddress([]byte{2})

		dst[addr1] = NewAccountAccessRuntime()
		dst[addr1].BalanceChanges[0] = big.NewInt(100)

		src[addr2] = NewAccountAccessRuntime()
		src[addr2].BalanceChanges[0] = big.NewInt(200)

		dst.Merge(src)

		require.Equal(t, big.NewInt(100), dst[addr1].BalanceChanges[0])
		require.Equal(t, big.NewInt(200), dst[addr2].BalanceChanges[0])
	})
}

func Test_BlockAccessRuntime_Pack(t *testing.T) {
	// TODO: check this
	t.Run("empty runtime", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		record := r.Pack()

		require.Empty(t, record)
	})

	t.Run("single account single change per type", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}
		slot := Hash{1}

		r.GetOrCreate(addr).RecordBalanceChange(0, big.NewInt(100))
		r.GetOrCreate(addr).RecordNonceChange(0, 5)
		r.GetOrCreate(addr).RecordCodeChange(0, []byte{1, 2, 3})
		r.GetOrCreate(addr).RecordStorageChange(0, slot, Hash{9})

		record := r.Pack()

		require.Len(t, record, 1)
		require.Equal(t, addr, record[0].Address)

		require.Len(t, record[0].BalanceChanges, 1)
		require.Equal(t, uint64(0), record[0].BalanceChanges[0].TxIndex)
		require.Equal(t, big.NewInt(100), record[0].BalanceChanges[0].Balance)

		require.Len(t, record[0].NonceChanges, 1)
		require.Equal(t, uint64(0), record[0].NonceChanges[0].TxIndex)
		require.Equal(t, uint64(5), record[0].NonceChanges[0].Nonce)

		require.Len(t, record[0].CodeChanges, 1)
		require.Equal(t, uint64(0), record[0].CodeChanges[0].TxIndex)
		require.Equal(t, []byte{1, 2, 3}, record[0].CodeChanges[0].Code)

		require.Len(t, record[0].StorageChanges, 1)
		require.Equal(t, slot, record[0].StorageChanges[0].Slot)
		require.Len(t, record[0].StorageChanges[0].SlotChanges, 1)
		require.Equal(t, uint64(0), record[0].StorageChanges[0].SlotChanges[0].TxIndex)
		require.Equal(t, Hash{9}, record[0].StorageChanges[0].SlotChanges[0].Value)
	})

	t.Run("account with no changes", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}

		r[addr] = NewAccountAccessRuntime()

		record := r.Pack()

		require.Len(t, record, 1)
		require.Equal(t, addr, record[0].Address)
		require.Empty(t, record[0].BalanceChanges)
		require.Empty(t, record[0].NonceChanges)
		require.Empty(t, record[0].CodeChanges)
		require.Empty(t, record[0].StorageChanges)
	})

	t.Run("accounts sorted lexicographically by address", func(t *testing.T) {
		r := NewBlockAccessRuntime()

		addr1 := Address{3}
		addr2 := Address{1}
		addr3 := Address{2}

		r.GetOrCreate(addr1).RecordBalanceChange(0, big.NewInt(100))
		r.GetOrCreate(addr2).RecordBalanceChange(0, big.NewInt(200))
		r.GetOrCreate(addr3).RecordBalanceChange(0, big.NewInt(300))

		record := r.Pack()

		require.Len(t, record, 3)
		require.Equal(t, addr2, record[0].Address)
		require.Equal(t, addr3, record[1].Address)
		require.Equal(t, addr1, record[2].Address)
	})

	t.Run("balance changes sorted by tx index with correct values", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}

		r.GetOrCreate(addr).RecordBalanceChange(2, big.NewInt(300))
		r.GetOrCreate(addr).RecordBalanceChange(0, big.NewInt(100))
		r.GetOrCreate(addr).RecordBalanceChange(1, big.NewInt(200))

		record := r.Pack()

		require.Len(t, record[0].BalanceChanges, 3)
		require.Equal(t, uint64(0), record[0].BalanceChanges[0].TxIndex)
		require.Equal(t, big.NewInt(100), record[0].BalanceChanges[0].Balance)
		require.Equal(t, uint64(1), record[0].BalanceChanges[1].TxIndex)
		require.Equal(t, big.NewInt(200), record[0].BalanceChanges[1].Balance)
		require.Equal(t, uint64(2), record[0].BalanceChanges[2].TxIndex)
		require.Equal(t, big.NewInt(300), record[0].BalanceChanges[2].Balance)
	})

	t.Run("nonce changes sorted by tx index with correct values", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}

		r.GetOrCreate(addr).RecordNonceChange(2, 3)
		r.GetOrCreate(addr).RecordNonceChange(0, 1)
		r.GetOrCreate(addr).RecordNonceChange(1, 2)

		record := r.Pack()

		require.Len(t, record[0].NonceChanges, 3)
		require.Equal(t, uint64(0), record[0].NonceChanges[0].TxIndex)
		require.Equal(t, uint64(1), record[0].NonceChanges[0].Nonce)
		require.Equal(t, uint64(1), record[0].NonceChanges[1].TxIndex)
		require.Equal(t, uint64(2), record[0].NonceChanges[1].Nonce)
		require.Equal(t, uint64(2), record[0].NonceChanges[2].TxIndex)
		require.Equal(t, uint64(3), record[0].NonceChanges[2].Nonce)
	})

	t.Run("code changes sorted by tx index with correct values", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}

		r.GetOrCreate(addr).RecordCodeChange(2, []byte{3})
		r.GetOrCreate(addr).RecordCodeChange(0, []byte{1})
		r.GetOrCreate(addr).RecordCodeChange(1, []byte{2})

		record := r.Pack()

		require.Len(t, record[0].CodeChanges, 3)
		require.Equal(t, uint64(0), record[0].CodeChanges[0].TxIndex)
		require.Equal(t, []byte{1}, record[0].CodeChanges[0].Code)
		require.Equal(t, uint64(1), record[0].CodeChanges[1].TxIndex)
		require.Equal(t, []byte{2}, record[0].CodeChanges[1].Code)
		require.Equal(t, uint64(2), record[0].CodeChanges[2].TxIndex)
		require.Equal(t, []byte{3}, record[0].CodeChanges[2].Code)
	})

	t.Run("storage slots sorted lexicographically with correct values", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}

		slot1 := Hash{3}
		slot2 := Hash{1}
		slot3 := Hash{2}

		r.GetOrCreate(addr).RecordStorageChange(0, slot1, Hash{10})
		r.GetOrCreate(addr).RecordStorageChange(0, slot2, Hash{20})
		r.GetOrCreate(addr).RecordStorageChange(0, slot3, Hash{30})

		record := r.Pack()

		require.Len(t, record[0].StorageChanges, 3)
		require.Equal(t, slot2, record[0].StorageChanges[0].Slot)
		require.Equal(t, Hash{20}, record[0].StorageChanges[0].SlotChanges[0].Value)
		require.Equal(t, slot3, record[0].StorageChanges[1].Slot)
		require.Equal(t, Hash{30}, record[0].StorageChanges[1].SlotChanges[0].Value)
		require.Equal(t, slot1, record[0].StorageChanges[2].Slot)
		require.Equal(t, Hash{10}, record[0].StorageChanges[2].SlotChanges[0].Value)
	})

	t.Run("storage slot changes sorted by tx index with correct values", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}
		slot := Hash{1}

		r.GetOrCreate(addr).RecordStorageChange(2, slot, Hash{3})
		r.GetOrCreate(addr).RecordStorageChange(0, slot, Hash{1})
		r.GetOrCreate(addr).RecordStorageChange(1, slot, Hash{2})

		record := r.Pack()

		require.Len(t, record[0].StorageChanges[0].SlotChanges, 3)
		require.Equal(t, uint64(0), record[0].StorageChanges[0].SlotChanges[0].TxIndex)
		require.Equal(t, Hash{1}, record[0].StorageChanges[0].SlotChanges[0].Value)
		require.Equal(t, uint64(1), record[0].StorageChanges[0].SlotChanges[1].TxIndex)
		require.Equal(t, Hash{2}, record[0].StorageChanges[0].SlotChanges[1].Value)
		require.Equal(t, uint64(2), record[0].StorageChanges[0].SlotChanges[2].TxIndex)
		require.Equal(t, Hash{3}, record[0].StorageChanges[0].SlotChanges[2].Value)
	})

	t.Run("multiple slots with multiple changes", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}

		slot1 := Hash{1}
		slot2 := Hash{2}

		r.GetOrCreate(addr).RecordStorageChange(0, slot1, Hash{10})
		r.GetOrCreate(addr).RecordStorageChange(1, slot1, Hash{11})
		r.GetOrCreate(addr).RecordStorageChange(0, slot2, Hash{20})
		r.GetOrCreate(addr).RecordStorageChange(1, slot2, Hash{21})

		record := r.Pack()

		require.Len(t, record[0].StorageChanges, 2)

		require.Equal(t, slot1, record[0].StorageChanges[0].Slot)
		require.Len(t, record[0].StorageChanges[0].SlotChanges, 2)
		require.Equal(t, uint64(0), record[0].StorageChanges[0].SlotChanges[0].TxIndex)
		require.Equal(t, Hash{10}, record[0].StorageChanges[0].SlotChanges[0].Value)
		require.Equal(t, uint64(1), record[0].StorageChanges[0].SlotChanges[1].TxIndex)
		require.Equal(t, Hash{11}, record[0].StorageChanges[0].SlotChanges[1].Value)

		require.Equal(t, slot2, record[0].StorageChanges[1].Slot)
		require.Len(t, record[0].StorageChanges[1].SlotChanges, 2)
		require.Equal(t, uint64(0), record[0].StorageChanges[1].SlotChanges[0].TxIndex)
		require.Equal(t, Hash{20}, record[0].StorageChanges[1].SlotChanges[0].Value)
		require.Equal(t, uint64(1), record[0].StorageChanges[1].SlotChanges[1].TxIndex)
		require.Equal(t, Hash{21}, record[0].StorageChanges[1].SlotChanges[1].Value)
	})

	t.Run("balance value is copied", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}

		bal := big.NewInt(100)
		r.GetOrCreate(addr).RecordBalanceChange(0, bal)

		record := r.Pack()

		bal.SetInt64(999)
		require.Equal(t, big.NewInt(100), record[0].BalanceChanges[0].Balance)
	})

	t.Run("code is cloned", func(t *testing.T) {
		r := NewBlockAccessRuntime()
		addr := Address{1}

		code := []byte{1, 2, 3}
		r.GetOrCreate(addr).RecordCodeChange(0, code)

		record := r.Pack()

		code[0] = 9
		require.Equal(t, []byte{1, 2, 3}, record[0].CodeChanges[0].Code)
	})

	t.Run("multiple accounts with different changes", func(t *testing.T) {
		r := NewBlockAccessRuntime()

		addr1 := Address{1}
		addr2 := Address{2}

		r.GetOrCreate(addr1).RecordBalanceChange(0, big.NewInt(100))
		r.GetOrCreate(addr1).RecordNonceChange(0, 1)
		r.GetOrCreate(addr2).RecordBalanceChange(0, big.NewInt(200))
		r.GetOrCreate(addr2).RecordCodeChange(0, []byte{1, 2, 3})

		record := r.Pack()

		require.Len(t, record, 2)

		require.Equal(t, addr1, record[0].Address)
		require.Len(t, record[0].BalanceChanges, 1)
		require.Equal(t, big.NewInt(100), record[0].BalanceChanges[0].Balance)
		require.Len(t, record[0].NonceChanges, 1)
		require.Equal(t, uint64(1), record[0].NonceChanges[0].Nonce)
		require.Empty(t, record[0].CodeChanges)
		require.Empty(t, record[0].StorageChanges)

		require.Equal(t, addr2, record[1].Address)
		require.Len(t, record[1].BalanceChanges, 1)
		require.Equal(t, big.NewInt(200), record[1].BalanceChanges[0].Balance)
		require.Len(t, record[1].CodeChanges, 1)
		require.Equal(t, []byte{1, 2, 3}, record[1].CodeChanges[0].Code)
		require.Empty(t, record[1].NonceChanges)
		require.Empty(t, record[1].StorageChanges)
	})
}
