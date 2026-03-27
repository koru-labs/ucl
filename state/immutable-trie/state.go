package itrie

import (
	"fmt"
	"sync"

	lru "github.com/hashicorp/golang-lru"

	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
)

// maxShardRootKeysKeep is the maximum number of historical shard root DB entries to keep.
// Commit() is called for both speculative (IBFT round failures) and finalized blocks,
// so this must be large enough to tolerate consecutive round failures.
// 10000 entries ≈ 80MB in DB, tolerates ~10000 consecutive round failures per native token account.
const maxShardRootKeysKeep = 10000

type State struct {
	storage            Storage
	cache              *lru.Cache
	shardRootsCache    *lru.Cache // Cache for shard roots (key = accountRoot for version isolation)
	oldShardRootKeys   [][]byte   // Ring buffer of historical shard root DB keys for cleanup
	oldShardRootKeysMu sync.Mutex // Protects oldShardRootKeys
}

func NewState(storage Storage) *State {
	// Trie cache: 1024 entries (~10MB), covers hot trie nodes across recent blocks
	cache, _ := lru.New(1024)
	// ShardRoots cache: 256 entries (~2MB), covers recent 256 blocks' shard root mappings
	shardRootsCache, _ := lru.New(256)

	s := &State{
		storage:         storage,
		cache:           cache,
		shardRootsCache: shardRootsCache,
	}

	return s
}

func (s *State) NewSnapshot() state.Snapshot {
	return &Snapshot{state: s, trie: s.newTrie()}
}

// NewShardedSnapshot creates a new sharded snapshot with 256 shards
func (s *State) NewShardedSnapshot() state.Snapshot {
	return NewShardedSnapshotImpl(s)
}

// NewShardedSnapshotAt creates a sharded snapshot from existing global root
func (s *State) NewShardedSnapshotAt(root types.Hash) (state.Snapshot, error) {
	return NewShardedSnapshotAtImpl(s, root)
}

func (s *State) NewSnapshotAt(root types.Hash) (state.Snapshot, error) {
	t, err := s.newTrieAt(root)
	if err != nil {
		return nil, err
	}

	return &Snapshot{state: s, trie: t}, nil
}

func (s *State) newTrie() *Trie {
	return NewTrie()
}

func (s *State) SetCode(hash types.Hash, code []byte) error {
	return s.storage.SetCode(hash, code)
}

func (s *State) GetCode(hash types.Hash) ([]byte, bool) {
	if hash == types.EmptyCodeHash {
		return []byte{}, true
	}

	return s.storage.GetCode(hash)
}

// newTrieAt returns trie with root and if necessary locks state on a trie level
func (s *State) newTrieAt(root types.Hash) (*Trie, error) {
	if root == types.EmptyRootHash {
		// empty state
		return s.newTrie(), nil
	}

	tt, ok := s.cache.Get(root)
	if ok {
		t, ok := tt.(*Trie)
		if !ok {
			return nil, fmt.Errorf("invalid type assertion on root: %s", root)
		}

		return t, nil
	}

	n, ok, err := GetNode(root.Bytes(), s.storage)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage root %s: %w", root, err)
	}

	if !ok {
		return nil, fmt.Errorf("state not found at hash %s", root)
	}

	t := &Trie{
		root: n,
	}

	return t, nil
}

// loadOrCreateShardedTrieAt loads specific shards for a native token account.
// Only loads the shards specified in shardIDs, others remain nil.
// Returns error if shard trie loading fails (instead of silently returning empty trie).
// accountRoot: the account's storage root, used as versioned key for shard roots.
func (s *State) loadOrCreateShardedTrieAt(accountRoot types.Hash, shardIDs []int) (*Trie, error) {
	t := &Trie{}

	// Load 256 shard roots from DB (key = accountRoot for version isolation)
	roots, err := s.loadNativeTokenAccountShardRoots(accountRoot)
	if err != nil {
		// New account, return empty trie with empty shards
		for _, sid := range shardIDs {
			t.shards[sid] = s.newTrie()
		}
		return t, nil
	}

	// Only load the specified shards
	for _, sid := range shardIDs {
		if roots[sid] == types.ZeroHash || roots[sid] == emptyStateHash {
			t.shards[sid] = s.newTrie()
		} else {
			shardTrie, err := s.newTrieAt(roots[sid])
			if err != nil {
				// CRITICAL: Do not silently return empty trie!
				// This indicates data corruption or missing trie nodes.
				return nil, fmt.Errorf("failed to load shard %d trie for accountRoot %s with shardRoot %s: %w",
					sid, accountRoot.String(), roots[sid].String(), err)
			}
			t.shards[sid] = shardTrie
		}
	}

	return t, nil
}

// loadShardRoots loads 256 shard roots from DB (without loading Tries).
// accountRoot: versioned key for shard roots lookup.
func (s *State) loadShardRoots(accountRoot types.Hash) ([DefaultShardCount]types.Hash, error) {
	return s.loadNativeTokenAccountShardRoots(accountRoot)
}

// cleanupOldShardRootKeys appends new keys to the ring buffer and asynchronously
// deletes old entries that exceed maxShardRootKeysKeep from DB.
func (s *State) cleanupOldShardRootKeys(newKeys [][]byte) {
	s.oldShardRootKeysMu.Lock()
	s.oldShardRootKeys = append(s.oldShardRootKeys, newKeys...)

	if len(s.oldShardRootKeys) <= maxShardRootKeysKeep {
		s.oldShardRootKeysMu.Unlock()
		return
	}

	// Collect keys to delete
	excess := len(s.oldShardRootKeys) - maxShardRootKeysKeep
	toDelete := make([][]byte, excess)
	copy(toDelete, s.oldShardRootKeys[:excess])
	s.oldShardRootKeys = s.oldShardRootKeys[excess:]
	s.oldShardRootKeysMu.Unlock()

	// Delete old entries (already running in goroutine from caller)
	for _, key := range toDelete {
		s.storage.Put(key, nil)
	}
}

func (s *State) AddState(root types.Hash, t *Trie) {
	s.cache.Add(root, t)
}
