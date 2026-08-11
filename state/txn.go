package state

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	iradix "github.com/hashicorp/go-immutable-radix"
	"github.com/hashicorp/go-metrics"
	lru "github.com/hashicorp/golang-lru"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
)

var emptyStateHash = types.StringToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

const (
	DefaultClearingRefund = uint64(15000)
	LondonClearingRefund  = uint64(4800)

	LegacyRefundQuotient = uint64(2)
	LondonRefundQuotient = uint64(5)
)

type readSnapshot interface {
	GetStorage(addr types.Address, root types.Hash, key types.Hash) types.Hash
	GetAccount(addr types.Address) (*Account, error)
	GetCode(hash types.Hash) ([]byte, bool)
	GetRootHash() types.Hash
}

var (
	// logIndex is the index of the logs in the trie
	logIndex = types.BytesToHash([]byte{2}).Bytes()

	// refundIndex is the index of the refund
	refundIndex = types.BytesToHash([]byte{3}).Bytes()
)

// Txn is a reference of the state
type Txn struct {
	// underlying storage ("database"), it is accessed when the state object can't be found in the txn
	snapshot  readSnapshot
	snapshots []*iradix.Tree
	txn       *iradix.Txn
	codeCache *lru.Cache

	// sucidedAddrs collects accounts suicided since the last CleanRadixObjects
	sucidedAddrs map[types.Address]struct{}
	// transientKeys collects transient storage keys since the last CleanRadixObjects
	transientKeys map[string]struct{}

	recorder *TxAccessRecorder

	bar types.BlockAccessRecord
}

func NewTxn(snapshot Snapshot) *Txn {
	return newTxn(snapshot)
}

func (txn *Txn) GetRadix() *iradix.Txn {
	return txn.txn
}

func newTxn(snapshot readSnapshot) *Txn {
	i := iradix.New()

	codeCache, _ := lru.New(20)

	return &Txn{
		snapshot:      snapshot,
		snapshots:     []*iradix.Tree{},
		txn:           i.Txn(),
		codeCache:     codeCache,
		sucidedAddrs:  map[types.Address]struct{}{},
		transientKeys: map[string]struct{}{},
	}
}

var transientStorageKeyPrefix = byte(0x04)

func calculateTransientStorageSlotIradixKey(addr types.Address, slot types.Hash) []byte {
	// Transient storage slot iradix key represents concatenation of the following:
	// 	1. prefix 0x04 (1 byte)
	//	2. account address (20 bytes)
	//	3. storage slot (32 bytes)
	// Total length of 53 bytes guarantees no collision with any other key type in the tree.
	k := make([]byte, 1+types.AddressLength+types.HashLength)

	k[0] = transientStorageKeyPrefix

	copy(k[1:], addr.Bytes())

	copy(k[21:], slot.Bytes())

	// k = 0x04 || <20-bytes-address> || <32-bytes-slot>
	return k
}

// SetTransientState writes a value into transient storage for the given address and slot.
func (txn *Txn) SetTransientState(addr types.Address, slot types.Hash, value types.Hash) {
	key := calculateTransientStorageSlotIradixKey(addr, slot)

	if (value == types.Hash{}) {
		txn.txn.Delete(key)
		delete(txn.transientKeys, string(key))
	} else {
		txn.txn.Insert(key, value.Bytes())
		txn.transientKeys[string(key)] = struct{}{}
	}
}

// GetTransientState reads a value from transient storage for the given address and slot.
func (txn *Txn) GetTransientState(addr types.Address, slot types.Hash) types.Hash {
	key := calculateTransientStorageSlotIradixKey(addr, slot)

	val, exists := txn.txn.Get(key)

	if !exists {
		return types.Hash{}
	}

	return types.BytesToHash(val.([]byte)) //nolint:forcetypeassert
}

