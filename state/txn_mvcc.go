package state

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/hashicorp/go-metrics"
	lru "github.com/hashicorp/golang-lru"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
)

// MVMemoryAccess is the read side of an STM batch's multi-version memory, as seen by a
// single incarnation's TxnMVCC. Implemented by state/stm.MVMemory; declared here (rather
// than state/stm depending back into state) purely to avoid an import cycle - state/stm is
// the only real implementation, and it stays in full control of writes/estimates/deletes.
type MVMemoryAccess interface {
	// Read resolves a read of key performed by txIndex: the highest version installed by a
	// strictly lower tx index, or found=false if none exists (caller must fall back to its
	// base snapshot). isEstimate=true means the visible version is a placeholder left by an
	// incarnation that's being re-executed - the caller must abort itself immediately rather
	// than use it.
	Read(key Key, txIndex int) (val any, foundTxIndex, foundIncarnation int, isEstimate, found bool)
}

// EstimateAbort is panicked from deep inside EVM execution (mirroring how this codebase's
// EVM interpreter already signals reverts/out-of-gas via ExecutionResult, just one level
// higher up the stack) when a read observes a placeholder left by an incarnation that is
// being re-executed. The recovering caller (state/stm's executor) must discard this
// incarnation's work entirely and retry once BlockedOn's transaction has produced a fresh
// result.
type EstimateAbort struct {
	BlockedOn int
}

func (e *EstimateAbort) Error() string {
	return fmt.Sprintf("state: stm read blocked on uncommitted tx %d", e.BlockedOn)
}

// KeyValue is one entry of an incarnation's write-set, ready for installation into a batch's
// multi-version memory.
type KeyValue struct {
	Key   Key
	Value any
}

// mvReadRecord remembers, for one read performed during this incarnation, which version was
// observed - so a later validation pass can detect whether it is still current.
// observedTxIndex == -1 means the read fell through to the batch's base state (dst's
// already-merged state from earlier batches, layered on the true base trie); such a read
// stays valid as long as it *still* falls through (the base never changes mid-block).
//
// epochAddr is non-nil only for storage-key reads: GetState's resolution for a storage key
// isn't a plain mv.Read (see resolveStorageRead) - it additionally depends on the owning
// account's accountEpochKey, so re-validating it must redo that exact same two-step
// resolution rather than a plain mv.Read, or a storage key whose raw mv version never changes
// would spuriously (and permanently) fail validation forever once its account-epoch override
// has ever applied.
type mvReadRecord struct {
	key                 Key
	observedTxIndex     int
	observedIncarnation int
	epochAddr           *types.Address
}

// TxnMVCC is an ITransitionTxn backend for one speculative incarnation of one transaction
// within a state/stm batch. It layers three read sources, checked in order: this
// incarnation's own not-yet-installed local writes, the batch's shared multi-version memory
// (other transactions' installed results), and finally the batch's base state. Writes are
// buffered in `local` only; state/stm decides when (and whether) to install them into shared
// memory, and later merges the batch's final validated incarnations into the block-wide
// TxnVerifier via FlushBatchInto.
type TxnMVCC struct {
	snapshot readSnapshot
	// trueBase is the block's true pre-block state (dst's own base snapshot, bypassing dst's
	// globalRadix entirely - i.e. every in-block change, including earlier batches/txs in this
	// same block). Used only by GetCommittedState: the EIP-2200/3529 gas-refund reference point
	// is "the value before this transaction" in this codebase's actual (block-oblivious, not
	// spec's transaction-oblivious) implementation - Txn's real GetCommittedState has the exact
	// same property, since txn.snapshot there is likewise the block's fixed parent root and
	// in-block storage writes are never folded back into an object's Account.Root mid-block.
	// snapshot must NOT be used here, or a slot written by an earlier batch/tx in this block
	// would be seen as the "original" value, understating the true gas cost (see
	// TestEngine_CrossChunkStorageVisibility).
	trueBase readSnapshot
	mv       MVMemoryAccess

	txIndex     int
	incarnation int

	codeCache *lru.Cache

	local          map[Key]txLocalValue
	readSet        []mvReadRecord
	pendingCredits map[types.Address]*big.Int

	// dagTracker records reads/writes at the exact call sites the sequential Txn's real
	// ITxAccessTracker does (see tx_access_tracker.go and txn.go) - e.g. TouchAccount and the
	// getStateObject calls behind Exist/Empty/GetStorageRoot/HasSuicided are deliberately NOT
	// recorded here, matching Txn precisely. This is intentionally separate from readSet (which
	// records every true mv/base resolution, for MVCC validation - broader by design and unrelated
	// to what verifiers need) and from local's isWritten flag (which governs flushing, not DAG
	// export). Only this tracker feeds GetReadWriteSet/the dependency DAG.
	dagTracker ITxAccessTracker

	txLocalLogs   []*types.Log
	txLocalRefund uint64

	journal   []journalEntry
	snapshots []txnSnapshot
}

