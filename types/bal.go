package types

import (
	"bytes"
	"cmp"
	"maps"
	"math/big"
	"slices"

	"golang.org/x/crypto/sha3"
)

// AccountAccessRuntime contains per-transaction state changes for an account. It is populated
// in runtime, that is, during block execution. For each transaction (indexed by its position
// in the block), only the last change is retained. So, intermediate changes within the same
// transaction are overwritten.
type AccountAccessRuntime struct {
	// StorageChanges contains all transactions that modified the account storage, mapped as:
	//
	// slot number -> tx index -> slot value after the tx.
	StorageChanges map[Hash]map[uint64]Hash
	// BalanceChanges contains all transactions that modified the account balance, mapped as:
	//
	// tx index -> balance after the tx
	BalanceChanges map[uint64]*big.Int
	// NonceChanges contains all transactions that modified the account nonce, mapped as:
	//
	// tx index -> nonce after the tx
	NonceChanges map[uint64]uint64
	// CodeChanges contains all transactions that modified the account code, mapped as:
	//
	// tx index -> code after the tx
	CodeChanges map[uint64][]byte
}

// NewAccountAccessRuntime returns a new empty [AccountAccessRuntime].
func NewAccountAccessRuntime() *AccountAccessRuntime {
	return &AccountAccessRuntime{
		StorageChanges: make(map[Hash]map[uint64]Hash),
		BalanceChanges: make(map[uint64]*big.Int),
		NonceChanges:   make(map[uint64]uint64),
		CodeChanges:    make(map[uint64][]byte),
	}
}

// BlockAccessRuntime holds state changes for all accounts modified during block executin.
type BlockAccessRuntime map[Address]*AccountAccessRuntime

// NewBlockAccessRuntime returns a new empty [BlockAccessRuntime].
func NewBlockAccessRuntime() BlockAccessRuntime {
	return make(map[Address]*AccountAccessRuntime)
}

// GetOrCreate returns the [AccountAccessRuntime] for the given address, creating one if it
// does not exist.
func (r BlockAccessRuntime) GetOrCreate(addr Address) *AccountAccessRuntime {
	acc, ok := r[addr]
	if !ok {
		acc = NewAccountAccessRuntime()
		r[addr] = acc
	}

	return acc
}

// RecordStorageChange records a value change for the given slot and transaction.
func (r *AccountAccessRuntime) RecordStorageChange(id uint64, slot, value Hash) {
	slotWrites, ok := r.StorageChanges[slot]
	if !ok {
		slotWrites = make(map[uint64]Hash)
		r.StorageChanges[slot] = slotWrites
	}

	slotWrites[id] = value
}

// RecordBalanceChange records a balance change for the given transaction.
func (r *AccountAccessRuntime) RecordBalanceChange(id uint64, balance *big.Int) {
	r.BalanceChanges[id] = new(big.Int).Set(balance)
}

// RecordNonceChange records a nonce change for the given transaction.
func (r *AccountAccessRuntime) RecordNonceChange(id uint64, nonce uint64) {
	r.NonceChanges[id] = nonce
}

// RecordCodeChange records a code change for the given transaction.
func (r *AccountAccessRuntime) RecordCodeChange(id uint64, code []byte) {
	r.CodeChanges[id] = bytes.Clone(code)
}

// Merge merges other into r. In case of a merge conflict (a key collision in any of the maps),
// other takes precedence over r.
func (r BlockAccessRuntime) Merge(other BlockAccessRuntime) {
	if other == nil {
		return
	}

	for addr, otherAcc := range other {
		acc, ok := r[addr]
		if !ok {
			r[addr] = otherAcc

			continue
		}

		for slot, changes := range otherAcc.StorageChanges {
			dst, ok := acc.StorageChanges[slot]
			if !ok {
				acc.StorageChanges[slot] = changes

				continue
			}

			maps.Copy(dst, changes)
		}

		maps.Copy(acc.BalanceChanges, otherAcc.BalanceChanges)
		maps.Copy(acc.NonceChanges, otherAcc.NonceChanges)
		maps.Copy(acc.CodeChanges, otherAcc.CodeChanges)
	}
}

type accountAccessRecord struct {
	Address        Address
	StorageChanges []storageChange
	BalanceChanges []balanceChange
	NonceChanges   []nonceChange
	CodeChanges    []codeChange
}

type slotChange struct {
	TxIndex uint64
	Value   Hash
}

type storageChange struct {
	Slot        Hash
	SlotChanges []slotChange
}

type balanceChange struct {
	TxIndex uint64
	Balance *big.Int
}

type nonceChange struct {
	TxIndex uint64
	Nonce   uint64
}

type codeChange struct {
	TxIndex uint64
	Code    []byte
}

// BlockAccessRecord is the packed representation of a [BlockAccessRuntime] (for more information
// on packing, see [BlockAccessRuntime.Pack]).
type BlockAccessRecord []accountAccessRecord

// Pack packs r from its runtime representation into a sorted [BlockAccessRecord]. The following
// rules apply:
//  1. accounts are ordered lexicographically by address,
//  2. storage slots lexicographically by slot hash, and
//  3. all changes ascending by tx index.
func (r BlockAccessRuntime) Pack() BlockAccessRecord {
	addresses := slices.Collect(maps.Keys(r))
	slices.SortFunc(addresses, func(x, y Address) int {
		return bytes.Compare(x[:], y[:])
	})

	res := make(BlockAccessRecord, 0, len(addresses))
	for _, addr := range addresses {
		res = append(res, r[addr].pack(addr))
	}

	return res
}

func (r *AccountAccessRuntime) pack(addr Address) accountAccessRecord {
	record := accountAccessRecord{
		Address:        addr,
		StorageChanges: make([]storageChange, 0, len(r.StorageChanges)),
		BalanceChanges: make([]balanceChange, 0, len(r.BalanceChanges)),
		NonceChanges:   make([]nonceChange, 0, len(r.NonceChanges)),
		CodeChanges:    make([]codeChange, 0, len(r.CodeChanges)),
	}

	sortedSlots := slices.Collect(maps.Keys(r.StorageChanges))
	slices.SortFunc(sortedSlots, func(x, y Hash) int {
		return bytes.Compare(x[:], y[:])
	})

	for _, slot := range sortedSlots {
		perSlotChanges := r.StorageChanges[slot]

		slotChanges := make([]slotChange, 0, len(perSlotChanges))
		for _, id := range sortedKeys(perSlotChanges) {
			slotChanges = append(slotChanges, slotChange{
				TxIndex: id,
				Value:   perSlotChanges[id],
			})
		}

		record.StorageChanges = append(record.StorageChanges, storageChange{
			Slot:        slot,
			SlotChanges: slotChanges,
		})
	}

	for _, id := range sortedKeys(r.BalanceChanges) {
		record.BalanceChanges = append(record.BalanceChanges, balanceChange{
			TxIndex: id,
			Balance: new(big.Int).Set(r.BalanceChanges[id]),
		})
	}

	for _, id := range sortedKeys(r.NonceChanges) {
		record.NonceChanges = append(record.NonceChanges, nonceChange{
			TxIndex: id,
			Nonce:   r.NonceChanges[id],
		})
	}

	for _, id := range sortedKeys(r.CodeChanges) {
		record.CodeChanges = append(record.CodeChanges, codeChange{
			TxIndex: id,
			Code:    bytes.Clone(r.CodeChanges[id]),
		})
	}

	return record
}

func sortedKeys[V any](m map[uint64]V) []uint64 {
	keys := slices.Collect(maps.Keys(m))
	slices.SortFunc(keys, cmp.Compare)

	return keys
}

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