// GetDumpTree function returns accounts based on the selected criteria.
func (txn *Txn) GetDumpTree(dumpObject *Dump, opts *DumpInfo, deleteEmptyObjects bool) ([]byte, error) {
	if err := txn.cleanDeleteObjects(deleteEmptyObjects); err != nil {
		return nil, err
	}

	var (
		nextKey         []byte
		hasStartKey     = len(opts.Start) > 0 && !bytes.Equal(opts.Start, types.EmptyRootHash.Bytes())
		committedIradix = txn.txn.Commit()
	)

	dumpObject.Accounts = make(map[types.Address]DumpAccount)

	committedIradix.Root().Walk(func(k []byte, v interface{}) bool {
		a, ok := v.(*StateObject)
		if !ok {
			return false
		}

		if hasStartKey {
			if !bytes.Equal(opts.Start, k) {
				return false
			}

			hasStartKey = false
		}

		if opts.Max > 0 && len(dumpObject.Accounts) >= opts.Max {
			nextKey = k

			return true
		}

		if k == nil && opts.OnlyWithAddresses {
			return false
		}

		addrBytes := types.BytesToAddress(k)
		dumpAccount := DumpAccount{
			Nonce:    a.Account.Nonce,
			Address:  addrBytes,
			Balance:  a.Account.Balance.String(),
			Root:     a.Account.Root.Bytes(),
			CodeHash: a.Account.CodeHash,
			Key:      k,
		}

		if !opts.SkipCode {
			dumpAccount.Code = a.Code
		}

		if !a.Deleted && !opts.SkipStorage && a.Txn != nil {
			dumpAccount.Storage = make(map[types.Hash]string)

			a.Txn.Root().Walk(func(k []byte, v interface{}) bool {
				if k == nil || v == nil {
					return false
				}

				bytesValue, ok := v.([]byte)
				if !ok {
					return false
				}

				dumpAccount.Storage[types.BytesToHash(k)] = hex.EncodeToString(bytesValue)

				return false
			})
		}

		dumpObject.Accounts[addrBytes] = dumpAccount

		return false
	})

	return nextKey, nil
}

// StorageRangeAt returns the storage at the given block height and transaction index.
func (txn *Txn) StorageRangeAt(storageRangeResult *StorageRangeResult, addr *types.Address,
	keyStart []byte, maxResult int) error {
	storageRangeResult.Storage = make(storageMap)

	object, exists := txn.getStateObject(*addr)
	if !exists {
		return nil
	}

	if object.Txn != nil {
		hasStartKey := len(keyStart) > 0

		object.Txn.Root().Walk(func(k []byte, v interface{}) bool {
			if k == nil || v == nil {
				return false
			}

			if hasStartKey {
				if !bytes.Equal(keyStart, k) {
					return false
				}

				hasStartKey = false
			}

			if maxResult > 0 && len(storageRangeResult.Storage) >= maxResult {
				storageRangeResult.NextKey = k

				return true
			}

			bytesValue, ok := v.([]byte)
			if !ok {
				return false
			}

			storageRangeResult.Storage[types.BytesToHash(k)] = storageEntry{k, types.BytesToHash(bytesValue)}

			return false
		})
	}

	return nil
}

// Snapshot takes a snapshot at this point in time
func (txn *Txn) Snapshot() int {
	t := txn.txn.CommitOnly()

	id := len(txn.snapshots)
	txn.snapshots = append(txn.snapshots, t)

	return id
}

// RevertToSnapshot reverts to a given snapshot
func (txn *Txn) RevertToSnapshot(id int) error {
	if id > len(txn.snapshots)-1 {
		return fmt.Errorf("snapshot id %d out of the range", id)
	}

	tree := txn.snapshots[id]
	txn.txn = tree.Txn()

	return nil
}

// GetAccount returns an account
func (txn *Txn) GetAccount(addr types.Address) (*Account, bool) {
	object, exists := txn.getStateObject(addr)
	if !exists {
		return nil, false
	}

	return object.Account, true
}

