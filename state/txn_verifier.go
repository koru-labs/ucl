package state

import (
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

// Txn is a reference of the state
type TxnVerifier struct {
	snapshot readSnapshot

	codeCache *lru.Cache

	blockMutex *sync.RWMutex
	blockRadix *iradix.Txn
	txLocalMap map[Key]txLocalValue
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
		snapshot:   snapshot,
		blockRadix: blockRadix,
		txLocalMap: map[Key]txLocalValue{},
		codeCache:  codeCache,
		blockMutex: blockMutex,
	}
}

func (txn *TxnVerifier) ClearAccessTracker(writesOnly bool) {
	txn.txLocalMap = make(map[Key]txLocalValue)
}

func (txn *TxnVerifier) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	return blockstm.TxReadWriteSet{}
}

// SetTransientState writes a value into transient storage for the given address and slot.
func (txn *TxnVerifier) SetTransientState(addr types.Address, slot types.Hash, value types.Hash) {
	key := NewTransientStateKey(addr, slot)

	if (value == types.Hash{}) {
		delete(txn.txLocalMap, key)
	} else {
		txn.txLocalMap[key] = txLocalValue{
			isWritten: true,
			value:     value.Bytes(),
		}
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
		delete(txn.txLocalMap, k)
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
	return 1
}

// RevertToSnapshot reverts to a given snapshot
func (txn *TxnVerifier) RevertToSnapshot(id int) error {
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
	txn.blockMutex.RLock()
	val, exists := txn.blockRadix.Get(addr.Bytes())
	txn.blockMutex.RUnlock()

	if exists {
		obj := val.(*StateObject) //nolint:forcetypeassert
		objCopy := obj.Copy()
		txn.txLocalMap[addKey] = txLocalValue{
			isWritten: false,
			value:     objCopy,
		}

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
		txn.txLocalMap[NewAddressKey(addr)] = txLocalValue{
			isWritten: true,
			value:     object,
		}
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

// AddBalanceDoNotTrack for verifier is the same as AddBalance
func (txn *TxnVerifier) AddBalanceDoNotTrack(addr types.Address, amount *big.Int) {
	txn.AddBalance(addr, amount)
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
	logIndexKey := NewLogIndexKey()

	log := &types.Log{
		Address: addr,
		Topics:  topics,
	}
	log.Data = append(log.Data, data...)

	var logs []*types.Log

	valFromLocal, exists := txn.txLocalMap[logIndexKey]
	if exists {
		logs = valFromLocal.value.([]*types.Log) //nolint:forcetypeassert
	} else {
		txn.blockMutex.RLock()
		val, exists := txn.blockRadix.Get(logIndex)
		txn.blockMutex.RUnlock()

		if exists {
			logs = val.([]*types.Log) //nolint:forcetypeassert
		}
	}

	txn.txLocalMap[logIndexKey] = txLocalValue{
		isWritten: true,
		value:     append(logs, log),
	}
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
		txn.txLocalMap[NewStateKey(addr, key)] = txLocalValue{
			isWritten: true,
			value:     value,
		}
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

	txn.blockMutex.RLock()
	// Try to get account state from radix tree first
	// Because the latest account state should be in in-memory radix tree
	// if account state update happened in previous transactions of same block
	if object.Txn != nil {
		if val, ok := object.Txn.Get(key.Bytes()); ok {
			txn.blockMutex.RUnlock()

			if val == nil {
				txn.txLocalMap[stateKey] = txLocalValue{
					value:     types.Hash{},
					isWritten: false,
				}

				return types.Hash{}
			}
			//nolint:forcetypeassert
			result := types.BytesToHash(val.([]byte))
			txn.txLocalMap[stateKey] = txLocalValue{
				value:     result,
				isWritten: false,
			}

			return result
		}
	}

	txn.blockMutex.RUnlock()

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
	refund := txn.GetRefund() + gas
	txn.txLocalMap[NewRefundIndexKey()] = txLocalValue{
		isWritten: true,
		value:     refund,
	}
}

func (txn *TxnVerifier) SubRefund(gas uint64) {
	refund := txn.GetRefund() - gas
	txn.txLocalMap[NewRefundIndexKey()] = txLocalValue{
		isWritten: true,
		value:     refund,
	}
}

func (txn *TxnVerifier) Logs() []*types.Log {
	logIndexKey := NewLogIndexKey()

	dataFromLocal, exists := txn.txLocalMap[logIndexKey]
	if exists {
		delete(txn.txLocalMap, logIndexKey) // why?
		//nolint:forcetypeassert
		return dataFromLocal.value.([]*types.Log)
	}

	txn.blockMutex.RLock()

	data, exists := txn.blockRadix.Get(logIndex)
	if exists {
		txn.blockRadix.Delete(logIndex)
	}

	txn.blockMutex.RUnlock()

	result := ([]*types.Log)(nil)

	if exists {
		//nolint:forcetypeassert
		result = data.([]*types.Log)
	}

	txn.txLocalMap[logIndexKey] = txLocalValue{
		isWritten: false,
		value:     result,
	}

	return result
}

func (txn *TxnVerifier) GetRefund() (result uint64) {
	refundKey := NewRefundIndexKey()

	dataFromLocal, exists := txn.txLocalMap[refundKey]
	if exists {
		//nolint:forcetypeassert
		return dataFromLocal.value.(uint64)
	}

	txn.blockMutex.RLock()
	data, exists := txn.blockRadix.Get(refundIndex)
	txn.blockMutex.RUnlock()

	if exists {
		//nolint:forcetypeassert
		result = data.(uint64)
	}

	txn.txLocalMap[refundKey] = txLocalValue{
		isWritten: false,
		value:     result,
	}

	return result
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

	txn.txLocalMap[NewAddressKey(addr)] = txLocalValue{
		isWritten: true,
		value:     obj,
	}
}

func (txn *TxnVerifier) CleanDeleteObjects(deleteEmptyObjects bool) error {
	remove := []Key{}

	for key, val := range txn.txLocalMap {
		if !key.IsAddress() {
			continue
		}

		a, ok := val.value.(*StateObject)
		if !ok {
			continue
		}

		if a.Suicide || a.Empty() && deleteEmptyObjects {
			remove = append(remove, key)
		}
	}

	for _, key := range remove {
		val, exists := txn.txLocalMap[key]
		if !exists {
			continue
		}

		obj, ok := val.value.(*StateObject)
		if !ok {
			continue
		}

		obj2 := obj.Copy()
		obj2.Deleted = true

		txn.txLocalMap[key] = txLocalValue{
			isWritten: true,
			value:     obj2,
		}
	}

	// delete refunds
	delete(txn.txLocalMap, NewRefundIndexKey())

	return nil
}

func (txn *TxnVerifier) Commit(deleteEmptyObjects bool) ([]*Object, error) {
	txn.blockMutex.Lock()
	defer txn.blockMutex.Unlock()

	return commitTxn(txn.blockRadix, deleteEmptyObjects)
}

func (txn *TxnVerifier) PopulateBlockRadix() error {
	txn.blockMutex.Lock()
	defer txn.blockMutex.Unlock()

	// First pass: propagate account, log, and refund writes into blockRadix.
	// Must happen before the storage pass so StateKey processing sees the latest StateObject.
	for key, val := range txn.txLocalMap {
		if !val.isWritten {
			continue
		}

		switch {
		case key.IsAddress():
			addr := key.GetAddress()
			obj := val.value.(*StateObject) //nolint:forcetypeassert

			if obj.Deleted {
				txn.blockRadix.Delete(addr.Bytes())
			} else {
				txn.blockRadix.Insert(addr.Bytes(), obj)
			}
		case key.IsLogIndex():
			txn.blockRadix.Insert(logIndex, val.value)
		case key.IsRefundIndex():
			txn.blockRadix.Insert(refundIndex, val.value)
		}
	}

	// Second pass: collect all storage writes per address, then insert once per address.
	dirtyObjects := map[types.Address]*StateObject{}

	for key, val := range txn.txLocalMap {
		if !val.isWritten || !key.IsState() {
			continue
		}

		addr := key.GetAddress()
		storageKey := key.GetStateKey()
		value := val.value.(types.Hash) //nolint:forcetypeassert

		obj, seen := dirtyObjects[addr]
		if !seen {
			if raw, exists := txn.blockRadix.Get(addr.Bytes()); exists {
				obj = raw.(*StateObject).Copy() //nolint:forcetypeassert
			} else {
				obj = newStateObject()
			}

			if obj.Txn == nil {
				obj.Txn = iradix.New().Txn()
			}

			dirtyObjects[addr] = obj
		}

		if value == (types.Hash{}) {
			obj.Txn.Insert(storageKey.Bytes(), nil)
		} else {
			obj.Txn.Insert(storageKey.Bytes(), value.Bytes())
		}
	}

	for addr, obj := range dirtyObjects {
		txn.blockRadix.Insert(addr.Bytes(), obj)
	}

	return nil
}
