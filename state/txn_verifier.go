package state

import (
	"fmt"
	"math/big"
	"sync"
	"time"

	iradix "github.com/hashicorp/go-immutable-radix"
	"github.com/hashicorp/go-metrics"
	lru "github.com/hashicorp/golang-lru"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/blockstm"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
)

type txLocalValue struct {
	isWritten bool
	value     any
}

// journalEntry records what a txLocalMap key held (if anything) right before it was
// overwritten or deleted, so RevertToSnapshot can restore it.
type journalEntry struct {
	key      Key
	existed  bool
	oldValue txLocalValue
}

// txnSnapshot is what Snapshot() captures: how far back the journal needs to be unwound,
// plus the logs/refund state to restore directly (see the txnLogs/txnRefund comment below
// for why those two don't need full journaling).
type txnSnapshot struct {
	journalLen int
	logsLen    int
	refund     uint64
}

// Txn is a reference of the state
type TxnVerifier struct {
	snapshot readSnapshot

	codeCache *lru.Cache

	globalMutex   *sync.RWMutex
	globalRadix   *iradix.Txn
	txLocalMap    map[Key]txLocalValue
	txLocalLogs   []*types.Log
	txLocalRefund uint64

	// journal + snapshots implement Snapshot()/RevertToSnapshot(): EVM calls that fail (a
	// failed CALL/CREATE, a top-level revert - a completely normal, common outcome, not just
	// malformed transactions) must undo only the state they touched, not everything staged so
	// far in the transaction. txLocalMap entries are journaled key-by-key since any key can be
	// written many times; txLocalLogs/txLocalRefund don't need that - logs are append-only, so
	// remembering the previous length and truncating back to it on revert is enough, and the
	// refund counter is a single value, so remembering the previous value and restoring it
	// directly is enough. Both are cheaper than journaling every individual append/delta.
	journal   []journalEntry
	snapshots []txnSnapshot
}

func createBlockRadix() *iradix.Txn {
	return iradix.New().Txn()
}

func NewTxnVerifier(
	snapshot readSnapshot,
	blockRadix *iradix.Txn,
	blockMutex *sync.RWMutex,
) *TxnVerifier {
	codeCache, _ := lru.New(20)

	return &TxnVerifier{
		snapshot:    snapshot,
		globalRadix: blockRadix,
		txLocalMap:  map[Key]txLocalValue{},
		codeCache:   codeCache,
		globalMutex: blockMutex,
	}
}

func (txn *TxnVerifier) ClearAccessTracker(writesOnly bool) {
}

// setLocal writes key into txLocalMap, journaling whatever was there before (or that nothing
// was) so a later RevertToSnapshot can restore it.
func (txn *TxnVerifier) setLocal(key Key, val txLocalValue) {
	old, existed := txn.txLocalMap[key]
	txn.journal = append(txn.journal, journalEntry{key: key, existed: existed, oldValue: old})
	txn.txLocalMap[key] = val
}

// deleteLocal removes key from txLocalMap, journaling the prior value so a later
// RevertToSnapshot can restore it.
func (txn *TxnVerifier) deleteLocal(key Key) {
	old, existed := txn.txLocalMap[key]
	if !existed {
		return
	}

	txn.journal = append(txn.journal, journalEntry{key: key, existed: true, oldValue: old})
	delete(txn.txLocalMap, key)
}

func (txn *TxnVerifier) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	return blockstm.TxReadWriteSet{}
}

// SetTransientState writes a value into transient storage for the given address and slot.
func (txn *TxnVerifier) SetTransientState(addr types.Address, slot types.Hash, value types.Hash) {
	key := NewTransientStateKey(addr, slot)

	if (value == types.Hash{}) {
		txn.deleteLocal(key)
	} else {
		txn.setLocal(key, txLocalValue{
			isWritten: true,
			value:     value.Bytes(),
		})
	}
}

