package state

import (
	"math/big"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
)

// codeReadCounter is a minimal `readSnapshot` that records every
// `GetCode(hash)` it serves. We use it instead of a real Pebble snapshot so
// tests can assert that the global cache actually short-circuits storage on
// cross-`Txn` reuse, and that DirtyCode never reaches the snapshot.
type codeReadCounter struct {
	mu       sync.Mutex
	accounts map[types.Address]*Account
	codes    map[types.Hash][]byte
	reads    map[types.Hash]int
}

func newCodeReadCounter() *codeReadCounter {
	return &codeReadCounter{
		accounts: make(map[types.Address]*Account),
		codes:    make(map[types.Hash][]byte),
		reads:    make(map[types.Hash]int),
	}
}

func (s *codeReadCounter) putContract(addr types.Address, code []byte) types.Hash {
	hash := types.BytesToHash(crypto.Keccak256(code))

	s.mu.Lock()
	defer s.mu.Unlock()

	s.accounts[addr] = &Account{
		Balance:  big.NewInt(0),
		CodeHash: hash.Bytes(),
		Root:     emptyStateHash,
	}
	s.codes[hash] = code

	return hash
}

func (s *codeReadCounter) readsFor(hash types.Hash) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.reads[hash]
}

func (s *codeReadCounter) GetStorage(_ types.Address, _ types.Hash, _ types.Hash) types.Hash {
	return types.Hash{}
}

func (s *codeReadCounter) GetAccount(addr types.Address) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	acc, ok := s.accounts[addr]
	if !ok {
		return nil, nil
	}

	return acc.Copy(), nil
}

func (s *codeReadCounter) GetCode(hash types.Hash) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	code, ok := s.codes[hash]
	if !ok {
		return nil, false
	}

	s.reads[hash]++

	out := make([]byte, len(code))
	copy(out, code)

	return out, true
}

func (s *codeReadCounter) GetRootHash() types.Hash {
	return types.Hash{}
}

// withFreshGlobalCodeCache reinstalls a clean cache for the duration of the
// test and restores the production default at teardown so individual test
// cases can never pollute each other's stats.
func withFreshGlobalCodeCache(t testing.TB, size int) {
	t.Helper()

	t.Cleanup(func() {
		SetGlobalCodeCacheSize(DefaultGlobalCodeCacheSize)
		PurgeGlobalCodeCache()
	})

	SetGlobalCodeCacheSize(size)
	PurgeGlobalCodeCache()
}

func makeContractCode(size int, seed byte) []byte {
	code := make([]byte, size)
	for i := range code {
		code[i] = seed + byte(i)
	}

	return code
}

func TestGlobalCodeCache_CrossTxnReuseAvoidsStorage(t *testing.T) {
	withFreshGlobalCodeCache(t, 64)

	snap := newCodeReadCounter()
	addr := types.StringToAddress("0xabc")
	code := makeContractCode(50*1024, 0x10)
	hash := snap.putContract(addr, code)

	// First Transition: cold global cache, must hit storage exactly once.
	got := newTxn(snap).GetCode(addr)
	require.Equal(t, code, got, "first GetCode must return the deployed bytes")
	require.Equal(t, 1, snap.readsFor(hash), "first GetCode must do exactly one snapshot read")

	hits, misses, adds, _, _, enabled := GlobalCodeCacheStats()
	require.True(t, enabled)
	assert.Equal(t, uint64(0), hits, "first lookup is a cold miss, not a hit")
	assert.Equal(t, uint64(1), misses)
	assert.Equal(t, uint64(1), adds, "miss path must publish to the global cache")

	// Second Transition (fresh Txn, fresh per-Txn LRU): global cache MUST
	// short-circuit Pebble.
	got = newTxn(snap).GetCode(addr)
	require.Equal(t, code, got, "second GetCode must return the same bytes")
	assert.Equal(t, 1, snap.readsFor(hash),
		"global cache must avoid a second snapshot read across Txn instances")

	hits, misses, _, _, _, _ = GlobalCodeCacheStats()
	assert.Equal(t, uint64(1), hits, "second lookup must hit the global cache")
	assert.Equal(t, uint64(1), misses, "miss count must not change on a hit")
}

