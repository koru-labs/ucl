/*
Package itrie implements ShardedSnapshot - a hybrid sharding storage strategy for blockchain state.

Architecture Overview:
  - globalRoot = Account Trie Root (compatible with Ethereum)
  - Normal accounts: single Storage Trie per account (traditional)
  - Native Token accounts: 256 independent storage shards per account

Storage Mapping:
  - DB Key: "shard_" + accountRoot -> Value: 256 shard roots (8192 bytes)
  - Native Token account.Root = Keccak256(shard0Root || shard1Root || ... || shard255Root)

Sharding Strategy:
  - shardID = storageKey[0] (first byte of storage key, 0-255)

Performance Features:
  - Three-level parallelism: account-level + shard-level + batch write
  - Lazy loading: shards loaded on demand
  - Unified batch write: all DB writes collected and executed once
*/
package itrie

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/armon/go-metrics"
	"github.com/umbracle/fastrlp"
)

// ============================================================================
// Constants
// ============================================================================

// DefaultShardCount is the number of storage shards for each native token account.
// Using 256 shards allows sharding by the first byte of storage key (0-255).
const DefaultShardCount = 256

// ============================================================================
// Main Struct
// ============================================================================

// ShardedSnapshot implements a hybrid sharding strategy for blockchain state storage.
//
// Architecture:
//   - globalRoot = Account Trie Root (same as Ethereum, for compatibility)
//   - Normal accounts: per-account Storage Trie (via embedded Snapshot)
//   - Native accounts: 256 independent storage shards stored in Trie.shards
//
// Root Calculation:
//   - globalRoot = Account Trie Root
//   - Normal account.Root = Storage Trie Root
//   - Native account.Root = Keccak256(shard0Root || shard1Root || ... || shard255Root)
//
// Note: Native token account shards are loaded lazily on access
type ShardedSnapshot struct {
	*Snapshot             // Embedded Snapshot for normal account storage
	globalRoot types.Hash // Cached global root (= Account Trie Root)
}

// decodeStorageValue decodes RLP-encoded storage value to Hash
func decodeStorageValue(val []byte) types.Hash {
	p := &fastrlp.Parser{}
	v, err := p.Parse(val)
	if err != nil {
		return types.Hash{}
	}
	res := []byte{}
	if res, err = v.GetBytes(res[:0]); err != nil {
		return types.Hash{}
	}
	return types.BytesToHash(res)
}

// ============================================================================
// Constructors
// ============================================================================

// NewShardedSnapshotImpl creates a new empty sharded snapshot.
// Used for genesis block or when creating a fresh state.
//
// Returns:
//   - A new ShardedSnapshot with empty Account Trie and no native token account shards
func NewShardedSnapshotImpl(s *State) *ShardedSnapshot {
	return &ShardedSnapshot{
		Snapshot: &Snapshot{state: s, trie: s.newTrie()},
	}
}

// NewShardedSnapshotAtImpl creates a sharded snapshot at a given root.
// Used for loading state at a specific block height.
//
// Parameters:
//   - s: State object providing storage access
//   - globalRoot: the state root to load (= Account Trie Root)
//
// Returns:
//   - ShardedSnapshot loaded from the given root
//   - Error if the Account Trie cannot be loaded
//
// Note: Native Token account shards are loaded lazily on first access
func NewShardedSnapshotAtImpl(s *State, globalRoot types.Hash) (*ShardedSnapshot, error) {
	if globalRoot == types.ZeroHash || globalRoot == emptyStateHash {
		return NewShardedSnapshotImpl(s), nil
	}

	// globalRoot = accountRoot (Account Trie root, same as Ethereum)
	t, err := s.newTrieAt(globalRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to load account trie: %w", err)
	}

	snap := &ShardedSnapshot{
		Snapshot:   &Snapshot{state: s, trie: t},
		globalRoot: globalRoot,
	}

	return snap, nil
}

// ============================================================================
// Read Methods
// ============================================================================

// IsNativeTokenAccount checks if an address is a native token account.
// Native Token accounts use 256 storage shards instead of a single Storage Trie.
func (s *ShardedSnapshot) IsNativeTokenAccount(addr types.Address) bool {
	// TODO: hamsa ptoken
	// nativetoken.NewNativeAddressCache().IsNativeToken(addr)

	return false
}