// GetTransientState reads a value from transient storage for the given address and slot.
func (txn *TxnVerifier) GetTransientState(addr types.Address, slot types.Hash) types.Hash {
	key := NewTransientStateKey(addr, slot)

	val, exists := txn.txLocalMap[key]
	if !exists {
		return types.Hash{}
	}

	return types.BytesToHash(val.value.([]byte)) //nolint:forcetypeassert
}

// ClearTransientStorage removes all transient storage entries. Must be called at the start
// of every tx because EIP-1153 requires transient storage to be empty at tx boundaries.
func (txn *TxnVerifier) ClearTransientStorage() {
	var toDelete []Key

	for key := range txn.txLocalMap {
		if key.IsTransientState() {
			toDelete = append(toDelete, key)
		}
	}

	for _, k := range toDelete {
		txn.deleteLocal(k)
	}
}

// GetDumpTree function returns accounts based on the selected criteria.
func (txn *TxnVerifier) GetDumpTree(dumpObject *Dump, opts *DumpInfo, deleteEmptyObjects bool) ([]byte, error) {
	return nil, nil
}

// StorageRangeAt returns the storage at the given block height and transaction index.
func (txn *TxnVerifier) StorageRangeAt(storageRangeResult *StorageRangeResult, addr *types.Address,
	keyStart []byte, maxResult int) error {
	return nil
}

// Snapshot takes a snapshot at this point in time
func (txn *TxnVerifier) Snapshot() int {
	id := len(txn.snapshots)
	txn.snapshots = append(txn.snapshots, txnSnapshot{
		journalLen: len(txn.journal),
		logsLen:    len(txn.txLocalLogs),
		refund:     txn.txLocalRefund,
	})

	return id
}

// RevertToSnapshot reverts to a given snapshot
func (txn *TxnVerifier) RevertToSnapshot(id int) error {
	if id < 0 || id > len(txn.snapshots)-1 {
		return fmt.Errorf("snapshot id %d out of the range", id)
	}

	snap := txn.snapshots[id]

	for i := len(txn.journal) - 1; i >= snap.journalLen; i-- {
		entry := txn.journal[i]
		if entry.existed {
			txn.txLocalMap[entry.key] = entry.oldValue
		} else {
			delete(txn.txLocalMap, entry.key)
		}
	}

	txn.journal = txn.journal[:snap.journalLen]
	txn.txLocalLogs = txn.txLocalLogs[:snap.logsLen]
	txn.txLocalRefund = snap.refund
	txn.snapshots = txn.snapshots[:id]

	return nil
}

// GetAccount returns an account
func (txn *TxnVerifier) GetAccount(addr types.Address) (*Account, bool) {
	object, exists := txn.getStateObject(addr)
	if !exists {
		return nil, false
	}

	return object.Account, true
}

func (txn *TxnVerifier) getStateObject(addr types.Address) (*StateObject, bool) {
	addKey := NewAddressKey(addr)
	// Try to get state from local tx map first
	valFromLocal, exists := txn.txLocalMap[addKey]
	if exists {
		obj := valFromLocal.value.(*StateObject) //nolint:forcetypeassert
		if obj.Deleted {
			return nil, false
		}

		return obj.Copy(), true
	}

	// then from radix tree which holds transient states during block processing
	txn.globalMutex.RLock()
	val, exists := txn.globalRadix.Get(addr.Bytes())
	txn.globalMutex.RUnlock()

	if exists {
		obj := val.(*StateObject) //nolint:forcetypeassert
		objCopy := obj.Copy()
		txn.setLocal(addKey, txLocalValue{
			isWritten: false,
			value:     objCopy,
		})

		if obj.Deleted {
			return nil, false
		}

		return objCopy, true
	}

	account, err := txn.snapshot.GetAccount(addr)
	if err != nil {
		return nil, false
	}

	if account == nil {
		return nil, false
	}

	obj := &StateObject{
		Account: account.Copy(),
	}

	return obj, true
}