// newTxnMVCC constructs a TxnMVCC for one incarnation attempt of tx txIndex. snapshot is the
// batch's base read source (typically a dstReadSnapshot wrapping the block-wide TxnVerifier);
// trueBase is dst's own base snapshot (see the TxnMVCC.trueBase field doc); mv is the batch's
// shared multi-version memory.
func newTxnMVCC(snapshot, trueBase readSnapshot, mv MVMemoryAccess, txIndex, incarnation int) *TxnMVCC {
	codeCache, _ := lru.New(20)

	dagTracker := TxAccessTrackerFactory(false)
	// checkSenderAccount (EIP-3607) runs before Transition.Apply's own ClearAccessTracker(false)
	// call and can touch dagTracker first, so it must already be initialized here - matching
	// newTxnWithTxAccessTracker's identical Clear(false) call for the same reason.
	dagTracker.Clear(false)

	return &TxnMVCC{
		snapshot:       snapshot,
		trueBase:       trueBase,
		mv:             mv,
		txIndex:        txIndex,
		incarnation:    incarnation,
		local:          map[Key]txLocalValue{},
		pendingCredits: map[types.Address]*big.Int{},
		codeCache:      codeCache,
		dagTracker:     dagTracker,
	}
}

func (t *TxnMVCC) recordRead(key Key, foundTxIndex, foundIncarnation int) {
	t.readSet = append(t.readSet, mvReadRecord{
		key:                 key,
		observedTxIndex:     foundTxIndex,
		observedIncarnation: foundIncarnation,
	})
}

// WriteSet returns the keys this incarnation wrote (isWritten entries of local), ready for
// installation into the batch's multi-version memory.
func (t *TxnMVCC) WriteSet() []KeyValue {
	var out []KeyValue

	for key, val := range t.local {
		if !val.isWritten {
			continue
		}

		out = append(out, KeyValue{Key: key, Value: val.value})
	}

	return out
}

// PendingCredits returns the deferred, untracked balance credits (coinbase fee, burn amount)
// accumulated by this incarnation via AddBalanceDoNotTrack.
func (t *TxnMVCC) PendingCredits() map[types.Address]*big.Int {
	return t.pendingCredits
}

// Validate re-resolves every read this incarnation performed against the batch's current
// multi-version memory and reports whether every one of them still observes the exact same
// version (or still falls through to base) as when this incarnation actually executed. A
// false result means this incarnation is stale and must be discarded and re-executed.
func (t *TxnMVCC) Validate() bool {
	for _, r := range t.readSet {
		var (
			foundTxIndex, foundIncarnation int
			found, blocked                 bool
		)

		if r.epochAddr != nil {
			_, foundTxIndex, foundIncarnation, found, _, blocked = t.resolveStorageRead(*r.epochAddr, r.key)
		} else {
			var isEstimate bool

			_, foundTxIndex, foundIncarnation, isEstimate, found = t.mv.Read(r.key, t.txIndex)
			blocked = found && isEstimate
		}

		if blocked {
			return false
		}

		if !found {
			if r.observedTxIndex != -1 {
				return false
			}

			continue
		}

		if r.observedTxIndex == -1 || foundTxIndex != r.observedTxIndex || foundIncarnation != r.observedIncarnation {
			return false
		}
	}

	return true
}

