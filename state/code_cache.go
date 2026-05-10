package state

import (
	"sync/atomic"

	"github.com/hashicorp/go-metrics"
	lru "github.com/hashicorp/golang-lru"

	"github.com/0xPolygon/polygon-edge/types"
)

// DefaultGlobalCodeCacheSize is the default number of distinct contract codes
// (keyed by code hash) for which the state layer will keep deployed bytecode
// in memory across `Txn` instances.
//
// The per-`Txn` LRU in `Txn.GetCode` only spans a single `Transition`, so
// JSON-RPC simulation paths (`eth_call`, `eth_estimateGas`'s binary search,
// etc.) that build a fresh `Transition` per attempt re-load the same large
// contract from Pebble every time without this cache.
//
// Memory budget at the default: a single entry costs roughly the deployed
// bytecode size. Worst case for a 24 KB contract is ~24 MiB; for a 50–60 KB
// contract it is closer to ~50–60 MiB. Operators can tune the value via
// `SetGlobalCodeCacheSize` (or the `--global-code-cache-size` CLI flag wired
// in `command/server`); 0 disables the cache.
const DefaultGlobalCodeCacheSize = 1024

// codeCacheT bundles the LRU together with its observability counters. The
// struct is swapped atomically as a single unit when the cache is
// reconfigured at runtime; cached values are immutable byte slices and can
// therefore be shared across goroutines without locking.
type codeCacheT struct {
	cache    *lru.Cache
	hits     atomic.Uint64
	misses   atomic.Uint64
	adds     atomic.Uint64
	evicts   atomic.Uint64
	curBytes atomic.Int64
}

// globalCodeCache is a process-wide cache of deployed contract bytecode keyed
// by the contract's code hash. Holding it as an atomic pointer lets us
// reconfigure (or disable) the cache at any time without taking a lock on the
// hot read path; in-flight `Txn.GetCode` calls keep a reference to the
// previous instance and finish safely.
//
// A nil pointer means "cache disabled".
var globalCodeCache atomic.Pointer[codeCacheT]

func init() {
	SetGlobalCodeCacheSize(DefaultGlobalCodeCacheSize)
}

// SetGlobalCodeCacheSize installs a new global contract code cache with the
// given capacity (number of distinct code hashes). Passing a value <= 0
// disables the cache entirely.
//
// It is safe to call at any time, including while transitions are in flight
// on other goroutines: any active reader is holding a pointer to the previous
// instance and will continue to use it until it returns.
//
// Resizing always discards previously cached entries; the cache will refill
// on subsequent contract calls.
func SetGlobalCodeCacheSize(size int) {
	if size <= 0 {
		globalCodeCache.Store(nil)

		return
	}

	c := &codeCacheT{}

	cache, err := lru.NewWithEvict(size, func(_, value interface{}) {
		buf, ok := value.([]byte)
		if !ok {
			return
		}

		c.evicts.Add(1)
		c.curBytes.Add(-int64(len(buf)))
		metrics.IncrCounter([]string{"state", "global_code_cache", "evict"}, 1)
		metrics.SetGauge([]string{"state", "global_code_cache", "bytes"},
			float32(c.curBytes.Load()))
	})
	if err != nil {
		// lru.NewWithEvict only fails when size <= 0, which we already
		// guarded against. Treat any unexpected failure as "disable cache"
		// rather than panicking from inside an unrelated state read.
		globalCodeCache.Store(nil)

		return
	}

	c.cache = cache
	globalCodeCache.Store(c)
}

// GlobalCodeCacheStats returns the cumulative counters since the last
// `SetGlobalCodeCacheSize` call. The `enabled` flag distinguishes
// "cache exists but has seen 0 lookups" (true) from "cache is disabled"
// (false), so observability dashboards can avoid plotting a meaningless 0%
// hit ratio against a disabled cache.
func GlobalCodeCacheStats() (hits, misses, adds, evicts uint64, bytes int64, enabled bool) {
	c := globalCodeCache.Load()
	if c == nil {
		return 0, 0, 0, 0, 0, false
	}

	return c.hits.Load(),
		c.misses.Load(),
		c.adds.Load(),
		c.evicts.Load(),
		c.curBytes.Load(),
		true
}

// GlobalCodeCacheLen returns the current number of distinct code hashes held
// by the cache, or 0 if the cache is disabled. Intended for monitoring and
// tests.
func GlobalCodeCacheLen() int {
	c := globalCodeCache.Load()
	if c == nil {
		return 0
	}

	return c.cache.Len()
}

// PurgeGlobalCodeCache drops every cached entry without changing the
// configured capacity and resets all counters. Mainly useful for benchmarks
// and tests.
func PurgeGlobalCodeCache() {
	c := globalCodeCache.Load()
	if c == nil {
		return
	}

	c.cache.Purge()
	c.hits.Store(0)
	c.misses.Store(0)
	c.adds.Store(0)
	c.evicts.Store(0)
	c.curBytes.Store(0)
	metrics.SetGauge([]string{"state", "global_code_cache", "bytes"}, 0)
}

// lookupCachedCode returns the cached bytecode for the given code hash if one
// is available, or `nil, false` otherwise. The returned slice MUST be treated
// as immutable by the caller — multiple goroutines may hold references to the
// same backing array concurrently.
func lookupCachedCode(hash types.Hash) ([]byte, bool) {
	c := globalCodeCache.Load()
	if c == nil {
		return nil, false
	}

	v, ok := c.cache.Get(hash)
	if !ok {
		c.misses.Add(1)
		metrics.IncrCounter([]string{"state", "global_code_cache", "miss"}, 1)

		return nil, false
	}

	c.hits.Add(1)
	metrics.IncrCounter([]string{"state", "global_code_cache", "hit"}, 1)

	buf, _ := v.([]byte)

	return buf, buf != nil
}

// storeCachedCode publishes a freshly loaded bytecode buffer under the given
// code hash. The buffer becomes immutable from the caller's perspective once
// this returns: any further mutation would race with concurrent
// `Txn.GetCode` callers that may already be reading the cached entry.
//
// Calls that target empty / unknown code hashes (`EmptyCodeHash`, `ZeroHash`)
// or that supply an empty buffer are silently ignored. Empty code is
// resolved without a storage hit by the snapshot layer
// (see `state/immutable-trie/state.go`), so caching it would only consume
// LRU slots without saving any work; ZeroHash never identifies a real
// contract on-chain.
func storeCachedCode(hash types.Hash, code []byte) {
	if !shouldCacheCode(hash, len(code)) {
		return
	}

	c := globalCodeCache.Load()
	if c == nil {
		return
	}

	c.cache.Add(hash, code)
	c.adds.Add(1)
	c.curBytes.Add(int64(len(code)))
	metrics.IncrCounter([]string{"state", "global_code_cache", "add"}, 1)
	metrics.SetGauge([]string{"state", "global_code_cache", "bytes"},
		float32(c.curBytes.Load()))
}

// shouldCacheCode mirrors the eligibility check used by the EVM JUMPDEST
// cache: only content-addressed, non-empty bytecode is worth caching, and we
// never publish under sentinel hashes that could collide across distinct
// contracts.
func shouldCacheCode(hash types.Hash, codeLen int) bool {
	if codeLen == 0 {
		return false
	}

	if hash == types.EmptyCodeHash || hash == types.ZeroHash {
		return false
	}

	return true
}