// GetStorage returns storage value for the given account and key.
// This is the main entry point for reading storage values.
//
// Routing:
//   - Native Token accounts: read from account's 256 storage shards
//   - Normal accounts: read from per-account Storage Trie (via embedded Snapshot)
//
// Parameters:
//   - addr: account address
//   - root: storage root (used by normal accounts, ignored by native token accounts)
//   - rawkey: the storage key to read
//
// Returns:
//   - The storage value as a Hash (zero hash if not found)
func (s *ShardedSnapshot) GetStorage(addr types.Address, root types.Hash, rawkey types.Hash) types.Hash {
	if s.IsNativeTokenAccount(addr) {
		return s.getNativeTokenAccountStorage(addr, root, rawkey)
	}
	// Normal accounts use embedded Snapshot.GetStorage
	return s.Snapshot.GetStorage(addr, root, rawkey)
}

// GetStorageRaw returns raw storage value (arbitrary length) for native token accounts.
// This is used for single-slot TokenEntity storage optimization.
func (s *ShardedSnapshot) GetStorageRaw(addr types.Address, root types.Hash, rawkey types.Hash) []byte {
	if !s.IsNativeTokenAccount(addr) {
		// Normal accounts: return 32-byte value as slice
		hash := s.Snapshot.GetStorage(addr, root, rawkey)
		if hash == types.ZeroHash {
			return nil
		}
		return hash.Bytes()
	}

	// Native token account: get raw value from shard
	key := crypto.Keccak256(rawkey.Bytes())
	shardID := s.shardId(key)

	shardTrie, err := s.getShardTrie(root, shardID)
	if err != nil {
		return nil
	}
	if shardTrie == nil {
		return nil
	}

	val, ok := shardTrie.Get(key, s.state.storage)
	if !ok {
		return nil
	}

	// Decode RLP-wrapped bytes
	p := &fastrlp.Parser{}
	v, err := p.Parse(val)
	if err != nil {
		return nil
	}
	res, err := v.GetBytes(nil)
	if err != nil {
		return nil
	}
	return res
}

// getNativeTokenAccountStorage reads data from a native token account's 256 storage shards.
//
// Sharding Strategy:
//   - shardID = rawkey[0], i.e., the first byte of the storage key (0-255)
//   - This distributes storage data evenly across 256 shards
//
// Lazy Loading:
//   - Only loads the required shard Trie (not all 256)
//   - Shard roots are LRU cached (State.shardRootsCache)
func (s *ShardedSnapshot) getNativeTokenAccountStorage(addr types.Address, accountRoot types.Hash, rawkey types.Hash) types.Hash {
	// Determine shard ID by first byte of storage key (0-255)
	key := crypto.Keccak256(rawkey.Bytes())
	shardID := s.shardId(key)

	// Lazy load: only load the required shard Trie
	shardTrie, err := s.getShardTrie(accountRoot, shardID)
	if err != nil {
		// Log error but return empty hash (don't panic)
		// This indicates potential data corruption that needs investigation
		panic(fmt.Sprintf("CRITICAL: failed to load shard trie for addr %s, shardID %d: %v", addr.String(), shardID, err))
	}
	if shardTrie == nil {
		return types.Hash{}
	}

	// Hash the storage key for Trie lookup
	val, ok := shardTrie.Get(key, s.state.storage)
	if !ok {
		return types.Hash{}
	}

	return decodeStorageValue(val)
}

// loadNativeTokenAccountShardRoots loads 256 shard roots for a native token account.
// Shard roots are LRU cached (State.shardRootsCache) to avoid repeated DB reads.
func (s *ShardedSnapshot) loadNativeTokenAccountShardRoots(accountRoot types.Hash) [DefaultShardCount]types.Hash {
	var roots [DefaultShardCount]types.Hash
	roots, _ = s.state.loadShardRoots(accountRoot)
	return roots
}