func TestGlobalCodeCache_SharedCodeHashAcrossAddresses(t *testing.T) {
	// Two distinct addresses pointing at the same content-addressed body —
	// e.g. minimal-proxy clones — must share a single global cache entry
	// and avoid a second storage read on the second address.
	withFreshGlobalCodeCache(t, 64)

	snap := newCodeReadCounter()
	addrA := types.StringToAddress("0xa")
	addrB := types.StringToAddress("0xb")
	code := makeContractCode(8*1024, 0x42)

	hashA := snap.putContract(addrA, code)
	hashB := snap.putContract(addrB, code)
	require.Equal(t, hashA, hashB, "test setup: clones must share a code hash")

	_ = newTxn(snap).GetCode(addrA)
	_ = newTxn(snap).GetCode(addrB)

	assert.Equal(t, 1, snap.readsFor(hashA),
		"second address with same code hash must hit the global cache")
	assert.Equal(t, 1, GlobalCodeCacheLen(),
		"identical code at distinct addresses must collapse to one entry")
}

func TestGlobalCodeCache_DistinctCodeHashesDoNotCollide(t *testing.T) {
	withFreshGlobalCodeCache(t, 64)

	snap := newCodeReadCounter()
	addrA := types.StringToAddress("0xa")
	addrB := types.StringToAddress("0xb")
	codeA := makeContractCode(2048, 0x11)
	codeB := makeContractCode(2048, 0x22)

	hashA := snap.putContract(addrA, codeA)
	hashB := snap.putContract(addrB, codeB)
	require.NotEqual(t, hashA, hashB, "test setup: distinct code must have distinct hashes")

	gotA := newTxn(snap).GetCode(addrA)
	gotB := newTxn(snap).GetCode(addrB)

	assert.Equal(t, codeA, gotA)
	assert.Equal(t, codeB, gotB)
	assert.Equal(t, 1, snap.readsFor(hashA))
	assert.Equal(t, 1, snap.readsFor(hashB))
	assert.Equal(t, 2, GlobalCodeCacheLen(),
		"different code hashes must occupy separate cache entries")
}

func TestGlobalCodeCache_DirtyCodeBypassesGlobalCache(t *testing.T) {
	// Code created (or overridden) inside the current transition is
	// transition-local; it must not leak into the process-wide cache.
	withFreshGlobalCodeCache(t, 64)

	snap := newCodeReadCounter()
	addr := types.StringToAddress("0xdeadbeef")
	overrideCode := makeContractCode(1024, 0x55)

	txn := newTxn(snap)
	txn.SetCode(addr, overrideCode)

	got := txn.GetCode(addr)
	assert.Equal(t, overrideCode, got, "DirtyCode must return the in-memory bytes")

	hits, misses, adds, _, bytes, _ := GlobalCodeCacheStats()
	assert.Zero(t, hits, "DirtyCode must not hit the global cache")
	assert.Zero(t, misses, "DirtyCode must not consult the global cache")
	assert.Zero(t, adds, "DirtyCode must not publish to the global cache")
	assert.Zero(t, bytes)
	assert.Zero(t, GlobalCodeCacheLen())
}

func TestGlobalCodeCache_DisabledFallsBackToColdReads(t *testing.T) {
	// Operators that pass `--global-code-cache-size 0` must see exactly the
	// pre-cache behavior: every fresh Txn pays for a snapshot read.
	withFreshGlobalCodeCache(t, 0)

	_, _, _, _, _, enabled := GlobalCodeCacheStats()
	require.False(t, enabled, "size 0 must report the cache as disabled")

	snap := newCodeReadCounter()
	addr := types.StringToAddress("0xc0de")
	code := makeContractCode(4096, 0x77)
	hash := snap.putContract(addr, code)

	for i := 0; i < 3; i++ {
		got := newTxn(snap).GetCode(addr)
		require.Equal(t, code, got)
	}

	assert.Equal(t, 3, snap.readsFor(hash),
		"disabled cache must let every fresh Txn re-read from storage")
	assert.Zero(t, GlobalCodeCacheLen())
}

func TestGlobalCodeCache_PerTxnLRUStillShortCircuitsGlobalLookup(t *testing.T) {
	// The per-Txn LRU is a serialization-free fast path within a single
	// Transition. We verify that once it warms up, repeated calls inside
	// the same Txn neither increment global cache stats nor touch storage.
	withFreshGlobalCodeCache(t, 64)

	snap := newCodeReadCounter()
	addr := types.StringToAddress("0xfeed")
	code := makeContractCode(2048, 0x99)
	hash := snap.putContract(addr, code)

	txn := newTxn(snap)

	require.Equal(t, code, txn.GetCode(addr))

	statsAfterFirst := func() (hits, misses, adds uint64) {
		h, m, a, _, _, _ := GlobalCodeCacheStats()

		return h, m, a
	}

	hits0, misses0, adds0 := statsAfterFirst()

	for i := 0; i < 5; i++ {
		require.Equal(t, code, txn.GetCode(addr))
	}

	hits1, misses1, adds1 := statsAfterFirst()
	assert.Equal(t, hits0, hits1, "per-Txn LRU hits must not show up as global cache hits")
	assert.Equal(t, misses0, misses1, "per-Txn LRU hits must not show up as global cache misses")
	assert.Equal(t, adds0, adds1, "per-Txn LRU hits must not publish to the global cache")
	assert.Equal(t, 1, snap.readsFor(hash),
		"per-Txn LRU hits must not reach the snapshot")
}