func (txn *Txn) getStateObject(addr types.Address) (*StateObject, bool) {
	// Try to get state from radix tree which holds transient states during block processing first
	val, exists := txn.txn.Get(addr.Bytes())
	if exists {
		obj := val.(*StateObject) //nolint:forcetypeassert
		if obj.Deleted {
			return nil, false
		}

		return obj.Copy(), true
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

func (txn *Txn) upsertAccount(addr types.Address, create bool, f func(object *StateObject)) {
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
		txn.txn.Insert(addr.Bytes(), object)
	}
}

func (txn *Txn) AddSealingReward(addr types.Address, balance *big.Int) {
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
func (txn *Txn) AddBalance(addr types.Address, balance *big.Int) {
	if txn.recorder == nil || txn.bar == nil {
		txn.addBalanceState(addr, balance)
	} else {
		txn.addBalanceNonState(addr, balance)
	}
}

func (txn *Txn) addBalanceState(addr types.Address, balance *big.Int) {
	var newBalance *big.Int

	txn.upsertAccount(addr, true, func(object *StateObject) {
		newBalance = big.NewInt(0).Add(object.Account.Balance, balance)
		object.Account.Balance = newBalance
	})

	if txn.recorder != nil {
		txn.recorder.RecordBalanceChange(addr, newBalance)
	}
}

func (txn *Txn) addBalanceNonState(addr types.Address, balance *big.Int) {
	oldBalance := txn.GetBalance(addr)

	newBalance := big.NewInt(0).Add(oldBalance, balance)

	txn.recorder.RecordBalanceChange(addr, newBalance)
}

// SubBalance reduces the balance at address addr by amount
func (txn *Txn) SubBalance(addr types.Address, amount *big.Int) error {
	// If we try to reduce balance by 0, then it's a noop
	if amount.Sign() == 0 {
		return nil
	}

	// Check if we have enough balance to deduce amount from
	balance := txn.GetBalance(addr)
	if balance.Cmp(amount) < 0 {
		return runtime.ErrNotEnoughFunds
	}

	if txn.recorder == nil || txn.bar == nil {
		txn.upsertAccount(addr, true, func(object *StateObject) {
			object.Account.Balance.Sub(object.Account.Balance, amount)
		})
	}

	if txn.recorder != nil {
		txn.recorder.RecordBalanceChange(addr, big.NewInt(0).Sub(balance, amount))
	}

	return nil
}

// SetBalance sets the balance
func (txn *Txn) SetBalance(addr types.Address, balance *big.Int) {
	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.Balance.SetBytes(balance.Bytes())
	})
}

// GetBalance returns the balance of an address
func (txn *Txn) GetBalance(addr types.Address) *big.Int {
	if txn.recorder != nil {
		if balance, ok := txn.recorder.GetBalance(addr); ok {
			return balance
		}
	}

	if txn.bar != nil {
		if balance, ok := txn.bar.BalanceBefore(addr, txn.recorder.txIndex); ok {
			return balance
		}
	}

	object, exists := txn.getStateObject(addr)
	if !exists {
		return big.NewInt(0)
	}

	return object.Account.Balance
}

// EmitLog appends log to logs tree storage
func (txn *Txn) EmitLog(addr types.Address, topics []types.Hash, data []byte) {
	log := &types.Log{
		Address: addr,
		Topics:  topics,
	}
	log.Data = append(log.Data, data...)

	var logs []*types.Log

	val, exists := txn.txn.Get(logIndex)
	if !exists {
		logs = []*types.Log{}
	} else {
		logs = val.([]*types.Log) //nolint:forcetypeassert
	}

	logs = append(logs, log)
	txn.txn.Insert(logIndex, logs)
}

// State