// GetReadWriteSet implements ITransitionTxn and is the STM engine's source for the dependency
// DAG fed to blockstm.DepsBuilder. It delegates entirely to dagTracker, so it matches the
// sequential access tracker's exact call sites and dedup rule (see tx_access_tracker.go) -
// verifiers consume an identical DAG shape regardless of which builder produced it.
func (t *TxnMVCC) GetReadWriteSet(txIndx int) TxReadWriteSet {
	return t.dagTracker.GetReadWriteSet(txIndx)
}

func (t *TxnMVCC) ClearAccessTracker(writesOnly bool) {
	t.dagTracker.Clear(writesOnly)

	// Reverting local writes (RevertToSnapshot, called right after this on a hard failure)
	// already undoes the write side; reads already performed en route to that failure must
	// stay recorded so a lower tx's later write can still invalidate and trigger a retry.
	if !writesOnly {
		t.readSet = nil

		// Transition.Apply's EIP-3607 checkSenderAccount check runs before this call and can
		// populate local's read-cache (e.g. the sender's address key) without ever reaching the
		// readSet-recording branch below - local is fresh for this incarnation at this point (no
		// real writes exist yet), so clearing it too guarantees the very next read of any such
		// key is treated as a genuine first read and actually gets into readSet, instead of
		// silently hitting a stale cache entry from before this reset.
		t.local = map[Key]txLocalValue{}
	}
}

func (t *TxnMVCC) ClearLocalChanges() {
	t.txLocalLogs = nil
	t.txLocalRefund = 0
	t.journal = nil
	t.snapshots = nil
}

// setLocal writes key into local with journaling, identical to TxnVerifier.setLocal.
func (t *TxnMVCC) setLocal(key Key, val any, isWritten bool) {
	old, existed := t.local[key]
	t.journal = append(t.journal, journalEntry{key: key, existed: existed, oldValue: old})
	t.local[key] = txLocalValue{isWritten: isWritten, value: val}
}

// deleteLocal removes key from local with journaling, identical to TxnVerifier.deleteLocal.
func (t *TxnMVCC) deleteLocal(key Key) {
	old, existed := t.local[key]
	if !existed {
		return
	}

	t.journal = append(t.journal, journalEntry{key: key, existed: true, oldValue: old})
	delete(t.local, key)
}

// Snapshot takes a snapshot at this point in time
func (t *TxnMVCC) Snapshot() int {
	id := len(t.snapshots)
	t.snapshots = append(t.snapshots, txnSnapshot{
		journalLen: len(t.journal),
		logsLen:    len(t.txLocalLogs),
		refund:     t.txLocalRefund,
	})

	return id
}

// RevertToSnapshot reverts to a given snapshot
func (t *TxnMVCC) RevertToSnapshot(id int) error {
	if id < 0 || id > len(t.snapshots)-1 {
		return fmt.Errorf("snapshot id %d out of the range", id)
	}

	snap := t.snapshots[id]

	for i := len(t.journal) - 1; i >= snap.journalLen; i-- {
		entry := t.journal[i]
		if entry.existed {
			t.local[entry.key] = entry.oldValue
		} else {
			delete(t.local, entry.key)
		}
	}

	t.journal = t.journal[:snap.journalLen]
	t.txLocalLogs = t.txLocalLogs[:snap.logsLen]
	t.txLocalRefund = snap.refund
	t.snapshots = t.snapshots[:id]

	return nil
}

