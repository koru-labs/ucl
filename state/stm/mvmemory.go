package stm

import (
	"sort"
	"sync"

	"github.com/0xPolygon/polygon-edge/state"
)

// numShards splits the key space to keep the top-level map lookup uncontended across workers;
// each individual key's version chain is additionally protected by its own mutex.
const numShards = 32

// mvVersion is one transaction's contribution to a key's version chain: either a real value
// (estimate == false) or a placeholder left behind while that transaction is being
// re-executed (estimate == true), which any reader must treat as a hard dependency block.
type mvVersion struct {
	txIndex     int
	incarnation int
	value       any
	estimate    bool
}

type mvKey struct {
	mu sync.Mutex
	// versions is sorted ascending by txIndex; at most one entry per txIndex.
	versions []mvVersion
}

func (k *mvKey) upsert(v mvVersion) {
	for i := range k.versions {
		if k.versions[i].txIndex == v.txIndex {
			k.versions[i] = v

			return
		}
	}

	idx := sort.Search(len(k.versions), func(i int) bool { return k.versions[i].txIndex >= v.txIndex })
	k.versions = append(k.versions, mvVersion{})
	copy(k.versions[idx+1:], k.versions[idx:])
	k.versions[idx] = v
}

func (k *mvKey) remove(txIndex int) {
	for i := range k.versions {
		if k.versions[i].txIndex == txIndex {
			k.versions = append(k.versions[:i], k.versions[i+1:]...)

			return
		}
	}
}

// MVMemory is one batch's multi-version memory: for each key, the ordered contributions of
// every transaction in the batch that has (speculatively) written it so far.
type MVMemory struct {
	shards [numShards]*shard
}

type shard struct {
	mu   sync.RWMutex
	data map[state.Key]*mvKey
}

func NewMVMemory() *MVMemory {
	mv := &MVMemory{}
	for i := range mv.shards {
		mv.shards[i] = &shard{data: map[state.Key]*mvKey{}}
	}

	return mv
}

func shardIndex(key state.Key) uint32 {
	var h uint32 = 2166136261

	for _, b := range key {
		h ^= uint32(b)
		h *= 16777619
	}

	return h % numShards
}

func (mv *MVMemory) get(key state.Key) *mvKey {
	s := mv.shards[shardIndex(key)]

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.data[key]
}

func (mv *MVMemory) getOrCreate(key state.Key) *mvKey {
	s := mv.shards[shardIndex(key)]

	s.mu.RLock()
	k, ok := s.data[key]
	s.mu.RUnlock()

	if ok {
		return k
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if k, ok = s.data[key]; ok {
		return k
	}

	k = &mvKey{}
	s.data[key] = k

	return k
}

// Read resolves a read of key performed by txIndex: the highest version installed by a
// strictly lower tx index, or found=false if none exists (caller must fall back to the
// batch's base state). isEstimate=true means the visible version is a placeholder left by an
// incarnation that is being re-executed - the caller must treat this as a hard dependency
// block, not a value.
func (mv *MVMemory) Read(key state.Key, txIndex int) (val any, foundTxIndex, foundIncarnation int, isEstimate, found bool) {
	k := mv.get(key)
	if k == nil {
		return nil, 0, 0, false, false
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	for i := len(k.versions) - 1; i >= 0; i-- {
		v := k.versions[i]
		if v.txIndex < txIndex {
			return v.value, v.txIndex, v.incarnation, v.estimate, true
		}
	}

	return nil, 0, 0, false, false
}

// Write installs txIndex's (incarnation's) value for key, replacing any earlier value it had
// previously installed for the same key.
func (mv *MVMemory) Write(key state.Key, txIndex, incarnation int, value any) {
	k := mv.getOrCreate(key)

	k.mu.Lock()
	defer k.mu.Unlock()

	k.upsert(mvVersion{txIndex: txIndex, incarnation: incarnation, value: value, estimate: false})
}

// Delete removes txIndex's entry for key entirely - used when a re-executed incarnation no
// longer writes a key its previous incarnation did.
func (mv *MVMemory) Delete(key state.Key, txIndex int) {
	k := mv.get(key)
	if k == nil {
		return
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	k.remove(txIndex)
}

// MarkEstimate flags txIndex's current entries for keys as placeholders: any reader that
// resolves to one of them must treat it as a hard dependency block rather than a value. Called
// the moment a validated incarnation is found stale, before it is discarded and re-executed,
// so nothing can observe its now-untrustworthy writes as if they were final.
func (mv *MVMemory) MarkEstimate(txIndex int, keys []state.Key) {
	for _, key := range keys {
		k := mv.getOrCreate(key)

		k.mu.Lock()
		k.upsert(mvVersion{txIndex: txIndex, incarnation: -1, value: nil, estimate: true})
		k.mu.Unlock()
	}
}

var _ state.MVMemoryAccess = (*MVMemory)(nil)