// SetStorage sets the storage of an address
func (txn *Txn) SetStorage(
	addr types.Address,
	key types.Hash,
	value types.Hash,
	config *chain.ForksInTime,
) runtime.StorageStatus {
	oldValue := txn.GetState(addr, key)
	if oldValue == value {
		return runtime.StorageUnchanged
	}

	current := oldValue                          // current - storage dirtied by previous lines of this contract
	original := txn.GetCommittedState(addr, key) // storage slot before this transaction started

	txn.SetState(addr, key, value)

	legacyGasMetering := !config.Istanbul && (config.Petersburg || !config.Constantinople)

	if legacyGasMetering {
		if oldValue == types.ZeroHash {
			return runtime.StorageAdded
		} else if value == types.ZeroHash {
			txn.AddRefund(15000)

			return runtime.StorageDeleted
		}

		return runtime.StorageModified
	}

	clearingRefund := DefaultClearingRefund
	if config.London {
		clearingRefund = LondonClearingRefund
	}

	if original == current {
		if original == types.ZeroHash { // create slot (2.1.1)
			return runtime.StorageAdded
		}

		if value == types.ZeroHash { // delete slot (2.1.2b)
			txn.AddRefund(clearingRefund)

			return runtime.StorageDeleted
		}

		return runtime.StorageModified
	}

	if original != types.ZeroHash { // Storage slot was populated before this transaction started
		if current == types.ZeroHash { // recreate slot (2.2.1.1)
			txn.SubRefund(clearingRefund)
		} else if value == types.ZeroHash { // delete slot (2.2.1.2)
			txn.AddRefund(clearingRefund)
		}
	}

	if original == value {
		if original == types.ZeroHash { // reset to original nonexistent slot (2.2.2.1)
			// Storage was used as memory (allocation and deallocation occurred within the same contract)
			if config.Istanbul {
				txn.AddRefund(19200)
			} else {
				txn.AddRefund(19800)
			}
		} else { // reset to original existing slot (2.2.2.2)
			if config.Istanbul {
				txn.AddRefund(4200)
			} else {
				txn.AddRefund(4800)
			}
		}
	}

	return runtime.StorageModifiedAgain
}

// SetState change the state of an address
func (txn *Txn) SetState(
	addr types.Address,
	key,
	value types.Hash,
) {
	if txn.recorder == nil || txn.bar == nil {
		txn.upsertAccount(addr, true, func(object *StateObject) {
			if object.Txn == nil {
				object.Txn = iradix.New().Txn()
			}

			if value == types.ZeroHash {
				object.Txn.Insert(key.Bytes(), nil)
			} else {
				object.Txn.Insert(key.Bytes(), value.Bytes())
			}
		})
	}

	if txn.recorder != nil {
		txn.recorder.RecordStorageChange(addr, key, value)
	}
}

// GetState returns the state of the address at a given key
func (txn *Txn) GetState(addr types.Address, key types.Hash) types.Hash {
	if txn.recorder != nil {
		if value, ok := txn.recorder.GetStorage(addr, key); ok {
			return value
		}
	}

	if txn.bar != nil {
		if value, ok := txn.bar.SlotBefore(addr, key, txn.recorder.txIndex); ok {
			return value
		}
	}

	object, exists := txn.getStateObject(addr)
	if !exists {
		return types.Hash{}
	}

	// Try to get account state from radix tree first
	// Because the latest account state should be in in-memory radix tree
	// if account state update happened in previous transactions of same block
	if object.Txn != nil {
		if val, ok := object.Txn.Get(key.Bytes()); ok {
			if val == nil {
				return types.Hash{}
			}
			//nolint:forcetypeassert
			return types.BytesToHash(val.([]byte))
		}
	}

	if object.withFakeStorage {
		return types.Hash{}
	}

	return txn.snapshot.GetStorage(addr, object.Account.Root, key)
}

// Nonce

// IncrNonce increases the nonce of the address
func (txn *Txn) IncrNonce(addr types.Address) error {
	// We work directly with the state in two cases:
	// 1. when EIP-7928 is off; in this case txn.recorder is nil
	// 2. when the executor is the block proposer; in this case txn.bar is nil
	if txn.recorder == nil || txn.bar == nil {
		return txn.incrNonceState(addr)
	}

	return txn.incrNonceNonState(addr)
}

func (txn *Txn) incrNonceState(addr types.Address) error {
	var (
		err   error
		nonce uint64
	)

	txn.upsertAccount(addr, true, func(object *StateObject) {
		if object.Account.Nonce+1 < object.Account.Nonce {
			err = ErrNonceUintOverflow

			return
		}

		object.Account.Nonce++

		nonce = object.Account.Nonce
	})

	// If EIP-7928 is enabled, txn.recorder must NOT be nil.
	if err == nil && txn.recorder != nil {
		txn.recorder.RecordNonceChange(addr, nonce)
	}

	return err
}