func (txn *TxnVerifier) upsertAccount(addr types.Address, create bool, f func(object *StateObject)) {
	object, exists := txn.getStateObject(addr)
	if !exists && create {
		object = &StateObject{
			Account: &Account{
				Balance:  big.NewInt(0),
				CodeHash: types.EmptyCodeHash.Bytes(),
				Root:     emptyStateHash,
			},
		}
	}

	// run the callback to modify the account
	f(object)

	if object != nil {
		txn.setLocal(NewAddressKey(addr), txLocalValue{
			isWritten: true,
			value:     object,
		})
	}
}

func (txn *TxnVerifier) AddSealingReward(addr types.Address, balance *big.Int) {
	txn.upsertAccount(addr, true, func(object *StateObject) {
		if object.Suicide {
			*object = *newStateObject()
			object.Account.Balance.SetBytes(balance.Bytes())
		} else {
			object.Account.Balance.Add(object.Account.Balance, balance)
		}
	})
}

// AddBalance adds balance
func (txn *TxnVerifier) AddBalance(addr types.Address, amount *big.Int) {
	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.Balance.Add(object.Account.Balance, amount)
	})
}

// AddBalanceDoNotTrack credits addr directly in the shared globalRadix instead of staging the
// change in txLocalMap for the next PopulateBlockRadix. This path exists for implicit,
// executor-injected credits (block reward, coinbase fee, burn) that every transaction in the
// block applies to the same fixed address as a side effect the tx-dependency graph has no
// visibility into - two dependency-graph-independent transactions can legitimately run
// concurrently and both target it. Going through the normal upsertAccount/PopulateBlockRadix
// path (read a local copy, publish it later with an unconditional overwrite) would lose
// updates under that concurrency, so the read-modify-write here happens atomically in a single
// blockMutex critical section instead.
func (txn *TxnVerifier) AddBalanceDoNotTrack(addr types.Address, amount *big.Int) {
	if amount == nil || amount.Sign() == 0 {
		return
	}

	txn.globalMutex.Lock()
	defer txn.globalMutex.Unlock()

	var obj *StateObject

	// globalRadix only holds accounts touched earlier in *this* block. A miss here does not
	// mean the account is new - it may carry a balance/nonce/storage root persisted by prior
	// blocks, so that must be read through the snapshot before defaulting to a fresh object,
	// the same three-tier lookup getStateObject uses for every other read path.
	if raw, exists := txn.globalRadix.Get(addr.Bytes()); exists {
		obj = raw.(*StateObject).Copy() //nolint:forcetypeassert
	} else if account, err := txn.snapshot.GetAccount(addr); err == nil && account != nil {
		obj = &StateObject{Account: account.Copy()}
	} else {
		obj = newStateObject()
	}

	obj.Account.Balance.Add(obj.Account.Balance, amount)

	txn.globalRadix.Insert(addr.Bytes(), obj)
}

// SubBalance reduces the balance at address addr by amount
func (txn *TxnVerifier) SubBalance(addr types.Address, amount *big.Int) error {
	// If we try to reduce balance by 0, then it's a noop
	if amount.Sign() == 0 {
		return nil
	}

	// Check if we have enough balance to deduce amount from
	if balance := txn.GetBalance(addr); balance.Cmp(amount) < 0 {
		return runtime.ErrNotEnoughFunds
	}

	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.Balance.Sub(object.Account.Balance, amount)
	})

	return nil
}

// SetBalance sets the balance
func (txn *TxnVerifier) SetBalance(addr types.Address, balance *big.Int) {
	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.Balance.SetBytes(balance.Bytes())
	})
}

// GetBalance returns the balance of an address
func (txn *TxnVerifier) GetBalance(addr types.Address) *big.Int {
	object, exists := txn.getStateObject(addr)
	if !exists {
		return big.NewInt(0)
	}

	return object.Account.Balance
}