func TestGlobalCodeCache_ResizingDropsExistingEntries(t *testing.T) {
	withFreshGlobalCodeCache(t, 16)

	snap := newCodeReadCounter()
	addr := types.StringToAddress("0xa")
	code := makeContractCode(1024, 0x33)
	snap.putContract(addr, code)

	_ = newTxn(snap).GetCode(addr)
	require.Equal(t, 1, GlobalCodeCacheLen())

	SetGlobalCodeCacheSize(8)
	assert.Zero(t, GlobalCodeCacheLen(), "resizing must reset the cache contents")

	hits, misses, adds, _, bytes, enabled := GlobalCodeCacheStats()
	assert.True(t, enabled)
	assert.Zero(t, hits, "stats must reset along with the LRU")
	assert.Zero(t, misses)
	assert.Zero(t, adds)
	assert.Zero(t, bytes)
}

func TestGlobalCodeCache_LRUEvictionTrackedInStats(t *testing.T) {
	const capacity = 4

	withFreshGlobalCodeCache(t, capacity)

	snap := newCodeReadCounter()
	codes := make([][]byte, capacity+2)

	for i := range codes {
		addr := types.BytesToAddress([]byte{byte(i + 1)})
		codes[i] = makeContractCode(256, byte(0xa0+i))
		snap.putContract(addr, codes[i])

		_ = newTxn(snap).GetCode(addr)
	}

	assert.LessOrEqual(t, GlobalCodeCacheLen(), capacity, "LRU must not exceed capacity")

	_, _, _, evicts, _, _ := GlobalCodeCacheStats()
	assert.GreaterOrEqual(t, evicts, uint64(2),
		"inserting capacity+2 distinct entries must produce at least two evictions")
}

func TestGlobalCodeCache_EmptyCodeIsNotCached(t *testing.T) {
	// `EmptyCodeHash` is resolved by the snapshot layer without any Pebble
	// hit. Caching it would only consume LRU slots, so we filter it out at
	// the publish site.
	withFreshGlobalCodeCache(t, 64)

	storeCachedCode(types.EmptyCodeHash, []byte{})
	storeCachedCode(types.EmptyCodeHash, makeContractCode(32, 0xff))
	storeCachedCode(types.ZeroHash, makeContractCode(32, 0xff))

	hits, misses, adds, _, bytes, _ := GlobalCodeCacheStats()
	assert.Zero(t, hits)
	assert.Zero(t, misses)
	assert.Zero(t, adds, "sentinel-hash payloads must not publish to the cache")
	assert.Zero(t, bytes)
	assert.Zero(t, GlobalCodeCacheLen())
}

func TestGlobalCodeCache_BytesAccountingTracksAddsAndEvictions(t *testing.T) {
	const capacity = 2

	withFreshGlobalCodeCache(t, capacity)

	snap := newCodeReadCounter()

	addrA := types.BytesToAddress([]byte{0x01})
	addrB := types.BytesToAddress([]byte{0x02})
	addrC := types.BytesToAddress([]byte{0x03})

	codeA := makeContractCode(1024, 0x10)
	codeB := makeContractCode(2048, 0x20)
	codeC := makeContractCode(512, 0x30)

	snap.putContract(addrA, codeA)
	snap.putContract(addrB, codeB)
	snap.putContract(addrC, codeC)

	_ = newTxn(snap).GetCode(addrA)
	_ = newTxn(snap).GetCode(addrB)

	_, _, _, _, bytes, _ := GlobalCodeCacheStats()
	assert.Equal(t, int64(len(codeA)+len(codeB)), bytes,
		"bytes gauge must reflect the current resident set size")

	// Inserting C must evict A; bytes must shrink by len(codeA) and grow
	// by len(codeC).
	_ = newTxn(snap).GetCode(addrC)

	_, _, _, evicts, bytes, _ := GlobalCodeCacheStats()
	assert.Equal(t, int64(len(codeB)+len(codeC)), bytes,
		"bytes gauge must subtract evicted entries before adding new ones")
	assert.GreaterOrEqual(t, evicts, uint64(1))
}
