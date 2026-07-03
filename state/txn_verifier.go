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

	// AddBalanceDoNotTrack keeps global balance amounts for special addresses (block creator, burn contract)
	globalAddBalances map[types.Address]*big.Int

	// Journal + snapshots undo a failed call's txLocalMap writes on RevertToSnapshot; logs and
	// refund only need their prior length/value remembered, not full per-entry journaling.
	journal   []journalEntry
	snapshots []txnSnapshot
}

func createBlockRadix() *iradix.Txn {
	return iradix.New().Txn()
}

func NewTxnVerifier(
	snapshot readSnapshot,
	blockMutex *sync.RWMutex,
	blockRadix *iradix.Txn,
	globalAddBalances map[types.Address]*big.Int,
) *TxnVerifier {
	codeCache, _ := lru.New(20)

	return &TxnVerifier{
		snapshot:          snapshot,
		globalMutex:       blockMutex,
		globalRadix:       blockRadix,
		globalAddBalances: globalAddBalances,
		txLocalMap:        map[Key]txLocalValue{},
		codeCache:         codeCache,
	}
}

func (txn *TxnVerifier) ClearAccessTracker(writesOnly bool) {
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
		txn.setLocal(key, value.Bytes(), true)
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
		if obj == nil || obj.Deleted {
			return nil, false
		}

		return obj.Copy(), true
	}

	// then from global processing block radix and snapshot
	txn.globalMutex.RLock()

	obj, exists := getStateObject(txn.globalRadix, txn.snapshot, addr)

	txn.setLocal(addKey, obj, false)

	txn.globalMutex.RUnlock()

	return obj, exists
}

func (txn *TxnVerifier) upsertAccount(addr types.Address, create bool, f func(object *StateObject)) {
	object, exists := txn.getStateObject(addr)
	if !exists && create {
		object = newStateObject()
	}

	// run the callback to modify the account
	f(object)

	if object != nil {
		txn.setLocal(NewAddressKey(addr), object, true)
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

// AddBalanceDoNotTrack accumulates addr's credit in the pendingAddBalances map
func (txn *TxnVerifier) AddBalanceDoNotTrack(addr types.Address, amount *big.Int) {
	if amount == nil || amount.Sign() == 0 {
		return
	}

	txn.globalMutex.Lock()
	defer txn.globalMutex.Unlock()

	if existing, ok := txn.globalAddBalances[addr]; ok {
		existing.Add(existing, amount)
	} else {
		txn.globalAddBalances[addr] = new(big.Int).Set(amount)
	}
}

// SubBalance reduces the balance at address addr by amount
func (txn *TxnVerifier) SubBalance(addr types.Address, amount *big.Int) error {
	// If we try to reduce balance by 0, then it's a noop
	if amount.Sign() == 0 {
		return nil
	}

	object, exists := txn.getStateObject(addr)
	// Check if we have enough balance to deduce amount from
	// if not exists balance will be zero, so no need to create empty object
	if !exists || object.Account.Balance.Cmp(amount) < 0 {
		return runtime.ErrNotEnoughFunds
	}

	object.Account.Balance.Sub(object.Account.Balance, amount)

	txn.setLocal(NewAddressKey(addr), object, true)

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
	txn.setLocal(NewStateKey(addr, key), value, true)
}

// GetState returns the state of the address at a given key
func (txn *TxnVerifier) GetState(addr types.Address, key types.Hash) types.Hash {
	stateKey := NewStateKey(addr, key)
	// first read from local tx map
	valFromLocal, valFromLocalExists := txn.txLocalMap[stateKey]
	if valFromLocalExists {
		return valFromLocal.value.(types.Hash) //nolint:forcetypeassert
	}

	// then try to from global radix tree which holds transient states during block processing
	txn.globalMutex.RLock()
	val := getState(txn.globalRadix, txn.snapshot, addr, key)
	txn.globalMutex.RUnlock()

	txn.setLocal(stateKey, val, false)

	return val
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

	txn.setLocal(NewAddressKey(addr), obj, true)
}

func (txn *TxnVerifier) CleanDeleteObjects(_ bool) error {
	// do nothing, everything will be cleared on PopulateBlockRadix
	return nil
}

func (txn *TxnVerifier) Commit(deleteEmptyObjects bool) ([]*Object, error) {
	txn.globalMutex.Lock()
	defer txn.globalMutex.Unlock()

	txn.addPendingBalancesUnlock()
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

		// if state object not in dirty objects or empty pull it from global radix or snapshot
		obj, seen := dirtyObjects[addr]
		if !seen || obj.Empty() {
			obj, seen = getStateObject(txn.globalRadix, txn.snapshot, addr)
			if !seen {
				obj = newStateObject()
			}
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
		txn.globalRadix.Insert(addr.Bytes(), obj)
	}

	txn.cleanAll()

	return nil
}

func (txn *TxnVerifier) addPendingBalancesUnlock() {
	// Apply pending AddBalanceDoNotTrack credits to global radix
	for addr, amount := range txn.globalAddBalances {
		obj, exists := getStateObject(txn.globalRadix, txn.snapshot, addr)
		if !exists {
			obj = newStateObject()
		}

		obj.Account.Balance.Add(obj.Account.Balance, amount)
		txn.globalRadix.Insert(addr.Bytes(), obj)
	}
}

func (txn *TxnVerifier) cleanAll() {
	txn.txLocalLogs = nil                   // clean logs
	txn.txLocalRefund = 0                   // reset refunds
	txn.txLocalMap = map[Key]txLocalValue{} // delete tx local map
	txn.journal = nil
	txn.snapshots = nil
}

// setLocal writes key into txLocalMap with journaling
func (txn *TxnVerifier) setLocal(key Key, val any, isWritten bool) {
	old, existed := txn.txLocalMap[key]
	txn.journal = append(txn.journal, journalEntry{key: key, existed: existed, oldValue: old})
	txn.txLocalMap[key] = txLocalValue{
		isWritten: isWritten,
		value:     val,
	}
}

// deleteLocal removes key from txLocalMap with journaling
func (txn *TxnVerifier) deleteLocal(key Key) {
	old, existed := txn.txLocalMap[key]
	if !existed {
		return
	}

	txn.journal = append(txn.journal, journalEntry{key: key, existed: true, oldValue: old})
	delete(txn.txLocalMap, key)
}
