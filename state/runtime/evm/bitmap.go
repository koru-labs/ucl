package evm

import (
	"time"

	"github.com/hashicorp/go-metrics"

	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/types"
)

const bitmapSize = 8

// bitmap stores the JUMPDEST positions of a contract's bytecode as a
// bit-packed []byte. The same struct is reused across every EVM Run() via the
// pooled `state` object, but the underlying buffer can have one of two
// lifetimes:
//
//   - Owned: this bitmap allocated `buf` itself, can grow it in place, and
//     must zero it out on reset(). Used for init code and any path that
//     bypasses the cache.
//   - Shared: `buf` is a slice into an immutable cache entry held by the
//     process-wide `jumpdestCache`. The buffer must NOT be mutated; reset()
//     just detaches the reference so the cache entry can keep being read by
//     other goroutines.
//
// `cached` records which case we are in. The wrong choice in either
// direction is a correctness bug — mutating a shared buffer would race with
// other Run()s reading the same entry, while leaving stale bits in an owned
// buffer would corrupt the next Run().
type bitmap struct {
	buf    []byte
	cached bool
}

func (b *bitmap) isSet(i uint64) bool {
	return b.buf[i/bitmapSize]&(1<<(i%bitmapSize)) != 0
}

// reset prepares the bitmap to be returned to the pool. For owned buffers we
// zero the prefix and truncate so the next Run() can reuse the allocation;
// for cache-shared buffers we just detach so we never accidentally write into
// memory that other goroutines may be reading.
func (b *bitmap) reset() {
	if b.cached {
		b.buf = nil
		b.cached = false

		return
	}

	for i := range b.buf {
		b.buf[i] = 0
	}

	b.buf = b.buf[:0]
}

// setCode populates the bitmap from `code`, writing into the bitmap's owned
// buffer (allocating or growing as needed). This path NEVER touches the
// JUMPDEST cache — it exists for code that is one-shot (init code) or for
// callers that explicitly opt out of caching (tests).
func (b *bitmap) setCode(code []byte) {
	if b.cached {
		// Drop the cache reference; we are about to write a fresh, owned bitmap.
		b.buf = nil
		b.cached = false
	}

	bufLen := len(code)/bitmapSize + 1
	b.buf = common.ExtendByteSlice(b.buf, bufLen)
	buildStart := time.Now()
	populateJumpdestBitmap(code, b.buf)
	metrics.MeasureSince([]string{"evm", "jumpdest_bitmap", "build"}, buildStart)
	metrics.IncrCounter([]string{"evm", "jumpdest_bitmap", "build", "count"}, 1)
}

// setCodeWithCache populates the bitmap using the JUMPDEST analysis cache when
// possible, falling back to an owned bitmap when the (codeHash, code) pair is
// not eligible for caching or when the cache is disabled.
//
// The cache key is the contract's code hash, which is content-addressed and
// therefore safe to share across chains and goroutines. On a cache hit we
// adopt a reference into the cached buffer with zero copying; on a miss we
// compute into a fresh allocation and publish it. On bypass (init code, empty
// code, or `SetJumpdestCacheSize(0)`), we delegate to setCode so we can keep
// reusing the pooled, owned buffer across Run()s.
//
// Important: both the hit AND the miss path leave the bitmap with `cached =
// true`. On a miss we just published the buffer to the LRU, so other
// goroutines may pick it up before this Run() returns; mutating it later
// would race with them. The flag drives `reset()` to detach (not zero) the
// shared buffer.
func (b *bitmap) setCodeWithCache(codeHash types.Hash, code []byte) {
	c := jumpdestCache.Load()
	if c == nil || !shouldCacheJumpdestBitmap(codeHash, len(code)) {
		if c == nil {
			metrics.IncrCounter([]string{"evm", "jumpdest_cache", "bypass", "disabled"}, 1)
		} else {
			metrics.IncrCounter([]string{"evm", "jumpdest_cache", "bypass", "not_cacheable"}, 1)
		}

		b.setCode(code)

		return
	}

	if cached, ok := lookupCachedJumpdestBitmap(codeHash); ok {
		// Drop any owned buffer; we will share the cached one. The owned
		// buffer becomes garbage and is reclaimed by GC on the next cycle.
		b.buf = cached
		b.cached = true

		return
	}

	// Cache miss: build a fresh, immutable buffer (sized exactly so that
	// `cap(b.buf) == len(b.buf)` and downstream code can't accidentally
	// extend it under another goroutine's nose), publish it, and adopt it.
	bufLen := len(code)/bitmapSize + 1
	fresh := make([]byte, bufLen)
	buildStart := time.Now()
	populateJumpdestBitmap(code, fresh)
	metrics.MeasureSince([]string{"evm", "jumpdest_bitmap", "build"}, buildStart)
	metrics.IncrCounter([]string{"evm", "jumpdest_bitmap", "build", "count"}, 1)

	storeCachedJumpdestBitmap(codeHash, fresh)

	// If this state previously owned a bitmap buffer, we deliberately let it
	// go: the cached buffer takes its place, and re-using a now-cached buffer
	// for a different contract on the next Run() would mutate memory that
	// other goroutines could be reading.
	b.buf = fresh
	b.cached = true
}

// populateJumpdestBitmap is the actual JUMPDEST-detection scan. It is the
// single hot loop that every EVM call used to pay for unconditionally; with
// the cache in place it now runs at most once per distinct deployed contract.
//
// The buffer is assumed zero-filled (true for `make([]byte, ...)` and for
// freshly grown owned buffers).
func populateJumpdestBitmap(code, buf []byte) {
	codeSize := len(code)

	for i := 0; i < codeSize; {
		c := code[i]

		if isPushOp(c) {
			// PUSH1..PUSH32: skip the immediate data so we don't mistake
			// embedded data bytes for opcodes (this is the whole reason we
			// can't just scan for `c == JUMPDEST`).
			i += int(c) - 0x60 + 2
		} else {
			if c == JUMPDEST {
				buf[i/bitmapSize] |= 1 << (i % bitmapSize)
			}

			i++
		}
	}
}

func isPushOp(i byte) bool {
	// From PUSH1 (0x60) to PUSH32 (0x7F): high 3 bits == 011.
	return i>>5 == 3
}