// getShardTrie loads a single shard Trie by shardID (lazy loading).
//
// Loading Process:
//  1. Load 256 shard roots from DB (8KB, cached)
//  2. Only load the shard Trie corresponding to shardID
//
// Note: Does not load all 256 shard Tries, only the required one.
// Returns error if shard trie loading fails.
func (s *ShardedSnapshot) getShardTrie(accountRoot types.Hash, shardID int) (*Trie, error) {
	// Only load the specified shard Trie (key = accountRoot for version isolation)
	t, err := s.state.loadOrCreateShardedTrieAt(accountRoot, []int{shardID})
	if err != nil {
		return nil, err
	}
	return t.shards[shardID], nil
}

// GetAccount and GetCode are inherited from embedded Snapshot

// ============================================================================
// Commit Types
// ============================================================================

// normalAccountTask holds data for parallel normal account storage commit.
// Each normal account's Storage Trie is committed independently in parallel.
type normalAccountTask struct {
	obj     *state.Object // Account object with storage changes
	oldRoot types.Hash    // Previous Storage Trie root
}

// normalAccountResult holds the result of normal account storage commit.
type normalAccountResult struct {
	addr    types.Address // Account address
	newRoot types.Hash    // New Storage Trie root after commit
	newTrie *Trie         // New Storage Trie (for cache update after DB write)
	err     error         // Error if commit failed
}

// nativeTokenAccountTask holds data for native token account storage commit.
// Each native token account's 256 shards are committed in parallel.
type nativeTokenAccountTask struct {
	addr            types.Address                             // Account address
	oldAccountRoot  types.Hash                                // Previous account storage root (versioned key)
	entries         [DefaultShardCount][]*state.StorageObject // Storage changes grouped by shard
	changedShardIDs []int                                     // Shard IDs with changes (avoid looping 256 times)
}

// nativeTokenAccountResult holds the result of native token account storage commit.
type nativeTokenAccountResult struct {
	addr           types.Address                 // Account address
	newAccountRoot types.Hash                    // New account storage root (versioned key for DB/cache)
	shardRoots     [DefaultShardCount]types.Hash // New roots for all 256 shards
	shardTries     [DefaultShardCount]*Trie      // New shard Tries (for cache update after DB write)
	changedSIDs    []int                         // Shard IDs that were changed (for cache update)
	err            error                         // Error if commit failed
}

// ============================================================================
// Commit Method
// ============================================================================

// Commit implements hybrid commit for state changes.
// This is the main entry point for persisting state changes.
//
// Commit Process:
//  1. classifyObjects: separate native token accounts and normal accounts
//  2. parallelCommitStorage: commit storage in parallel
//     - Native Token accounts: each account's shards committed in parallel (only changed shards)
//     - Normal accounts: each account's Storage Trie committed in parallel
//  3. collectCommitResults: build root map and collect all batches
//  4. updateAccountTrie: insert all account data with new storage roots
//  5. finalizeCommit: commit account trie, store shard roots, write batches to DB
//
// Returns:
//   - New ShardedSnapshot with updated state
//   - New globalRoot (= Account Trie Root)
//   - Error if commit failed
func (s *ShardedSnapshot) Commit(objs []*state.Object) (state.Snapshot, []byte, error) {
	if len(objs) == 0 {
		// Reset metrics for empty block
		metrics.SetGauge([]string{"sharded_snapshot", "merkle_duration_ms"}, 0)
		metrics.SetGauge([]string{"sharded_snapshot", "db_write_duration_ms"}, 0)
		metrics.SetGauge([]string{"sharded_snapshot", "commit_objs_count"}, 0)
		return s, s.globalRoot[:], nil
	}
	metrics.SetGauge([]string{"sharded_snapshot", "commit_objs_count"}, float32(len(objs)))

	merkleDurationStart := time.Now()
	rawBatch := s.state.storage.Batch()
	// Wrap batch for thread-safe concurrent writes
	sharedBatch := NewConcurrentBatch(rawBatch)
	tt := s.trie.Txn(s.state.storage)
	tt.batch = sharedBatch

	// 1. Classify objects into native token accounts and normal accounts
	nativeTokenTasks, normalTasks := s.classifyObjects(objs, tt, sharedBatch)

	// 2. Parallel commit storage for all accounts (using shared batch)
	nativeTokenResults, normalResults := s.parallelCommitStorage(nativeTokenTasks, normalTasks, sharedBatch)

	// 3. Collect results (no longer collecting batches)
	normalRootMap, err := s.collectCommitResults(normalResults, nativeTokenResults)
	if err != nil {
		return nil, nil, err
	}

	// 4. Update account trie with new storage roots
	s.updateAccountTrie(objs, tt, nativeTokenResults, normalRootMap)

	// 5. Finalize commit (single batch write, then cache update)
	return s.finalizeCommit(tt, nativeTokenResults, normalResults, sharedBatch, merkleDurationStart)
}