// getStateObject resolves an account through local -> mv -> base, in that order, caching the
// result locally (isWritten:false) so repeated reads within this incarnation stay consistent
// and cheap. Panics with *EstimateAbort if mv holds an unresolved placeholder for this key.
func (t *TxnMVCC) getStateObject(addr types.Address) (*StateObject, bool) {
	addrKey := NewAddressKey(addr)

	if v, ok := t.local[addrKey]; ok {
		obj, _ := v.value.(*StateObject) //nolint:forcetypeassert
		if obj == nil || obj.Suicide {
			return nil, false
		}

		return obj.Copy(false), true
	}

	val, foundTxIndex, foundIncarnation, isEstimate, found := t.mv.Read(addrKey, t.txIndex)
	if found && isEstimate {
		panic(&EstimateAbort{BlockedOn: foundTxIndex})
	}

	var (
		obj    *StateObject
		exists bool
	)

	if found {
		srcObj, _ := val.(*StateObject) //nolint:forcetypeassert
		if srcObj != nil && !srcObj.Suicide {
			obj, exists = srcObj.Copy(false), true
		}

		t.recordRead(addrKey, foundTxIndex, foundIncarnation)
	} else {
		account, err := t.snapshot.GetAccount(addr)
		if err == nil && account != nil {
			obj, exists = &StateObject{Account: account.Copy()}, true
		}

		t.recordRead(addrKey, -1, 0)
	}

	t.setLocal(addrKey, obj, false)

	return obj, exists
}

func (t *TxnMVCC) GetAccount(addr types.Address) (*Account, bool) {
	object, exists := t.getStateObject(addr)
	if !exists {
		return nil, false
	}

	return object.Account, true
}

func (t *TxnMVCC) upsertAccount(addr types.Address, create bool, f func(object *StateObject)) {
	object, exists := t.getStateObject(addr)
	if !exists && create {
		object = newStateObject()
	}

	f(object)

	if object != nil {
		t.setLocal(NewAddressKey(addr), object, true)
	}
}

func (t *TxnMVCC) AddSealingReward(addr types.Address, balance *big.Int) {
	t.upsertAccount(addr, true, func(object *StateObject) {
		if object.Suicide {
			*object = *newStateObject()
			object.dirtyFields = true
			object.Account.Balance.SetBytes(balance.Bytes())
		} else {
			object.dirtyFields = true
			object.Account.Balance.Add(object.Account.Balance, balance)
		}

		t.dagTracker.AddWrite(addr, BalancePath, new(big.Int).Set(object.Account.Balance))
	})
}

func (t *TxnMVCC) AddBalance(addr types.Address, amount *big.Int) {
	t.upsertAccount(addr, true, func(object *StateObject) {
		object.dirtyFields = object.dirtyFields || amount.Sign() != 0
		object.Account.Balance.Add(object.Account.Balance, amount)

		if amount.Sign() > 0 {
			t.dagTracker.AddWrite(addr, BalancePath, new(big.Int).Set(object.Account.Balance))
		}
	})
}

// AddBalanceDoNotTrack accumulates addr's credit for this incarnation only. It never enters
// local/mv, so it can never conflict with or be observed by any other transaction; state/stm
// merges only the winning incarnation's credits into the block once this tx is finalized (see
// FlushBatchInto), discarding any aborted incarnation's contribution entirely.
func (t *TxnMVCC) AddBalanceDoNotTrack(addr types.Address, amount *big.Int) {
	if amount == nil || amount.Sign() == 0 {
		return
	}

	if existing, ok := t.pendingCredits[addr]; ok {
		existing.Add(existing, amount)
	} else {
		t.pendingCredits[addr] = new(big.Int).Set(amount)
	}
}

func (t *TxnMVCC) SubBalance(addr types.Address, amount *big.Int) error {
	if amount.Sign() == 0 {
		return nil
	}

	object, exists := t.getStateObject(addr)
	if !exists || object.Account.Balance.Cmp(amount) < 0 {
		t.dagTracker.AddWrite(addr, BalancePath, big.NewInt(0))

		return runtime.ErrNotEnoughFunds
	}

	object.Account.Balance.Sub(object.Account.Balance, amount)
	object.dirtyFields = true

	t.setLocal(NewAddressKey(addr), object, true)
	t.dagTracker.AddWrite(addr, BalancePath, new(big.Int).Set(object.Account.Balance))

	return nil
}