// EmitLog appends log to logs tree storage
func (txn *TxnVerifier) EmitLog(addr types.Address, topics []types.Hash, data []byte) {
	txn.txLocalLogs = append(txn.txLocalLogs, &types.Log{
		Address: addr,
		Topics:  topics,
		Data:    append([]byte(nil), data...),
	})
}

// Logs will retrieve logs (deleting is done on PopulateBlockRadix)
func (txn *TxnVerifier) Logs() []*types.Log {
	return txn.txLocalLogs
}

// State

// SetStorage sets the storage of an address
func (txn *TxnVerifier) SetStorage(
	addr types.Address,
	key types.Hash,
	value types.Hash,
	config *chain.ForksInTime,
) runtime.StorageStatus {
	return setStorage(txn, addr, key, value, config)
}

// SetState change the state of an address
func (txn *TxnVerifier) SetState(
	addr types.Address,
	key,
	value types.Hash,
) {
	txn.upsertAccount(addr, true, func(object *StateObject) {
		txn.setLocal(NewStateKey(addr, key), txLocalValue{
			isWritten: true,
			value:     value,
		})
	})
}

// GetState returns the state of the address at a given key
func (txn *TxnVerifier) GetState(addr types.Address, key types.Hash) types.Hash {
	stateKey := NewStateKey(addr, key)
	// first read from local tx map
	valFromLocal, exists := txn.txLocalMap[stateKey]
	if exists {
		//nolint:forcetypeassert
		return valFromLocal.value.(types.Hash)
	}
	// then try read from account
	object, exists := txn.getStateObject(addr)
	if !exists {
		return types.Hash{}
	}

	var (
		ok  bool
		val any
	)

	txn.globalMutex.RLock()
	// Try to get account state from radix tree first
	// Because the latest account state should be in in-memory radix tree
	// if account state update happened in previous transactions of same block
	if object.Txn != nil {
		val, ok = object.Txn.Get(key.Bytes())
	}

	txn.globalMutex.RUnlock()

	if ok {
		if val == nil {
			txn.setLocal(stateKey, txLocalValue{
				isWritten: false,
				value:     types.Hash{},
			})

			return types.Hash{}
		}

		result := types.BytesToHash(val.([]byte)) //nolint:forcetypeassert

		txn.setLocal(stateKey, txLocalValue{
			isWritten: false,
			value:     result,
		})

		return result
	}

	if object.withFakeStorage {
		return types.Hash{}
	}

	return txn.snapshot.GetStorage(addr, object.Account.Root, key)
}

// Nonce

// IncrNonce increases the nonce of the address
func (txn *TxnVerifier) IncrNonce(addr types.Address) error {
	var err error

	txn.upsertAccount(addr, true, func(object *StateObject) {
		if object.Account.Nonce+1 < object.Account.Nonce {
			err = ErrNonceUintOverflow

			return
		}

		object.Account.Nonce++
	})

	return err
}

// SetNonce reduces the balance
func (txn *TxnVerifier) SetNonce(addr types.Address, nonce uint64) {
	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.Nonce = nonce
	})
}

// GetNonce returns the nonce of an addr
func (txn *TxnVerifier) GetNonce(addr types.Address) uint64 {
	object, exists := txn.getStateObject(addr)
	if !exists {
		return 0
	}

	return object.Account.Nonce
}

// Code

// SetCode sets the code for an address
func (txn *TxnVerifier) SetCode(addr types.Address, code []byte) {
	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.CodeHash = crypto.Keccak256(code)
		object.DirtyCode = true
		object.Code = code
	})
}