func (s *ShardedSnapshot) shardId(hash []byte) int {
	return int(hash[0])
	//return int(binary.LittleEndian.Uint64(hash) % DefaultShardCount)
}

// classifyObjects separates objects into native token accounts and normal accounts.
// Also handles deleted accounts and dirty code.
func (s *ShardedSnapshot) classifyObjects(
	objs []*state.Object,
	tt *Txn,
	batch Batch,
) (map[types.Address]*nativeTokenAccountTask, []normalAccountTask) {
	nativeTokenTasks := make(map[types.Address]*nativeTokenAccountTask)
	var normalTasks []normalAccountTask

	for _, obj := range objs {
		if obj.Deleted {
			tt.Delete(hashit(obj.Address.Bytes()))
			continue
		}

		if len(obj.Storage) != 0 {
			if s.IsNativeTokenAccount(obj.Address) {
				// Native token account: group storage changes by shard
				task, ok := nativeTokenTasks[obj.Address]
				if !ok {
					task = &nativeTokenAccountTask{addr: obj.Address, oldAccountRoot: obj.Root}
					nativeTokenTasks[obj.Address] = task
				}
				for _, entry := range obj.Storage {
					key := crypto.Keccak256(entry.Key)
					shardID := s.shardId(key)

					if len(task.entries[shardID]) == 0 {
						// First data added to this shard, record shard ID
						task.changedShardIDs = append(task.changedShardIDs, shardID)
					}
					task.entries[shardID] = append(task.entries[shardID], entry)
				}
			} else {
				// Normal account: collect for parallel commit
				normalTasks = append(normalTasks, normalAccountTask{obj: obj, oldRoot: obj.Root})
			}
		}

		if obj.DirtyCode {
			batch.Put(GetCodeKey(obj.CodeHash), obj.Code)
		}
	}

	return nativeTokenTasks, normalTasks
}

// parallelCommitStorage commits storage for all accounts in parallel.
// Returns results for both native token accounts and normal accounts.
// All writes go to the shared batch for single DB write at the end.
func (s *ShardedSnapshot) parallelCommitStorage(
	nativeTokenTasks map[types.Address]*nativeTokenAccountTask,
	normalTasks []normalAccountTask,
	sharedBatch Batch,
) (map[types.Address]*nativeTokenAccountResult, []normalAccountResult) {
	nativeTokenResults := make(map[types.Address]*nativeTokenAccountResult)
	normalResults := make([]normalAccountResult, len(normalTasks))
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Parallel native token account storage
	for _, task := range nativeTokenTasks {
		wg.Add(1)
		go func(t *nativeTokenAccountTask) {
			defer wg.Done()
			result := s.commitNativeTokenAccountShards(t, sharedBatch)
			mu.Lock()
			nativeTokenResults[t.addr] = &result
			mu.Unlock()
		}(task)
	}

	// Parallel normal account storage
	for i, task := range normalTasks {
		wg.Add(1)
		go func(idx int, t normalAccountTask) {
			defer wg.Done()
			normalResults[idx] = s.commitNormalAccountStorage(t, sharedBatch)
		}(i, task)
	}

	wg.Wait()
	return nativeTokenResults, normalResults
}

// collectCommitResults builds root map from commit results.
// Returns error if any account storage commit failed.
// Note: No longer collects batches as all writes go to shared batch.
func (s *ShardedSnapshot) collectCommitResults(
	normalResults []normalAccountResult,
	nativeTokenResults map[types.Address]*nativeTokenAccountResult,
) (map[types.Address]types.Hash, error) {
	// Build normal account root map
	normalRootMap := make(map[types.Address]types.Hash)
	for _, res := range normalResults {
		if res.err != nil {
			return nil, fmt.Errorf("normal account %s storage error: %w", res.addr.String(), res.err)
		}
		normalRootMap[res.addr] = res.newRoot
	}

	// Check for native token account errors
	for addr, result := range nativeTokenResults {
		if result.err != nil {
			return nil, fmt.Errorf("native token account %s storage error: %w", addr.String(), result.err)
		}
	}

	return normalRootMap, nil
}