func (t *TxnMVCC) SetBalance(addr types.Address, balance *big.Int) {
	t.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.Balance.SetBytes(balance.Bytes())
		object.dirtyFields = true

		if object.Account.Balance.Cmp(balance) != 0 {
			t.dagTracker.AddWrite(addr, BalancePath, balance)
		}
	})
}

func (t *TxnMVCC) GetBalance(addr types.Address) *big.Int {
	t.dagTracker.AddRead(addr, BalancePath)

	object, exists := t.getStateObject(addr)
	if !exists {
		return big.NewInt(0)
	}

	return object.Account.Balance
}

func (t *TxnMVCC) EmitLog(addr types.Address, topics []types.Hash, data []byte) {
	t.txLocalLogs = append(t.txLocalLogs, &types.Log{
		Address: addr,
		Topics:  topics,
		Data:    append([]byte(nil), data...),
	})
}

func (t *TxnMVCC) Logs() []*types.Log {
	return t.txLocalLogs
}

func (t *TxnMVCC) SetStorage(
	addr types.Address,
	key types.Hash,
	value types.Hash,
	config *chain.ForksInTime,
) runtime.StorageStatus {
	return setStorage(t, addr, key, value, config)
}

func (t *TxnMVCC) SetState(addr types.Address, key, value types.Hash) {
	t.upsertAccount(addr, true, func(object *StateObject) {
		t.setLocal(NewStateKey(addr, key), value, true)
	})

	t.dagTracker.AddStorageWrite(addr, key, value)
}

// accountEpochKey marks an address's "storage lineage boundary": written whenever CreateAccount
// or a successful Suicide runs (see those methods), read by GetState to tell whether a storage
// key's mv version predates the account's most recent reset. Real sequential Txn never needs
// this - it nests storage inside the account's own StateObject.Txn, so a fresh account object
// structurally has no way to see old storage. This codebase's flat key-value model has no such
// relationship between an address key and its storage keys, so it needs an explicit one: without
// it, a storage write from before a selfdestruct would remain visible to a later transaction that
// recreates the same address in the same batch (see TestEngine_MetamorphicCreate2Redeploy).
func accountEpochKey(addr types.Address) Key {
	return NewSubpathKey(addr, SuicidePath)
}

// resolveStorageRead resolves a storage key exactly as GetState needs to: the raw mv version,
// overridden to "not found" if the owning account has been reset (see accountEpochKey) since
// that version was written. It has no side effects (no caching, no read recording), so both
// GetState (which then caches + records) and Validate (which must re-derive the identical
// effective observation, not a plain mv.Read - see mvReadRecord.epochAddr) can share it.
func (t *TxnMVCC) resolveStorageRead(addr types.Address, stateKey Key) (
	val any, foundTxIndex, foundIncarnation int, found bool, blockedOn int, blocked bool,
) {
	var isEstimate bool

	val, foundTxIndex, foundIncarnation, isEstimate, found = t.mv.Read(stateKey, t.txIndex)
	if found && isEstimate {
		return nil, 0, 0, false, foundTxIndex, true
	}

	if !found {
		return val, foundTxIndex, foundIncarnation, found, 0, false
	}

	epochKey := accountEpochKey(addr)

	_, epochTxIndex, _, epochEstimate, epochFound := t.mv.Read(epochKey, t.txIndex)
	if epochFound && epochEstimate {
		return nil, 0, 0, false, epochTxIndex, true
	}

	// the account was reset after this storage version was written - it belongs to a destroyed
	// lineage and must not be visible to this read
	if epochFound && epochTxIndex > foundTxIndex {
		found = false
	}

	return val, foundTxIndex, foundIncarnation, found, 0, false
}

func (t *TxnMVCC) recordStorageRead(stateKey Key, addr types.Address, foundTxIndex, foundIncarnation int) {
	t.readSet = append(t.readSet, mvReadRecord{
		key: stateKey, observedTxIndex: foundTxIndex, observedIncarnation: foundIncarnation, epochAddr: &addr,
	})
}

