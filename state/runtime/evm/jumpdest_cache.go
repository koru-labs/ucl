package evm

import (
	"sync/atomic"

	"github.com/hashicorp/go-metrics"
	lru "github.com/hashicorp/golang-lru"

	"github.com/0xPolygon/polygon-edge/types"
)

// DefaultJumpdestCacheSize is the default number of distinct contract codes
// (keyed by code hash) for which the EVM will keep a precomputed JUMPDEST
// bitmap in memory.
//
// Each cached entry is `len(code) / bitmapSize + 1` bytes (i.e. ~12.5% of the
// contract size). At 4096 entries × 24 KB max code size that bounds the cache
// at ~12 MiB; with 50 KB codes ~25 MiB. Operators can override this via
// `SetJumpdestCacheSize` (or the `--jumpdest-cache-size` CLI flag wired in
// `command/server`).
const DefaultJumpdestCacheSize = 4096

// jumpdestCacheT bundles the LRU together with hit/miss counters so they can
// be swapped atomically as a single unit when the cache is reconfigured at
// runtime. Cached values are immutable bitmap buffers and are safe to share
// across goroutines.
type jumpdestCacheT struct {
	cache  *lru.Cache
	hits   atomic.Uint64
	misses atomic.Uint64
}

// jumpdestCache is a process-wide cache of JUMPDEST analysis results, keyed
// by the contract's code hash. Holding it as an atomic pointer lets us
// reconfigure (or disable) the cache at any time without taking a lock on the
// EVM hot path; in-flight Run()s keep a reference to the previous instance
// and finish safely.
//
// A nil pointer means "cache disabled".
var jumpdestCache atomic.Pointer[jumpdestCacheT]

func init() {
	SetJumpdestCacheSize(DefaultJumpdestCacheSize)
}

// SetJumpdestCacheSize installs a new JUMPDEST bitmap cache with the given
// capacity. Passing a value <= 0 disables the cache entirely (useful for
// memory-constrained environments or for diff testing). It is safe to call
// at any time, including while the EVM is executing on other goroutines.
//
// Resizing always discards previously cached entries; the cache will refill
// on subsequent contract calls.
func SetJumpdestCacheSize(size int) {
	if size <= 0 {
		jumpdestCache.Store(nil)

		return
	}

	cache, err := lru.New(size)
	if err != nil {
		// lru.New only fails when size <= 0, which we already guarded against.
		// Treat any unexpected failure as "disable cache" rather than panicking
		// inside an unrelated EVM Run().
		jumpdestCache.Store(nil)

		return
	}

	jumpdestCache.Store(&jumpdestCacheT{cache: cache})
}

// JumpdestCacheStats returns the cumulative hit and miss counters since the
// last `SetJumpdestCacheSize` call. The `enabled` flag distinguishes
// "cache exists but has seen 0 lookups" (true) from "cache is disabled"
// (false), so observability dashboards can avoid plotting a meaningless 0%
// hit ratio against a disabled cache.
func JumpdestCacheStats() (hits, misses uint64, enabled bool) {
	c := jumpdestCache.Load()
	if c == nil {
		return 0, 0, false
	}

	return c.hits.Load(), c.misses.Load(), true
}

// JumpdestCacheLen returns the current number of distinct code hashes held by
// the cache, or 0 if the cache is disabled. Intended for monitoring and tests.
func JumpdestCacheLen() int {
	c := jumpdestCache.Load()
	if c == nil {
		return 0
	}

	return c.cache.Len()
}

// PurgeJumpdestCache drops every cached entry without changing the configured
// capacity. Mainly useful for benchmarks and tests.
func PurgeJumpdestCache() {
	c := jumpdestCache.Load()
	if c == nil {
		return
	}

	c.cache.Purge()
	c.hits.Store(0)
	c.misses.Store(0)
}

// lookupCachedJumpdestBitmap returns a (read-only) JUMPDEST bitmap for the
// given code hash if one is cached, or `nil, false` otherwise. The returned
// slice MUST NOT be mutated by the caller — multiple goroutines can be
// reading the same backing array concurrently.
func lookupCachedJumpdestBitmap(hash types.Hash) ([]byte, bool) {
	c := jumpdestCache.Load()
	if c == nil {
		return nil, false
	}

	v, ok := c.cache.Get(hash)
	if !ok {
		c.misses.Add(1)
		metrics.IncrCounter([]string{"evm", "jumpdest_cache", "miss"}, 1)

		return nil, false
	}

	c.hits.Add(1)
	metrics.IncrCounter([]string{"evm", "jumpdest_cache", "hit"}, 1)

	buf, _ := v.([]byte)

	return buf, buf != nil
}

// storeCachedJumpdestBitmap publishes a freshly computed bitmap under the
// given code hash. The buffer becomes immutable from the caller's perspective
// once this returns: any further mutation would be racy with concurrent
// Run()s that may already be using the cached entry.
func storeCachedJumpdestBitmap(hash types.Hash, buf []byte) {
	c := jumpdestCache.Load()
	if c == nil {
		return
	}

	c.cache.Add(hash, buf)
}

// shouldCacheJumpdestBitmap decides whether a given (codeHash, code) pair is
// eligible for the cache.
//
// Bitmaps are cached when the code hash uniquely identifies the running code,
// which is true for any post-deployment call (CALL/DELEGATECALL/STATICCALL/
// CALLCODE). It is NOT true for:
//
//   - Empty code (nothing to scan; the bitmap is trivially empty).
//   - CREATE / CREATE2 init code: while the constructor is running the
//     account exists with `CodeHash == EmptyCodeHash`, so the host returns
//     EmptyCodeHash even though the code being executed is non-empty. Caching
//     under EmptyCodeHash would let *any* non-empty init code collide with
//     itself or with future contracts deployed at the same address. Init
//     code is also one-shot so caching would only thrash the LRU.
//   - Calls into accounts with no associated code-hash (defensive: zero
//     hash). Should not happen in practice.
//
// We deliberately skip the alternative of hashing the bytes ourselves on init
// code: that would re-introduce the very 50 KB scan we're trying to avoid.
func shouldCacheJumpdestBitmap(codeHash types.Hash, codeLen int) bool {
	if codeLen == 0 {
		return false
	}

	if codeHash == types.EmptyCodeHash || codeHash == types.ZeroHash {
		return false
	}

	return true
}