// updateAccountTrie inserts all accounts into account trie with updated storage roots.
func (s *ShardedSnapshot) updateAccountTrie(
	objs []*state.Object,
	tt *Txn,
	nativeTokenResults map[types.Address]*nativeTokenAccountResult,
	normalRootMap map[types.Address]types.Hash,
) {
	arena := stateArenaPool.Get()
	defer stateArenaPool.Put(arena)

	for _, obj := range objs {
		if obj.Deleted {
			continue
		}

		account := state.Account{
			Balance:  obj.Balance,
			Nonce:    obj.Nonce,
			CodeHash: obj.CodeHash.Bytes(),
			Root:     obj.Root,
		}

		if s.IsNativeTokenAccount(obj.Address) {
			// Native Token account root = aggregated hash of 256 shard roots
			if result, ok := nativeTokenResults[obj.Address]; ok {
				account.Root = s.calculateShardRoot(result.shardRoots[:])
				result.newAccountRoot = account.Root
			}
		} else if newRoot, ok := normalRootMap[obj.Address]; ok {
			account.Root = newRoot
		}

		vv := account.MarshalWith(arena)
		data := vv.MarshalTo(nil)
		tt.Insert(hashit(obj.Address.Bytes()), data)
		arena.Reset()
	}
}

// finalizeCommit commits account trie, stores shard roots, and writes single shared batch to DB.
// IMPORTANT: Cache updates happen AFTER DB write to prevent race conditions.
func (s *ShardedSnapshot) finalizeCommit(
	tt *Txn,
	nativeTokenResults map[types.Address]*nativeTokenAccountResult,
	normalResults []normalAccountResult,
	sharedBatch Batch,
	merkleDurationStart time.Time,
) (state.Snapshot, []byte, error) {
	// Commit account trie (compute hash and get new trie, but don't cache yet)
	accountRoot, err := tt.Hash()
	if err != nil {
		return nil, types.ZeroHash[:], fmt.Errorf("sharded commit can not retrieve hash: %w", err)
	}
	newAccountTrie := tt.Commit()
	// Don't update cache here! Will be updated after DB write

	// globalRoot = accountRoot (Account Trie root)
	globalRoot := types.BytesToHash(accountRoot)

	// Write shard roots to batch (without updating cache)
	s.writeShardRootsToBatch(nativeTokenResults, sharedBatch)
	merkleDuration := time.Since(merkleDurationStart)
	metrics.SetGauge([]string{"sharded_snapshot", "merkle_duration_ms"}, float32(merkleDuration.Milliseconds()))

	// Write single shared batch (all operations collected in one batch)
	dbWriteStart := time.Now()
	if err := sharedBatch.Write(); err != nil {
		return nil, types.ZeroHash[:], fmt.Errorf("sharded commit batch write error: %w", err)
	}
	metrics.SetGauge([]string{"sharded_snapshot", "db_write_duration_ms"}, float32(time.Since(dbWriteStart).Milliseconds()))

	// Update all caches AFTER DB write is complete (critical for consistency)
	s.updateCachesAfterDBWrite(accountRoot, newAccountTrie, nativeTokenResults, normalResults)

	newSnap := &ShardedSnapshot{
		Snapshot:   &Snapshot{state: s.state, trie: newAccountTrie},
		globalRoot: globalRoot,
	}

	return newSnap, accountRoot, nil
}

// ============================================================================
// Commit Helper Methods
// ============================================================================