func (txn *Txn) incrNonceNonState(addr types.Address) error {
	nonce, ok := txn.recorder.GetNonce(addr)

	if !ok && txn.bar != nil {
		nonce, ok = txn.bar.NonceBefore(addr, txn.recorder.txIndex)
	}

	if !ok {
		txn.upsertAccount(addr, true, func(object *StateObject) {
			nonce = object.Account.Nonce
		})
	}

	if nonce+1 < nonce {
		return ErrNonceUintOverflow
	}

	txn.recorder.RecordNonceChange(addr, nonce+1)

	return nil
}

// SetNonce reduces the balance
func (txn *Txn) SetNonce(addr types.Address, nonce uint64) {
	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.Nonce = nonce
	})
}

// GetNonce returns the nonce of an addr
func (txn *Txn) GetNonce(addr types.Address) uint64 {
	if txn.recorder != nil {
		if nonce, ok := txn.recorder.GetNonce(addr); ok {
			return nonce
		}
	}

	if txn.bar != nil {
		if nonce, ok := txn.bar.NonceBefore(addr, txn.recorder.txIndex); ok {
			return nonce
		}
	}

	object, exists := txn.getStateObject(addr)
	if !exists {
		return 0
	}

	return object.Account.Nonce
}

// Code

// SetCode sets the code for an address
func (txn *Txn) SetCode(addr types.Address, code []byte) {
	if txn.recorder == nil || txn.bar == nil {
		// TODO: dirty code handle
		txn.upsertAccount(addr, true, func(object *StateObject) {
			object.Account.CodeHash = crypto.Keccak256(code)
			object.DirtyCode = true
			object.Code = code
		})
	}

	if txn.recorder != nil {
		txn.recorder.RecordCodeChange(addr, code)
	}
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
func (txn *Txn) GetCode(addr types.Address) []byte {
	if txn.recorder != nil {
		if code, ok := txn.recorder.GetCode(addr); ok {
			return code
		}
	}

	if txn.bar != nil {
		if code, ok := txn.bar.CodeBefore(addr, txn.recorder.txIndex); ok {
			return code
		}
	}

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

func (txn *Txn) GetCodeSize(addr types.Address) int {
	return len(txn.GetCode(addr))
}

func (txn *Txn) GetCodeHash(addr types.Address) types.Hash {
	if txn.recorder != nil {
		if code, ok := txn.recorder.GetCode(addr); ok {
			return types.BytesToHash(code)
		}
	}

	if txn.bar != nil {
		if code, ok := txn.bar.CodeBefore(addr, txn.recorder.txIndex); ok {
			return types.BytesToHash(code)
		}
	}

	object, exists := txn.getStateObject(addr)
	if !exists {
		return types.Hash{}
	}

	return types.BytesToHash(object.Account.CodeHash)
}

// Suicide marks the given account as suicided
func (txn *Txn) Suicide(addr types.Address) bool {
	var suicided bool

	txn.upsertAccount(addr, false, func(object *StateObject) {
		if object == nil || object.Suicide {
			suicided = false
		} else {
			suicided = true
			object.Suicide = true
			txn.sucidedAddrs[addr] = struct{}{}
		}

		if object != nil {
			object.Account.Balance = new(big.Int)
		}
	})

	return suicided
}

// HasSuicided returns true if the account suicided
func (txn *Txn) HasSuicided(addr types.Address) bool {
	object, exists := txn.getStateObject(addr)

	return exists && object.Suicide
}

// Refund
func (txn *Txn) AddRefund(gas uint64) {
	refund := txn.GetRefund() + gas
	txn.txn.Insert(refundIndex, refund)
}

func (txn *Txn) SubRefund(gas uint64) {
	refund := txn.GetRefund() - gas
	txn.txn.Insert(refundIndex, refund)
}

func (txn *Txn) Logs() []*types.Log {
	data, exists := txn.txn.Get(logIndex)
	if !exists {
		return nil
	}

	txn.txn.Delete(logIndex)
	//nolint:forcetypeassert
	return data.([]*types.Log)
}

func (txn *Txn) GetRefund() uint64 {
	data, exists := txn.txn.Get(refundIndex)
	if !exists {
		return 0
	}

	//nolint:forcetypeassert
	return data.(uint64)
}

// GetCommittedState returns the state of the address in the trie
func (txn *Txn) GetCommittedState(addr types.Address, key types.Hash) types.Hash {
	if txn.bar != nil {
		if value, ok := txn.bar.SlotBefore(addr, key, txn.recorder.txIndex); ok {
			return value
		}
	} else {
		val, exists := txn.snapshots[0].Get(addr.Bytes())
		if exists {
			object := val.(*StateObject) //nolint:forcetypeassert
			if object.Deleted || object.Suicide {
				return types.Hash{}
			}

			if object.Txn != nil {
				if val, ok := object.Txn.Get(key.Bytes()); ok {
					if val == nil {
						return types.Hash{}
					}
					//nolint:forcetypeassert
					return types.BytesToHash(val.([]byte))
				}
			}
		}
	}

	account, err := txn.snapshot.GetAccount(addr)
	if account == nil || err != nil {
		return types.Hash{}
	}

	obj := &StateObject{
		Account: account.Copy(),
	}

	return txn.snapshot.GetStorage(addr, obj.Account.Root, key)
}

// GetStorageRoot retrieves the storage root from the given address or empty
// if object not found.
func (txn *Txn) GetStorageRoot(addr types.Address) types.Hash {
	obj, ok := txn.getStateObject(addr)
	if !ok {
		return types.Hash{}
	}

	return obj.Account.Root
}

// SetFullStorage is used to replace the full state of the address.
// Only used for debugging on the override jsonrpc endpoint.
func (txn *Txn) SetFullStorage(addr types.Address, state map[types.Hash]types.Hash) {
	for k, v := range state {
		txn.SetState(addr, k, v)
	}

	txn.upsertAccount(addr, true, func(object *StateObject) {
		object.withFakeStorage = true
	})
}

func (txn *Txn) TouchAccount(addr types.Address) {
	txn.upsertAccount(addr, true, func(obj *StateObject) {

	})
}

func (txn *Txn) Exist(addr types.Address) bool {
	_, exists := txn.getStateObject(addr)

	return exists
}

func (txn *Txn) Empty(addr types.Address) bool {
	obj, exists := txn.getStateObject(addr)
	if !exists {
		return true
	}

	return obj.Empty()
}

func newStateObject() *StateObject {
	return &StateObject{
		Account: &Account{
			Balance:  big.NewInt(0),
			CodeHash: types.EmptyCodeHash.Bytes(),
			Root:     emptyStateHash,
		},
	}
}

func (txn *Txn) CreateAccount(addr types.Address) {
	if txn.recorder == nil || txn.bar == nil {
		txn.createAccountState(addr)
	} else {
		txn.createAccountNonState(addr)
	}
}

func (txn *Txn) createAccountState(addr types.Address) {
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

	txn.txn.Insert(addr.Bytes(), obj)

	if txn.recorder != nil {
		// TODO: check the way it encodes empty map and slice!
		txn.recorder.RecordBalanceChange(addr, obj.Account.Balance)
		txn.recorder.RecordNonceChange(addr, 0)
		txn.recorder.RecordCodeChange(addr, []byte{})
	}
}

func (txn *Txn) createAccountNonState(addr types.Address) {
	var balance *big.Int

	_, ok := txn.recorder.current[addr]

	if ok {
		balance = txn.recorder.current[addr].Balance
	}

	if balance == nil {
		// TODO
	}

	if balance == nil {
		if prev, ok := txn.getStateObject(addr); ok {
			balance = prev.Account.Balance
		}
	}

	if balance == nil {
		balance = big.NewInt(0)
	}

	// TODO: check the way it encodes empty map and slice!
	txn.recorder.RecordBalanceChange(addr, balance)
	txn.recorder.RecordNonceChange(addr, 0)
	txn.recorder.RecordCodeChange(addr, []byte{})
}

// cleanDeleteObjects cleans all suicided or empty blocks (if deleteEmptyObjects) from radix
func (txn *Txn) cleanDeleteObjects(deleteEmptyObjects bool) error {
	remove := [][]byte{}

	txn.txn.Root().Walk(func(k []byte, v interface{}) bool {
		a, ok := v.(*StateObject)
		if !ok {
			return false
		}

		if a.Suicide || a.Empty() && deleteEmptyObjects {
			remove = append(remove, k)
		}

		return false
	})

	for _, k := range remove {
		v, ok := txn.txn.Get(k)
		if !ok {
			return fmt.Errorf("failed to retrieve value for %s key", string(k))
		}

		obj, ok := v.(*StateObject)
		if !ok {
			return errors.New("found object is not of StateObject type")
		}

		obj2 := obj.Copy()
		obj2.Deleted = true
		txn.txn.Insert(k, obj2)
	}

	// delete refunds
	txn.txn.Delete(refundIndex)

	return nil
}

func (txn *Txn) Commit(deleteEmptyObjects bool) ([]*Object, error) {
	if err := txn.cleanDeleteObjects(deleteEmptyObjects); err != nil {
		return nil, err
	}

	x := txn.txn.Commit()

	// Do a more complex thing for now
	objs := []*Object{}

	x.Root().Walk(func(k []byte, v interface{}) bool {
		a, ok := v.(*StateObject)
		if !ok {
			// We also have logs, avoid those
			return false
		}

		obj := &Object{
			Nonce:     a.Account.Nonce,
			Address:   types.BytesToAddress(k),
			Balance:   a.Account.Balance,
			Root:      a.Account.Root,
			CodeHash:  types.BytesToHash(a.Account.CodeHash),
			DirtyCode: a.DirtyCode,
			Code:      a.Code,
		}
		if a.Deleted {
			obj.Deleted = true
		} else if a.Txn != nil {
			a.Txn.Root().Walk(func(k []byte, v interface{}) bool {
				store := &StorageObject{Key: k}
				if v == nil {
					store.Deleted = true
				} else {
					store.Val = v.([]byte) //nolint:forcetypeassert
				}

				obj.Storage = append(obj.Storage, store)

				return false
			})
		}

		objs = append(objs, obj)

		return false
	})

	return objs, nil
}

// CleanRadixObjects cleans suicided accounts, transient storage, refund index
func (txn *Txn) CleanRadixObjects() error {
	// clean suicided accounts
	for addr := range txn.sucidedAddrs {
		v, ok := txn.txn.Get(addr.Bytes())
		if !ok {
			// the write was rolled back (reverted tx); nothing to inspect
			continue
		}

		obj, ok := v.(*StateObject)
		if !ok {
			return errors.New("found object is not of StateObject type")
		}

		if obj.Suicide {
			obj2 := obj.Copy()
			obj2.Deleted = true
			txn.txn.Insert(addr.Bytes(), obj2)
		}
	}

	// clean transient storage
	for keyStr := range txn.transientKeys {
		key := []byte(keyStr)

		_, ok := txn.txn.Get(key)
		if !ok {
			// the write was rolled back (reverted tx); nothing to inspect
			continue
		}

		txn.txn.Delete(key)
	}

	// clean refunds
	txn.txn.Delete(refundIndex)

	// clear the tracking maps
	clear(txn.sucidedAddrs)
	clear(txn.transientKeys)

	return nil
}

var createdContractKeyPrefix = byte(0x05)

func calculateCreatedContractIradixKey(addr types.Address) []byte {
	k := make([]byte, 1+types.AddressLength)
	k[0] = createdContractKeyPrefix
	copy(k[1:], addr.Bytes())

	return k // 0x05 || <20-bytes-address>
}

// MarkContractCreated marks addr as created within the current transaction (EIP-6780).
func (txn *Txn) MarkContractCreated(addr types.Address) {
	txn.txn.Insert(calculateCreatedContractIradixKey(addr), true)
}

// IsContractCreatedInTx reports whether addr was created in the current transaction.
func (txn *Txn) IsContractCreatedInTx(addr types.Address) bool {
	_, exists := txn.txn.Get(calculateCreatedContractIradixKey(addr))

	return exists
}

// ClearCreatedContracts removes all creation markers. Must be called at the start of every tx.
func (txn *Txn) ClearCreatedContracts() {
	var toDelete [][]byte

	txn.txn.Root().Walk(func(key []byte, value interface{}) bool {
		if len(key) == 1+types.AddressLength && key[0] == createdContractKeyPrefix {
			toDelete = append(toDelete, key)
		}

		return false
	})

	for _, k := range toDelete {
		txn.txn.Delete(k)
	}
}
