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
func (r BlockAccessRecord) Insert(recorder *TxAccessRecorder) {
	if recorder == nil {
		return
	}

	txIndex := recorder.txIndex

	for addr, record := range recorder.current {
		acc := r.GetOrCreate(addr)

		if record.Balance != nil {
			acc.RecordBalanceChange(txIndex, record.Balance)
		}

		if record.Nonce != nil {
			acc.RecordNonceChange(txIndex, *record.Nonce)
		}

		if record.Code != nil {
			acc.RecordCodeChange(txIndex, record.Code)
		}

		// nil storage safely iterates 0 times; EIP-158 emptiness clearing
		// is applied at Commit time on the receiving side, not encoded here.
		for slot, value := range record.Storage {
			acc.RecordStorageChange(txIndex, slot, value)
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

	// nil storage safely iterates 0 times; EIP-158 emptiness clearing
	// is applied at Commit time on the receiving side, not encoded here.
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

// // TxAccessRecorder records state changes for a single transaction. For each state field, only
// // the last recorded change is retained. It supports nested calls through a snapshot mechanism.
// // See [TxAccessRecorder.Snapshot], [TxAccessRecorder.Commit], and [TxAccessRecorder.Revert].
// type TxAccessRecorder interface {
// 	// RecordStorageChange records a storage slot value change for the given account.
// 	RecordStorageChange(addr types.Address, slot types.Hash, value types.Hash)
// 	// RecordBalanceChange records a balance change for the given account.
// 	RecordBalanceChange(addr types.Address, balance *big.Int)
// 	// RecordNonceChange records a nonce change for the given account.
// 	RecordNonceChange(addr types.Address, nonce uint64)
// 	// RecordCodeChange records a code change for the given account.
// 	RecordCodeChange(addr types.Address, code []byte)
// 	// GetStorage returns the current value of the given storage slot if it was modified in the
// 	// current transaction. Returns false if the slot was not modified.
// 	GetStorage(addr types.Address, slot types.Hash) (types.Hash, bool)
// 	// GetBalance returns the current balance of the given account if it was modified in the
// 	// current transaction. Returns false if the balance was not modified.
// 	GetBalance(addr types.Address) (*big.Int, bool)
// 	// GetNonce returns the current nonce of the given account if it was modified in the current
// 	// transaction. Returns false if the nonce was not modified.
// 	GetNonce(addr types.Address) (uint64, bool)
// 	// GetCode returns the current code of the given account if it was modified in the current
// 	// transaction. Returns false if the code was not modified.
// 	GetCode(addr types.Address) ([]byte, bool)
// 	// Snapshot marks the current state as a restore point. All changes made after this point
// 	// can be accepted by [TxAccessRecorder.Commit] or undone by [TxAccessRecorder.Revert].
// 	// Snapshots can be nested - each Revert or Commit only affects the last snapshot.
// 	Snapshot()
// 	// Revert undoes all changes made since the last snapshot.
// 	Revert()
// 	// Commit accepts all changes made since the last snapshot.
// 	Commit()
// }

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

type TxAccessRecorder struct {
	current   map[types.Address]*txAccountAccessRecord
	journal   []journalEntry
	snapshots []int
	txIndex   uint64
}

// NewAccountAccessRecord returns a new empty [txAccessRecorder].
func NewTxAccessRecorder(txIndex uint64) *TxAccessRecorder {
	return &TxAccessRecorder{
		current: make(map[types.Address]*txAccountAccessRecord),
		txIndex: txIndex,
	}
}

// Snapshot marks the current state as a restore point. All changes made after this point can
// be accepted by [txAccessRecorder.Commit] or undone by [txAccessRecorder.Revert]. Snapshots
// can be nested - each Revert or Commit only affects the last snapshot.
func (r *TxAccessRecorder) Snapshot() {
	if r == nil {
		return
	}

	r.snapshots = append(r.snapshots, len(r.journal))
}

// Commit accepts all changes made since the last snapshot.
func (r *TxAccessRecorder) Commit() {
	if r == nil || len(r.snapshots) == 0 {
		return
	}

	r.snapshots = r.snapshots[:len(r.snapshots)-1]
}

// Revert undoes all changes made since the last snapshot.
func (r *TxAccessRecorder) Revert() {
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

func (r *TxAccessRecorder) getOrCreate(addr types.Address) *txAccountAccessRecord {
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
func (r *TxAccessRecorder) RecordStorageChange(
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
func (r *TxAccessRecorder) RecordBalanceChange(addr types.Address, balance *big.Int) {
	if r == nil {
		return
	}

	acc := r.getOrCreate(addr)
	r.journal = append(r.journal, journalEntry{addr: addr, prev: acc.Balance})
	acc.Balance = new(big.Int).Set(balance)
}

// RecordNonceChange records a nonce change for the given account.
func (r *TxAccessRecorder) RecordNonceChange(addr types.Address, nonce uint64) {
	if r == nil {
		return
	}

	acc := r.getOrCreate(addr)
	r.journal = append(r.journal, journalEntry{addr: addr, prev: acc.Nonce})
	acc.Nonce = &nonce
}

// RecordCodeChange records a code change for the given account.
func (r *TxAccessRecorder) RecordCodeChange(addr types.Address, code []byte) {
	if r == nil {
		return
	}

	acc := r.getOrCreate(addr)
	r.journal = append(r.journal, journalEntry{addr: addr, prev: acc.Code})
	acc.Code = bytes.Clone(code)
}

// GetStorage returns the current value of the given storage slot if it was modified in the
// current transaction. Returns false if the slot was not modified.
func (r *TxAccessRecorder) GetStorage(addr types.Address, slot types.Hash) (types.Hash, bool) {
	if r == nil {
		return types.Hash{}, false
	}

	acc, ok := r.current[addr]
	if !ok {
		return types.Hash{}, false
	}

	val, ok := acc.Storage[slot]

	return val, ok
}

// GetBalance returns the current balance of the given account if it was modified in the current
// transaction. Returns false if the balance was not modified.
func (r *TxAccessRecorder) GetBalance(addr types.Address) (*big.Int, bool) {
	if r == nil {
		return nil, false
	}

	acc, ok := r.current[addr]
	if !ok {
		return nil, false
	}

	return acc.Balance, acc.Balance != nil
}

// GetNonce returns the current nonce of the given account if it was modified in the current
// transaction. Returns false if the nonce was not modified.
func (r *TxAccessRecorder) GetNonce(addr types.Address) (uint64, bool) {
	if r == nil {
		return 0, false
	}

	acc, ok := r.current[addr]
	if !ok {
		return 0, false
	}

	if acc.Nonce == nil {
		return 0, false
	}

	return *acc.Nonce, true
}

// GetCode returns the current code of the given account if it was modified in the current
// transaction. Returns false if the code was not modified.
func (r *TxAccessRecorder) GetCode(addr types.Address) ([]byte, bool) {
	if r == nil {
		return nil, false
	}

	acc, ok := r.current[addr]
	if !ok {
		return nil, false
	}

	return acc.Code, acc.Code != nil
}

func (r *TxAccessRecorder) Reset(txIndex uint64) {
	r.txIndex = txIndex
	clear(r.current)              // Go 1.21+ builtin: empties map, keeps capacity
	r.journal = r.journal[:0]     // len=0, cap retained
	r.snapshots = r.snapshots[:0] // len=0, cap retained
}