// GetCode gets the code on a given address.
//
// Read path and caching (see also METRICS.md → State):
//
//   - One *Txn is reused for an entire block in Executor.ProcessBlock, so the
//     per-Txn LRU (codeCache, capacity 20) is scoped to that block transition,
//     not to each individual transaction.
//   - On a per-Txn miss we read through the snapshot (Pebble in production) and
//     populate the per-Txn LRU keyed by address.
//   - DirtyCode (contract created or updated in this transition, including
//     `WithStateOverride`) returns in-memory bytes and bypasses the LRU and
//     storage.
func (txn *TxnVerifier) GetCode(addr types.Address) []byte {
	object, exists := txn.getStateObject(addr)
	if !exists {
		return nil
	}

	if object.DirtyCode {
		return object.Code
	}
	//nolint:godox
	// TODO; Should we move this to state? (to be fixed in EVM-527)
	if v, ok := txn.codeCache.Get(addr); ok {
		metrics.IncrCounter([]string{"state", "code_cache", "hit"}, 1)
		//nolint:forcetypeassert
		return v.([]byte)
	}

	metrics.IncrCounter([]string{"state", "code_cache", "miss"}, 1)

	codeHash := types.BytesToHash(object.Account.CodeHash)

	start := time.Now().UTC()
	code, _ := txn.snapshot.GetCode(codeHash)

	metrics.MeasureSince([]string{"state", "code_db_read"}, start)

	txn.codeCache.Add(addr, code)

	return code
}

func (txn *TxnVerifier) GetCodeSize(addr types.Address) int {
	return len(txn.GetCode(addr))
}

func (txn *TxnVerifier) GetCodeHash(addr types.Address) types.Hash {
	object, exists := txn.getStateObject(addr)
	if !exists {
		return types.Hash{}
	}

	return types.BytesToHash(object.Account.CodeHash)
}

// Suicide marks the given account as suicided
func (txn *TxnVerifier) Suicide(addr types.Address) bool {
	var suicided bool

	txn.upsertAccount(addr, false, func(object *StateObject) {
		if object == nil || object.Suicide {
			suicided = false
		} else {
			suicided = true
			object.Suicide = true
		}

		if suicided {
			object.Account.Balance = new(big.Int)
		}
	})

	return suicided
}

// HasSuicided returns true if the account suicided
func (txn *TxnVerifier) HasSuicided(addr types.Address) bool {
	object, exists := txn.getStateObject(addr)

	return exists && object.Suicide
}

// Refund
func (txn *TxnVerifier) AddRefund(gas uint64) {
	txn.txLocalRefund += gas
}

func (txn *TxnVerifier) SubRefund(gas uint64) {
	txn.txLocalRefund -= gas
}

func (txn *TxnVerifier) GetRefund() (result uint64) {
	return txn.txLocalRefund
}

// GetCommittedState returns the state of the address in the trie
func (txn *TxnVerifier) GetCommittedState(addr types.Address, key types.Hash) types.Hash {
	obj, ok := txn.getStateObject(addr)
	if !ok {
		return types.Hash{}
	}

	return txn.snapshot.GetStorage(addr, obj.Account.Root, key)
}

// GetStorageRoot retrieves the storage root from the given address or empty
// if object not found.
func (txn *TxnVerifier) GetStorageRoot(addr types.Address) types.Hash {
	obj, ok := txn.getStateObject(addr)
	if !ok {
		return types.Hash{}
	}

	return obj.Account.Root
}

// SetFullStorage is used to replace the full state of the address.
// Only used for debugging on the override jsonrpc endpoint.
func (txn *TxnVerifier) SetFullStorage(addr types.Address, state map[types.Hash]types.Hash) {
	for k, v := range state {
		txn.SetState(addr, k, v)
	}

	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.withFakeStorage = true
	})
}

func (txn *TxnVerifier) TouchAccount(addr types.Address) {
	txn.upsertAccount(addr, true, func(obj *StateObject) {
		// Intentionally ignoring writeAccessMap update here
		// If account is being modified we will update writeAccessMap in the corresponding method
	})
}

func (txn *TxnVerifier) Exist(addr types.Address) bool {
	_, exists := txn.getStateObject(addr)

	return exists
}

