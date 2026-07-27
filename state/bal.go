package state

import (
	"bytes"
	"maps"
	"math/big"
	"slices"

	"github.com/0xPolygon/polygon-edge/types"
)

// AccountAccessRecord contains per-transaction state changes for an account, populated during
// block execution. For each transaction, only the last state change is stored - intermediate
// changes within the transaction never reach this structure.
type AccountAccessRecord struct {
	// StorageChanges contains all transactions that modified the account storage, mapped as:
	//
	// slot number -> tx index -> slot value after the tx.
	StorageChanges map[types.Hash]map[uint64]types.Hash
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

// NewAccountAccessRecord returns a new empty [AccountAccessRecord].
func NewAccountAccessRecord() *AccountAccessRecord {
	return &AccountAccessRecord{
		StorageChanges: make(map[types.Hash]map[uint64]types.Hash),
		BalanceChanges: make(map[uint64]*big.Int),
		NonceChanges:   make(map[uint64]uint64),
		CodeChanges:    make(map[uint64][]byte),
	}
}

// RecordStorageChange records a value change for the given slot and transaction.
func (r *AccountAccessRecord) RecordStorageChange(id uint64, slot, value types.Hash) {
	slotChanges, ok := r.StorageChanges[slot]
	if !ok {
		slotChanges = make(map[uint64]types.Hash)
		r.StorageChanges[slot] = slotChanges
	}

	slotChanges[id] = value
}

// RecordBalanceChange records a balance change for the given transaction.
func (r *AccountAccessRecord) RecordBalanceChange(id uint64, balance *big.Int) {
	r.BalanceChanges[id] = new(big.Int).Set(balance)
}

// RecordNonceChange records a nonce change for the given transaction.
func (r *AccountAccessRecord) RecordNonceChange(id uint64, nonce uint64) {
	r.NonceChanges[id] = nonce
}

// RecordCodeChange records a code change for the given transaction.
func (r *AccountAccessRecord) RecordCodeChange(id uint64, code []byte) {
	r.CodeChanges[id] = bytes.Clone(code)
}

// BlockAccessRecord holds state changes for all accounts modified during block executin.
type BlockAccessRecord map[types.Address]*AccountAccessRecord

// NewBlockAccessRecord returns a new empty [BlockAccessRecord].
func NewBlockAccessRecord() BlockAccessRecord {
	return make(map[types.Address]*AccountAccessRecord)
}

// GetOrCreate returns the [AccountAccessRecord] for the given address, creating one if it
// does not exist.
func (r BlockAccessRecord) GetOrCreate(addr types.Address) *AccountAccessRecord {
	acc, ok := r[addr]
	if !ok {
		acc = NewAccountAccessRecord()
		r[addr] = acc
	}

	return acc
}

// Insert inserts all state changes recorded for the given transaction into r.
func (r BlockAccessRecord) Insert(recorder *txAccessRecorder) {
	if recorder == nil {
		return
	}

	for addr, record := range recorder.current {
		acc := r.GetOrCreate(addr)

		if record.Balance != nil {
			acc.RecordBalanceChange(recorder.txIndex, record.Balance)
		}

		if record.Nonce != nil {
			acc.RecordNonceChange(recorder.txIndex, *record.Nonce)
		}

		if record.Code != nil {
			acc.RecordCodeChange(recorder.txIndex, record.Code)
		}

		for slot, value := range record.Storage {
			acc.RecordStorageChange(recorder.txIndex, slot, value)
		}
	}
}

// Pack packs r from its runtime representation into a sorted [types.BlockAccessRecord] ready
// for block inclusion. The following rules apply:
//  1. accounts are ordered lexicographically by address,
//  2. storage slots lexicographically by slot hash, and
//  3. all changes ascending by tx index.
func (r BlockAccessRecord) Pack() types.BlockAccessRecord {
	addresses := slices.Collect(maps.Keys(r))
	slices.SortFunc(addresses, func(x, y types.Address) int {
		return bytes.Compare(x[:], y[:])
	})

	res := make(types.BlockAccessRecord, 0, len(addresses))
	for _, addr := range addresses {
		res = append(res, pack(r[addr], addr))
	}

	return res
}

func pack(r *AccountAccessRecord, addr types.Address) types.AccountAccessRecord {
	record := types.AccountAccessRecord{
		Address:        addr,
		StorageChanges: make([]types.StorageChange, 0, len(r.StorageChanges)),
		BalanceChanges: make([]types.BalanceChange, 0, len(r.BalanceChanges)),
		NonceChanges:   make([]types.NonceChange, 0, len(r.NonceChanges)),
		CodeChanges:    make([]types.CodeChange, 0, len(r.CodeChanges)),
	}

	sortedSlots := slices.Collect(maps.Keys(r.StorageChanges))
	slices.SortFunc(sortedSlots, func(x, y types.Hash) int {
		return bytes.Compare(x[:], y[:])
	})

	for _, slot := range sortedSlots {
		perSlotChanges := r.StorageChanges[slot]

		slotChanges := make([]types.SlotChange, 0, len(perSlotChanges))
		for _, id := range sortedKeys(perSlotChanges) {
			slotChanges = append(slotChanges, types.SlotChange{
				TxIndex: id,
				Value:   perSlotChanges[id],
			})
		}

		record.StorageChanges = append(record.StorageChanges, types.StorageChange{
			Slot:        slot,
			SlotChanges: slotChanges,
		})
	}

	for _, id := range sortedKeys(r.BalanceChanges) {
		record.BalanceChanges = append(record.BalanceChanges, types.BalanceChange{
			TxIndex: id,
			Balance: new(big.Int).Set(r.BalanceChanges[id]),
		})
	}

	for _, id := range sortedKeys(r.NonceChanges) {
		record.NonceChanges = append(record.NonceChanges, types.NonceChange{
			TxIndex: id,
			Nonce:   r.NonceChanges[id],
		})
	}

	for _, id := range sortedKeys(r.CodeChanges) {
		record.CodeChanges = append(record.CodeChanges, types.CodeChange{
			TxIndex: id,
			Code:    bytes.Clone(r.CodeChanges[id]),
		})
	}

	return record
}

func sortedKeys[V any](m map[uint64]V) []uint64 {
	keys := slices.Collect(maps.Keys(m))
	slices.Sort(keys)

	return keys
}

// TxAccessRecorder records state changes for a single transaction. For each state field, only
// the last recorded change is retained. It supports nested calls through a snapshot mechanism.
// See [TxAccessRecorder.Snapshot], [TxAccessRecorder.Commit], and [TxAccessRecorder.Revert].
type TxAccessRecorder interface {
	// RecordStorageChange records a storage slot value change for the given account.
	RecordStorageChange(addr types.Address, slot types.Hash, value types.Hash)
	// RecordBalanceChange records a balance change for the given account.
	RecordBalanceChange(addr types.Address, balance *big.Int)
	// RecordNonceChange records a nonce change for the given account.
	RecordNonceChange(addr types.Address, nonce uint64)
	// RecordCodeChange records a code change for the given account.
	RecordCodeChange(addr types.Address, code []byte)
	// Snapshot marks the current state as a restore point. All changes made after this point
	// can be accepted by [TxAccessRecorder.Commit] or undone by [TxAccessRecorder.Revert].
	// Snapshots can be nested - each Revert or Commit only affects the last snapshot.
	Snapshot()
	// Revert undoes all changes made since the last snapshot.
	Revert()
	// Commit accepts all changes made since the last snapshot.
	Commit()
}

type txAccountAccessRecord struct {
	Storage map[types.Hash]types.Hash
	Balance *big.Int
	Nonce   *uint64
	Code    []byte
}

type journalEntry struct {
	addr types.Address
	prev any
}

type txAccessRecorder struct {
	txIndex   uint64
	current   map[types.Address]*txAccountAccessRecord
	journal   []journalEntry
	snapshots []int
}

// NewAccountAccessRecord returns a new empty [txAccessRecorder].
func NewTxAccessRecorder(txIndex uint64) *txAccessRecorder {
	return &txAccessRecorder{
		txIndex: txIndex,
		current: make(map[types.Address]*txAccountAccessRecord),
	}
}

// Snapshot marks the current state as a restore point. All changes made after this point can
// be accepted by [txAccessRecorder.Commit] or undone by [txAccessRecorder.Revert]. Snapshots
// can be nested - each Revert or Commit only affects the last snapshot.
func (r *txAccessRecorder) Snapshot() {
	if r == nil {
		return
	}

	r.snapshots = append(r.snapshots, len(r.journal))
}

// Commit accepts all changes made since the last snapshot.
func (r *txAccessRecorder) Commit() {
	if r == nil || len(r.snapshots) == 0 {
		return
	}

	snapshotId := r.snapshots[len(r.snapshots)-1]
	r.snapshots = r.snapshots[:len(r.snapshots)-1]
	r.journal = r.journal[:snapshotId]
}

// Revert undoes all changes made since the last snapshot.
func (r *txAccessRecorder) Revert() {
	if r == nil || len(r.snapshots) == 0 {
		return
	}

	snapshotId := r.snapshots[len(r.snapshots)-1]
	r.snapshots = r.snapshots[:len(r.snapshots)-1]

	for i := len(r.journal) - 1; i >= snapshotId; i-- {
		entry := r.journal[i]
		acc := r.current[entry.addr]

		switch prev := entry.prev.(type) {
		case *big.Int:
			acc.Balance = prev
		case *uint64:
			acc.Nonce = prev
		case []byte:
			acc.Code = prev
		case struct {
			slot types.Hash
			prev *types.Hash
		}:
			if prev.prev == nil {
				delete(acc.Storage, prev.slot)
			} else {
				acc.Storage[prev.slot] = *prev.prev
			}
		default:
			delete(r.current, entry.addr)
		}
	}

	r.journal = r.journal[:snapshotId]
}

func (r *txAccessRecorder) getOrCreate(addr types.Address) *txAccountAccessRecord {
	acc, ok := r.current[addr]
	if !ok {
		acc = &txAccountAccessRecord{
			Storage: make(map[types.Hash]types.Hash),
		}

		r.current[addr] = acc
		r.journal = append(r.journal, journalEntry{addr: addr, prev: nil})
	}

	return acc
}

// RecordStorageChange records a storage slot value change for the given account.
func (r *txAccessRecorder) RecordStorageChange(
	addr types.Address,
	slot types.Hash,
	value types.Hash) {
	if r == nil {
		return
	}

	acc := r.getOrCreate(addr)
	prev, exists := acc.Storage[slot]

	entry := struct {
		slot types.Hash
		prev *types.Hash
	}{slot: slot}

	if exists {
		entry.prev = &prev
	}

	r.journal = append(r.journal, journalEntry{addr: addr, prev: entry})
	acc.Storage[slot] = value
}

// RecordBalanceChange records a balance change for the given account.
func (r *txAccessRecorder) RecordBalanceChange(addr types.Address, balance *big.Int) {
	if r == nil {
		return
	}

	acc := r.getOrCreate(addr)
	r.journal = append(r.journal, journalEntry{addr: addr, prev: acc.Balance})
	acc.Balance = new(big.Int).Set(balance)
}

// RecordNonceChange records a nonce change for the given account.
func (r *txAccessRecorder) RecordNonceChange(addr types.Address, nonce uint64) {
	if r == nil {
		return
	}

	acc := r.getOrCreate(addr)
	r.journal = append(r.journal, journalEntry{addr: addr, prev: acc.Nonce})
	acc.Nonce = &nonce
}

// RecordCodeChange records a code change for the given account.
func (r *txAccessRecorder) RecordCodeChange(addr types.Address, code []byte) {
	if r == nil {
		return
	}

	acc := r.getOrCreate(addr)
	r.journal = append(r.journal, journalEntry{addr: addr, prev: acc.Code})
	acc.Code = bytes.Clone(code)
}