// commitNativeTokenAccountShards commits all 256 shards for one native token account in parallel.
// This is called for each native token account during Commit.
//
// Parallel Strategy:
//   - Each shard with changes is committed in a separate goroutine
//   - Shards without changes keep their existing Trie and root
//   - All writes go to shared batch for single DB write at the end
//
// Returns:
//   - nativeTokenAccountResult containing new shard roots
func (s *ShardedSnapshot) commitNativeTokenAccountShards(task *nativeTokenAccountTask, sharedBatch Batch) nativeTokenAccountResult {
	result := nativeTokenAccountResult{addr: task.addr, changedSIDs: task.changedShardIDs}

	// If no changes, return directly (no need to load shard roots)
	if len(task.changedShardIDs) == 0 {
		return result
	}

	// Load 256 shard roots using oldAccountRoot (versioned key for isolation)
	existingRoots := s.loadNativeTokenAccountShardRoots(task.oldAccountRoot)

	// Copy all existing roots first, then only process shards with changes
	result.shardRoots = existingRoots

	var wg sync.WaitGroup
	var mu sync.Mutex

	// Only iterate shards with changes (using changedShardIDs recorded during collection, no longer loop 256 times)
	for _, shardID := range task.changedShardIDs {
		wg.Add(1)
		go func(sid int, entries []*state.StorageObject, existingRoot types.Hash) {
			defer wg.Done()

			// Lazy load only the shard that has changes
			var trie *Trie
			if existingRoot == types.ZeroHash || existingRoot == emptyStateHash {
				trie = s.state.newTrie()
			} else {
				t, err := s.state.newTrieAt(existingRoot)
				if err != nil {
					// CRITICAL: Do not silently create empty trie!
					// This would cause data loss by overwriting existing data.
					mu.Lock()
					result.err = fmt.Errorf("failed to load existing shard %d trie with root %s: %w", sid, existingRoot.String(), err)
					mu.Unlock()
					return
				}
				trie = t
			}

			// Use shared batch for all writes
			txn := trie.Txn(s.state.storage)
			txn.batch = sharedBatch

			arena := stateArenaPool.Get()
			defer stateArenaPool.Put(arena)

			for _, entry := range entries {
				k := hashit(entry.Key)
				if entry.Deleted {
					txn.Delete(k)
				} else {
					vv := arena.NewBytes(bytes.TrimLeft(entry.Val, "\x00"))
					txn.Insert(k, vv.MarshalTo(nil))
				}
				arena.Reset()
			}

			newRoot, err := txn.Hash()
			if err != nil {
				mu.Lock()
				result.err = err
				mu.Unlock()
				return
			}

			newTrie := txn.Commit()
			// Don't update cache here! Store trie for later cache update after DB write
			mu.Lock()
			result.shardRoots[sid] = types.BytesToHash(newRoot)
			result.shardTries[sid] = newTrie
			mu.Unlock()
		}(shardID, task.entries[shardID], existingRoots[shardID])
	}

	wg.Wait()
	return result
}

// commitNormalAccountStorage commits storage for a normal account.
// This is called for each normal account during Commit (in parallel).
//
// Process:
//  1. Load existing Storage Trie from oldRoot
//  2. Apply all storage changes (insert/delete)
//  3. Calculate new root and commit Trie
//  4. All writes go to shared batch
func (s *ShardedSnapshot) commitNormalAccountStorage(task normalAccountTask, sharedBatch Batch) normalAccountResult {
	trie, err := s.state.newTrieAt(task.oldRoot)
	if err != nil {
		return normalAccountResult{addr: task.obj.Address, err: err}
	}

	// Use shared batch for all writes
	localTxn := trie.Txn(s.state.storage)
	localTxn.batch = sharedBatch

	arena := stateArenaPool.Get()
	defer stateArenaPool.Put(arena)

	for _, entry := range task.obj.Storage {
		k := hashit(entry.Key)
		if entry.Deleted {
			localTxn.Delete(k)
		} else {
			vv := arena.NewBytes(bytes.TrimLeft(entry.Val, "\x00"))
			localTxn.Insert(k, vv.MarshalTo(nil))
		}
		arena.Reset()
	}

	accountStateRoot, err := localTxn.Hash()
	if err != nil {
		return normalAccountResult{addr: task.obj.Address, err: err}
	}

	accountStateTrie := localTxn.Commit()
	// Don't update cache here! Store trie for later cache update after DB write

	return normalAccountResult{
		addr:    task.obj.Address,
		newRoot: types.BytesToHash(accountStateRoot),
		newTrie: accountStateTrie,
	}
}

// ============================================================================
// Utility Methods
// ============================================================================