func (t *TxnMVCC) GetState(addr types.Address, key types.Hash) types.Hash {
	t.dagTracker.AddStorageRead(addr, key)

	stateKey := NewStateKey(addr, key)

	if v, ok := t.local[stateKey]; ok {
		return v.value.(types.Hash) //nolint:forcetypeassert
	}

	val, foundTxIndex, foundIncarnation, found, blockedOn, blocked := t.resolveStorageRead(addr, stateKey)
	if blocked {
		panic(&EstimateAbort{BlockedOn: blockedOn})
	}

	var result types.Hash

	if found {
		result, _ = val.(types.Hash) //nolint:forcetypeassert
		t.recordStorageRead(stateKey, addr, foundTxIndex, foundIncarnation)
	} else {
		if obj, exists := t.getStateObject(addr); exists {
			result = t.snapshot.GetStorage(addr, obj.Account.Root, key)
		}

		t.recordStorageRead(stateKey, addr, -1, 0)
	}

	t.setLocal(stateKey, result, false)

	return result
}

func (t *TxnMVCC) IncrNonce(addr types.Address) error {
	var err error

	t.upsertAccount(addr, true, func(object *StateObject) {
		if object.Account.Nonce+1 < object.Account.Nonce {
			err = ErrNonceUintOverflow

			return
		}

		object.dirtyFields = true
		object.Account.Nonce++

		t.dagTracker.AddWrite(addr, NoncePath, object.Account.Nonce+1)
	})

	return err
}

func (t *TxnMVCC) SetNonce(addr types.Address, nonce uint64) {
	t.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.Nonce = nonce
		object.dirtyFields = true

		t.dagTracker.AddWrite(addr, NoncePath, nonce)
	})
}

func (t *TxnMVCC) GetNonce(addr types.Address) uint64 {
	t.dagTracker.AddRead(addr, NoncePath)

	object, exists := t.getStateObject(addr)
	if !exists {
		return 0
	}

	return object.Account.Nonce
}

func (t *TxnMVCC) SetCode(addr types.Address, code []byte) {
	t.upsertAccount(addr, true, func(object *StateObject) {
		object.Account.CodeHash = crypto.Keccak256(code)
		object.DirtyCode = true
		object.Code = code
		object.dirtyFields = true

		t.dagTracker.AddWrite(addr, CodePath, code)
	})
}

func (t *TxnMVCC) GetCode(addr types.Address) []byte {
	t.dagTracker.AddRead(addr, CodePath)

	object, exists := t.getStateObject(addr)
	if !exists {
		return nil
	}

	if object.DirtyCode {
		return object.Code
	}

	if v, ok := t.codeCache.Get(addr); ok {
		metrics.IncrCounter([]string{"state", "code_cache", "hit"}, 1)
		//nolint:forcetypeassert
		return v.([]byte)
	}

	metrics.IncrCounter([]string{"state", "code_cache", "miss"}, 1)

	codeHash := types.BytesToHash(object.Account.CodeHash)

	start := time.Now().UTC()
	code, _ := t.snapshot.GetCode(codeHash)

	metrics.MeasureSince([]string{"state", "code_db_read"}, start)

	t.codeCache.Add(addr, code)

	return code
}

func (t *TxnMVCC) GetCodeSize(addr types.Address) int {
	t.dagTracker.AddRead(addr, CodePath)

	return len(t.GetCode(addr))
}

func (t *TxnMVCC) GetCodeHash(addr types.Address) types.Hash {
	t.dagTracker.AddRead(addr, CodePath)

	object, exists := t.getStateObject(addr)
	if !exists {
		return types.Hash{}
	}

	return types.BytesToHash(object.Account.CodeHash)
}

func (t *TxnMVCC) Suicide(addr types.Address) bool {
	var suicided bool

	t.upsertAccount(addr, false, func(object *StateObject) {
		if object == nil || object.Suicide {
			suicided = false
		} else {
			suicided = true
			object.Suicide = true
		}

		if suicided {
			object.Account.Balance = new(big.Int)
			object.dirtyFields = true

			t.dagTracker.AddWrite(addr, SuicidePath, true)
			t.setLocal(accountEpochKey(addr), true, true)
		}
	})

	return suicided
}