func (txn *TxnVerifier) Empty(addr types.Address) bool {
	obj, exists := txn.getStateObject(addr)
	if !exists {
		return true
	}

	return obj.Empty()
}

func (txn *TxnVerifier) CreateAccount(addr types.Address) {
	obj := &StateObject{
		Account: &Account{
			Balance:  big.NewInt(0),
			CodeHash: types.EmptyCodeHash.Bytes(),
			Root:     emptyStateHash,
		},
	}

	prev, ok := txn.getStateObject(addr)
	if ok {
		obj.Account.Balance.SetBytes(prev.Account.Balance.Bytes())
	}

	txn.setLocal(NewAddressKey(addr), txLocalValue{
		isWritten: true,
		value:     obj,
	})
}

func (txn *TxnVerifier) CleanDeleteObjects(_ bool) error {
	// do nothing, everything will be cleared on PopulateBlockRadix
	return nil
}

func (txn *TxnVerifier) Commit(deleteEmptyObjects bool) ([]*Object, error) {
	txn.globalMutex.Lock()
	defer txn.globalMutex.Unlock()

	// Callers (e.g. consensus PreCommitState hooks such as staking contract deployment) can
	// mutate this Transition directly after Execute() has already returned and its per-tx
	// PopulateBlockRadix calls are done. Those writes only ever land in txLocalMap - nothing
	// else flushes them into the shared blockRadix that Commit reads from, so without this they
	// would be silently dropped instead of persisted. PopulateBlockRadix is a no-op when
	// txLocalMap is already empty, so this is safe to call unconditionally here.
	if err := txn.populateBlockRadixNoLock(); err != nil {
		return nil, err
	}

	return commitTxn(txn.globalRadix, deleteEmptyObjects)
}

func (txn *TxnVerifier) PopulateBlockRadix() error {
	txn.globalMutex.Lock()
	defer txn.globalMutex.Unlock()

	return txn.populateBlockRadixNoLock()
}

func (txn *TxnVerifier) populateBlockRadixNoLock() error {
	dirtyObjects := map[types.Address]*StateObject{}

	// First pass: propagate accounts and fill dirty objects map
	for key, val := range txn.txLocalMap {
		if !val.isWritten || !key.IsAddress() {
			continue
		}

		dirtyObjects[key.GetAddress()] = val.value.(*StateObject) //nolint:forcetypeassert
	}

	// Second pass: collect all storage writes per address, then insert once per address.
	for key, val := range txn.txLocalMap {
		if !val.isWritten || !key.IsState() {
			continue
		}

		addr := key.GetAddress()
		storageKey := key.GetStateKey()
		value := val.value.(types.Hash) //nolint:forcetypeassert

		// SetState always stages a matching AddressKey entry via upsertAccount, so Pass 1 must
		// already have populated dirtyObjects[addr] by the time any StateKey for addr is seen here.
		obj, seen := dirtyObjects[addr]
		if !seen {
			return fmt.Errorf("state write for %s has no corresponding account entry in txLocalMap", addr)
		}

		if obj.Txn == nil {
			obj.Txn = iradix.New().Txn()
		}

		if value == types.ZeroHash {
			obj.Txn.Insert(storageKey.Bytes(), nil)
		} else {
			obj.Txn.Insert(storageKey.Bytes(), value.Bytes())
		}
	}

	for addr, obj := range dirtyObjects {
		if obj.Deleted {
			txn.globalRadix.Delete(addr.Bytes())
		} else {
			txn.globalRadix.Insert(addr.Bytes(), obj)
		}
	}

	txn.txLocalLogs = nil                   // clean logs
	txn.txLocalRefund = 0                   // reset refunds
	txn.txLocalMap = map[Key]txLocalValue{} // delete tx local map
	txn.journal = nil
	txn.snapshots = nil

	return nil
}