// calculateShardRoot calculates aggregated root from 256 shard roots.
// Used to compute native token account's account.Root.
//
// Formula:
//
//	account.Root = Keccak256(shard0Root || shard1Root || ... || shard255Root)
//
// Note: 256 shard roots are concatenated in order (0-255), no separator.
func (s *ShardedSnapshot) calculateShardRoot(roots []types.Hash) types.Hash {
	data := make([]byte, len(roots)*32)
	for i, root := range roots {
		copy(data[i*32:(i+1)*32], root[:])
	}
	return types.BytesToHash(crypto.Keccak256(data))
}

// writeShardRootsToBatch writes shard root mappings to shared batch (without updating cache).
// Cache update happens after DB write in updateCachesAfterDBWrite.
//
// Storage Format:
//   - Key: "shard_" + account address (fixed key, overwritten on each commit, no historical accumulation)
//   - Value: 256 × 32 bytes = 8192 bytes (all shard roots concatenated)
func (s *ShardedSnapshot) writeShardRootsToBatch(hotResults map[types.Address]*nativeTokenAccountResult, sharedBatch Batch) {
	var newKeys [][]byte

	for _, result := range hotResults {
		// Key: "shard_" + newAccountRoot (versioned key for isolation)
		key := append([]byte("shard_"), result.newAccountRoot[:]...)
		data := make([]byte, DefaultShardCount*32)
		for i, root := range result.shardRoots {
			copy(data[i*32:(i+1)*32], root[:])
		}
		sharedBatch.Put(key, data)

		keyCopy := make([]byte, len(key))
		copy(keyCopy, key)
		newKeys = append(newKeys, keyCopy)
	}

	// Async cleanup of old shard root entries (entire cleanup runs in goroutine to avoid blocking Commit)
	if len(newKeys) > 0 {
		go s.state.cleanupOldShardRootKeys(newKeys)
	}
}

// updateCachesAfterDBWrite updates all caches after DB write is complete.
// This ensures cache is only updated after data is persisted to DB.
func (s *ShardedSnapshot) updateCachesAfterDBWrite(
	accountRoot []byte,
	newAccountTrie *Trie,
	nativeTokenResults map[types.Address]*nativeTokenAccountResult,
	normalResults []normalAccountResult,
) {
	// Update account trie cache
	s.state.AddState(types.BytesToHash(accountRoot), newAccountTrie)

	// Update native token account shard caches
	for _, result := range nativeTokenResults {
		// Update shard roots cache (key = newAccountRoot for version isolation)
		s.state.shardRootsCache.Add(result.newAccountRoot, result.shardRoots)

		// Update shard trie caches (only for changed shards)
		for _, sid := range result.changedSIDs {
			if result.shardTries[sid] != nil {
				s.state.AddState(result.shardRoots[sid], result.shardTries[sid])
			}
		}
	}

	// Update normal account storage trie caches
	for _, result := range normalResults {
		if result.newTrie != nil {
			s.state.AddState(result.newRoot, result.newTrie)
		}
	}
}

// ============================================================================
// State Helper Methods
// ============================================================================

// loadNativeTokenAccountShardRoots loads 256 shard roots from cache or DB.
// Uses shardRootsCache with accountRoot as key for version isolation.
func (s *State) loadNativeTokenAccountShardRoots(accountRoot types.Hash) ([DefaultShardCount]types.Hash, error) {
	var roots [DefaultShardCount]types.Hash

	// Check cache first (using accountRoot as cache key for version isolation)
	if cached, ok := s.shardRootsCache.Get(accountRoot); ok {
		return cached.([DefaultShardCount]types.Hash), nil
	}

	// Load from DB (key = "shard_" + accountRoot for version isolation)
	key := append([]byte("shard_"), accountRoot[:]...)
	data, ok, _ := s.storage.Get(key)
	if !ok {
		return roots, fmt.Errorf("native token account shard roots not found for accountRoot %s", accountRoot.String())
	}

	if len(data) != DefaultShardCount*32 {
		return roots, fmt.Errorf("invalid native token account shard roots data length: %d", len(data))
	}

	for i := 0; i < DefaultShardCount; i++ {
		copy(roots[i][:], data[i*32:(i+1)*32])
	}

	// Cache the result (use accountRoot as cache key)
	s.shardRootsCache.Add(accountRoot, roots)

	return roots, nil
}