func (t *TxnMVCC) HasSuicided(addr types.Address) bool {
	object, exists := t.getStateObject(addr)

	return exists && object.Suicide
}

func (t *TxnMVCC) AddRefund(gas uint64) {
	t.txLocalRefund += gas
}

func (t *TxnMVCC) SubRefund(gas uint64) {
	t.txLocalRefund -= gas
}

func (t *TxnMVCC) GetRefund() uint64 {
	return t.txLocalRefund
}

func (t *TxnMVCC) GetCommittedState(addr types.Address, key types.Hash) types.Hash {
	obj, ok := t.getStateObject(addr)
	if !ok {
		return types.Hash{}
	}

	return t.trueBase.GetStorage(addr, obj.Account.Root, key)
}

func (t *TxnMVCC) GetStorageRoot(addr types.Address) types.Hash {
	obj, ok := t.getStateObject(addr)
	if !ok {
		return types.Hash{}
	}

	return obj.Account.Root
}

func (t *TxnMVCC) SetFullStorage(addr types.Address, state map[types.Hash]types.Hash) {
	for k, v := range state {
		t.SetState(addr, k, v)
	}

	t.upsertAccount(addr, true, func(object *StateObject) {
		object.withFakeStorage = true
	})
}

func (t *TxnMVCC) TouchAccount(addr types.Address) {
	t.upsertAccount(addr, true, func(obj *StateObject) {})
}

func (t *TxnMVCC) Exist(addr types.Address) bool {
	_, exists := t.getStateObject(addr)

	return exists
}

func (t *TxnMVCC) Empty(addr types.Address) bool {
	obj, exists := t.getStateObject(addr)
	if !exists {
		return true
	}

	return obj.Empty()
}

func (t *TxnMVCC) CreateAccount(addr types.Address) {
	obj := &StateObject{
		Account: &Account{
			Balance:  big.NewInt(0),
			CodeHash: types.EmptyCodeHash.Bytes(),
			Root:     emptyStateHash,
		},
		dirtyFields: true,
	}

	prev, ok := t.getStateObject(addr)
	if ok {
		obj.Account.Balance.SetBytes(prev.Account.Balance.Bytes())
	}

	t.setLocal(NewAddressKey(addr), obj, true)
	t.dagTracker.AddWrite(addr, FullPath, true)
	t.setLocal(accountEpochKey(addr), true, true)
}

func (t *TxnMVCC) SetTransientState(addr types.Address, slot types.Hash, value types.Hash) {
	key := NewTransientStateKey(addr, slot)

	if (value == types.Hash{}) {
		t.deleteLocal(key)
	} else {
		t.setLocal(key, value.Bytes(), true)
	}
}

func (t *TxnMVCC) GetTransientState(addr types.Address, slot types.Hash) types.Hash {
	key := NewTransientStateKey(addr, slot)

	val, exists := t.local[key]
	if !exists {
		return types.Hash{}
	}

	return types.BytesToHash(val.value.([]byte)) //nolint:forcetypeassert
}

// CleanRadixObjects removes all transient storage entries from local, matching
// TxnVerifier.CleanRadixObjects (suicide/deleted reconciliation happens later, at flush time).
func (t *TxnMVCC) CleanRadixObjects() error {
	var toDelete []Key

	for key := range t.local {
		if key.IsTransientState() {
			toDelete = append(toDelete, key)
		}
	}

	for _, k := range toDelete {
		t.deleteLocal(k)
	}

	return nil
}

func (t *TxnMVCC) GetDumpTree(dumpObject *Dump, opts *DumpInfo, deleteEmptyObjects bool) ([]byte, error) {
	return nil, nil
}

func (t *TxnMVCC) StorageRangeAt(storageRangeResult *StorageRangeResult, addr *types.Address,
	keyStart []byte, maxResult int) error {
	return nil
}

