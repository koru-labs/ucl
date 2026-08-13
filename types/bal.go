package types

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"

	"golang.org/x/crypto/sha3"
)

// AccountAccessRecord holds all state changes for a single account during block execution.
type AccountAccessRecord struct {
	Address        Address
	StorageChanges []StorageChange
	BalanceChanges []BalanceChange
	NonceChanges   []NonceChange
	CodeChanges    []CodeChange
}

// SlotChange holds the value of a storage slot after the execution of the tx at the given index.
type SlotChange struct {
	TxIndex uint64
	Value   Hash
}

// StorageChange holds all changes for a single storage slot across multiple txs.
type StorageChange struct {
	Slot        Hash
	SlotChanges []SlotChange
}

// BalanceChange holds the account balance after the execution of the tx at the given index.
type BalanceChange struct {
	TxIndex uint64
	Balance *big.Int
}

// NonceChange holds the account nonce after the execution of the tx at the given index.
type NonceChange struct {
	TxIndex uint64
	Nonce   uint64
}

// CodeChange holds the account code after the execution of the tx at the given index.
type CodeChange struct {
	TxIndex uint64
	Code    []byte
}

// BlockAccessRecord holds all account state changes that occurred during block execution.
type BlockAccessRecord []AccountAccessRecord

// Hash returns the keccak256 hash of the RLP-encoded r.
func (r BlockAccessRecord) Hash() Hash {
	keccak256 := func(v ...[]byte) []byte {
		h := sha3.NewLegacyKeccak256()
		for _, i := range v {
			h.Write(i)
		}

		return h.Sum(nil)
	}

	return BytesToHash(keccak256(r.MarshalRLPTo(nil)))
}

// account returns a pointer to the AccountAccessRecord for addr, or nil if absent.
func (r BlockAccessRecord) account(addr Address) *AccountAccessRecord {
	for i := range r {
		if r[i].Address == addr {
			return &r[i]
		}
	}
	return nil
}

// BalanceBefore returns the account balance after the most recent tx with
// TxIndex < txIndex. Returns false if the account is absent or has no earlier change.
func (r BlockAccessRecord) BalanceBefore(addr Address, txIndex uint64) (*big.Int, bool) {
	a := r.account(addr)
	if a == nil {
		return nil, false
	}
	i := sort.Search(len(a.BalanceChanges), func(i int) bool {
		return a.BalanceChanges[i].TxIndex >= txIndex
	})
	if i == 0 {
		return nil, false
	}
	return a.BalanceChanges[i-1].Balance, true
}

// NonceBefore returns the account nonce after the most recent tx with
// TxIndex < txIndex. Returns false if the account is absent or has no earlier change.
func (r BlockAccessRecord) NonceBefore(addr Address, txIndex uint64) (uint64, bool) {
	a := r.account(addr)
	if a == nil {
		return 0, false
	}
	i := sort.Search(len(a.NonceChanges), func(i int) bool {
		return a.NonceChanges[i].TxIndex >= txIndex
	})
	if i == 0 {
		return 0, false
	}
	return a.NonceChanges[i-1].Nonce, true
}

// CodeBefore returns the account code after the most recent tx with
// TxIndex < txIndex. Returns false if the account is absent or has no earlier change.
func (r BlockAccessRecord) CodeBefore(addr Address, txIndex uint64) ([]byte, bool) {
	a := r.account(addr)
	if a == nil {
		return nil, false
	}
	i := sort.Search(len(a.CodeChanges), func(i int) bool {
		return a.CodeChanges[i].TxIndex >= txIndex
	})
	if i == 0 {
		return nil, false
	}
	return a.CodeChanges[i-1].Code, true
}

// SlotBefore returns the storage slot value after the most recent tx with
// TxIndex < txIndex. Returns false if the account, the slot, or an earlier
// change is absent.
func (r BlockAccessRecord) SlotBefore(addr Address, slot Hash, txIndex uint64) (Hash, bool) {
	a := r.account(addr)
	if a == nil {
		return Hash{}, false
	}

	n := len(a.StorageChanges)
	idx := sort.Search(n, func(i int) bool {
		return bytes.Compare(a.StorageChanges[i].Slot[:], slot[:]) >= 0
	})

	if idx == n || a.StorageChanges[idx].Slot != slot {
		return Hash{}, false
	}

	sc := &a.StorageChanges[idx]

	i := sort.Search(len(sc.SlotChanges), func(i int) bool {
		return sc.SlotChanges[i].TxIndex >= txIndex
	})
	if i == 0 {
		return Hash{}, false
	}

	return sc.SlotChanges[i-1].Value, true
}

func (r BlockAccessRecord) Validate() error {
	for i := 1; i < len(r); i++ {
		if bytes.Compare(r[i-1].Address[:], r[i].Address[:]) >= 0 {
			return fmt.Errorf("accounts not strictly sorted at %d: %s >= %s",
				i, r[i-1].Address, r[i].Address)
		}
	}

	for i := range r {
		if err := r[i].validate(); err != nil {
			return fmt.Errorf("account %s: %w", r[i].Address, err)
		}
	}

	return nil
}

func (a AccountAccessRecord) validate() error {
	for i := 1; i < len(a.StorageChanges); i++ {
		if bytes.Compare(a.StorageChanges[i-1].Slot[:], a.StorageChanges[i].Slot[:]) >= 0 {
			return fmt.Errorf("storage slots not strictly sorted at %d", i)
		}
	}

	for _, sc := range a.StorageChanges {
		for i := 1; i < len(sc.SlotChanges); i++ {
			if sc.SlotChanges[i-1].TxIndex >= sc.SlotChanges[i].TxIndex {
				return fmt.Errorf("slot %s: SlotChanges not strictly ascending at %d", sc.Slot, i)
			}
		}
	}

	for i := 1; i < len(a.BalanceChanges); i++ {
		if a.BalanceChanges[i-1].TxIndex >= a.BalanceChanges[i].TxIndex {
			return fmt.Errorf("BalanceChanges not strictly ascending at %d", i)
		}
	}

	for i := 1; i < len(a.NonceChanges); i++ {
		if a.NonceChanges[i-1].TxIndex >= a.NonceChanges[i].TxIndex {
			return fmt.Errorf("NonceChanges not strictly ascending at %d", i)
		}
	}

	for i := 1; i < len(a.CodeChanges); i++ {
		if a.CodeChanges[i-1].TxIndex >= a.CodeChanges[i].TxIndex {
			return fmt.Errorf("CodeChanges not strictly ascending at %d", i)
		}
	}

	return nil
}
