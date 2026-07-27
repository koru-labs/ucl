package types

import (
	"math/big"

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