// Commit, PopulateBlockRadix, AddPendingBalances and SetCurrentTxContext exist only to
// satisfy ITransitionTxn: state/stm never calls them. A per-incarnation TxnMVCC's results are
// gathered directly by state/stm (WriteSet/GetReadWriteSet/PendingCredits/Validate) and merged
// into the block-wide TxnVerifier via FlushBatchInto once finalized - never via Commit.

func (t *TxnMVCC) Commit(deleteEmptyObjects bool) ([]*Object, error) {
	return nil, errors.New("state: TxnMVCC.Commit must never be called; STM incarnations are merged via FlushBatchInto")
}

func (t *TxnMVCC) PopulateBlockRadix() error {
	return nil
}

func (t *TxnMVCC) AddPendingBalances() {
}

func (t *TxnMVCC) SetCurrentTxContext(txContext TxWithIndex) {
}

var _ ITransitionTxn = (*TxnMVCC)(nil)

// dstReadSnapshot adapts a block-wide TxnVerifier's accumulated state (everything merged from
// earlier STM batches in this block, layered on the true base trie) into the readSnapshot
// shape, so a new batch's TxnMVCC instances see every earlier batch's committed effects, not
// just the block's original parent state root. It reads dst's globalRadix/snapshot directly
// (through the package-level getStateObject/getState helpers) rather than through dst's own
// getStateObject/GetState methods, because those cache into dst.txLocalMap - a plain map that
// is only safe for the single worker that owns a TxnVerifier, and here many STM workers across
// many batches would race on it concurrently.
type dstReadSnapshot struct {
	dst *TxnVerifier
}

func (d dstReadSnapshot) GetAccount(addr types.Address) (*Account, error) {
	d.dst.globalMutex.RLock()
	obj, exists := getStateObject(d.dst.globalRadix, d.dst.snapshot, addr, false)
	d.dst.globalMutex.RUnlock()

	if !exists {
		return nil, nil
	}

	return obj.Account, nil
}

func (d dstReadSnapshot) GetStorage(addr types.Address, _ types.Hash, key types.Hash) types.Hash {
	d.dst.globalMutex.RLock()
	val := getState(d.dst.globalRadix, d.dst.snapshot, addr, key, false)
	d.dst.globalMutex.RUnlock()

	return val
}

func (d dstReadSnapshot) GetCode(hash types.Hash) ([]byte, bool) {
	return d.dst.snapshot.GetCode(hash)
}

func (d dstReadSnapshot) GetRootHash() types.Hash {
	return d.dst.snapshot.GetRootHash()
}

var _ readSnapshot = dstReadSnapshot{}

// FlushBatchInto merges every incarnation's validated local writes and deferred balance
// credits into dst, in final block order, flushing after each one individually - exactly the
// cadence TxDependancyExecutor already uses (Write then PopulateBlockRadix, per tx). This
// matters, not just mirrors: populateBlockRadixNoLock's storage pass re-attaches a flat storage
// key to whatever object currently sits at that address, keyed only by the CURRENT account
// object's Suicide flag - it has no notion of which transaction a storage write chronologically
// belongs to. Accumulating every incarnation's local writes together before a single flush would
// let a later resurrection (a fresh, non-suicided object) silently reabsorb an earlier,
// now-orphaned storage write from before a selfdestruct in between (e.g. tx0 writes storage,
// tx1 selfdestructs the account, tx2 sends it plain value - a real EVM resurrection must come up
// with no code and no storage). Flushing per incarnation instead lets each transaction's storage
// writes get consumed and merged into the CURRENT account object immediately, so a subsequent
// selfdestruct's Deleted flag (also set on flush) correctly severs anything written before it.
func FlushBatchInto(dst *TxnVerifier, incarnations []*TxnMVCC) error {
	dst.globalMutex.Lock()
	defer dst.globalMutex.Unlock()

	for _, t := range incarnations {
		for key, val := range t.local {
			if !val.isWritten {
				continue
			}

			dst.setLocal(key, val.value, true)
		}

		for addr, amount := range t.pendingCredits {
			dst.AddBalanceDoNotTrack(addr, amount)
		}

		if err := dst.populateBlockRadixNoLock(); err != nil {
			return err
		}
	}

	return nil
}
