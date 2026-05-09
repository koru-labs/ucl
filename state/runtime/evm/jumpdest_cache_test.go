package evm

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeJumpdestTestCode builds a deterministic pseudo-random bytecode of the
// requested size with a JUMPDEST sprinkled at positions that are NOT inside
// PUSH immediates, so the bitmap actually has bits set. PUSH opcodes are
// scrubbed to avoid accidentally consuming the JUMPDEST byte.
func makeJumpdestTestCode(t testing.TB, size int, seed int64) []byte {
	t.Helper()

	r := rand.New(rand.NewSource(seed))
	code := make([]byte, size)

	for i := 0; i < size; i++ {
		b := byte(r.Intn(256))
		if isPushOp(b) {
			b = 0x01 // ADD; never a PUSH so we don't skip the next byte
		}

		code[i] = b
	}

	for i := 0; i < size; i += 64 {
		code[i] = JUMPDEST
	}

	return code
}

// directBitmap computes the JUMPDEST bitmap with the un-cached path so we can
// compare against the cached path bit-for-bit.
func directBitmap(t testing.TB, code []byte) []byte {
	t.Helper()

	var b bitmap

	b.setCode(code)
	out := make([]byte, len(b.buf))

	copy(out, b.buf)

	return out
}

// withFreshCache reinstalls a clean cache with the given size and restores
// the default at the end of the test. Centralised so individual tests can't
// pollute each other's stats counters.
func withFreshCache(t testing.TB, size int) {
	t.Helper()

	t.Cleanup(func() {
		SetJumpdestCacheSize(DefaultJumpdestCacheSize)
	})

	SetJumpdestCacheSize(size)
	PurgeJumpdestCache()
}

func TestJumpdestCache_HitMatchesMiss(t *testing.T) {
	withFreshCache(t, 64)

	code := makeJumpdestTestCode(t, 4096, 1)
	hash := types.BytesToHash(crypto.Keccak256(code))

	want := directBitmap(t, code)

	var miss bitmap

	miss.setCodeWithCache(hash, code)

	hits, misses, _ := JumpdestCacheStats()
	require.Equal(t, uint64(0), hits, "first lookup should not be a hit")
	require.Equal(t, uint64(1), misses, "first lookup should be a miss")
	assert.Equal(t, want, miss.buf, "cache-miss bitmap diverges from un-cached computation")

	var hit bitmap

	hit.setCodeWithCache(hash, code)

	hits, misses, _ = JumpdestCacheStats()
	require.Equal(t, uint64(1), hits, "second lookup should be a hit")
	require.Equal(t, uint64(1), misses, "miss count should be unchanged on hit")
	assert.True(t, hit.cached, "cache hit must mark the bitmap as shared")
	assert.Equal(t, want, hit.buf, "cache-hit bitmap diverges from un-cached computation")

	// The hit and miss must both reference the same underlying buffer.
	// Pointer equality is intentional — it proves we're sharing memory, not
	// re-allocating on every hit.
	assert.Same(t, &miss.buf[0], &hit.buf[0], "cache hit should share the cached buffer")
}

func TestJumpdestCache_StatsTrackHitsAndMisses(t *testing.T) {
	withFreshCache(t, 64)

	code := makeJumpdestTestCode(t, 1024, 2)
	hash := types.BytesToHash(crypto.Keccak256(code))

	var b bitmap

	b.setCodeWithCache(hash, code) // miss
	b.reset()
	b.setCodeWithCache(hash, code) // hit
	b.reset()
	b.setCodeWithCache(hash, code) // hit
	b.reset()

	hits, misses, enabled := JumpdestCacheStats()
	assert.True(t, enabled)
	assert.Equal(t, uint64(2), hits, "expected exactly two cache hits")
	assert.Equal(t, uint64(1), misses, "expected exactly one cache miss")
}

func TestJumpdestCache_SkipsInitCodeAndEmpty(t *testing.T) {
	t.Run("empty code is never cached", func(t *testing.T) {
		withFreshCache(t, 64)

		var b bitmap

		b.setCodeWithCache(types.EmptyCodeHash, nil)
		b.setCodeWithCache(types.EmptyCodeHash, []byte{})

		hits, misses, _ := JumpdestCacheStats()
		assert.Zero(t, hits, "empty code must bypass the cache")
		assert.Zero(t, misses, "empty code must bypass the cache")
		assert.Zero(t, JumpdestCacheLen())
	})

	t.Run("init code (EmptyCodeHash) is never cached", func(t *testing.T) {
		withFreshCache(t, 64)

		var b bitmap

		// Simulates `applyCreate`: account exists with EmptyCodeHash but the
		// constructor's bytecode is non-empty.
		code := makeJumpdestTestCode(t, 512, 3)
		b.setCodeWithCache(types.EmptyCodeHash, code)

		assert.False(t, b.cached, "init code must take the owned-buffer path")

		hits, misses, _ := JumpdestCacheStats()
		assert.Zero(t, hits)
		assert.Zero(t, misses, "bypass should not even consult the cache")
		assert.Zero(t, JumpdestCacheLen())

		assert.Equal(t, directBitmap(t, code), b.buf,
			"bypass path must still produce a correct bitmap")
	})

	t.Run("zero hash is never cached", func(t *testing.T) {
		withFreshCache(t, 64)

		code := makeJumpdestTestCode(t, 512, 3)

		var b bitmap

		b.setCodeWithCache(types.ZeroHash, code)

		assert.False(t, b.cached)
		assert.Zero(t, JumpdestCacheLen())
		assert.Equal(t, directBitmap(t, code), b.buf)
	})
}

func TestJumpdestCache_DisabledCacheStillProducesCorrectBitmap(t *testing.T) {
	withFreshCache(t, 0)

	_, _, enabled := JumpdestCacheStats()
	require.False(t, enabled, "size 0 must disable the cache")

	code := makeJumpdestTestCode(t, 2048, 4)
	hash := types.BytesToHash(crypto.Keccak256(code))

	var b bitmap

	b.setCodeWithCache(hash, code)
	assert.False(t, b.cached, "disabled cache must take the owned-buffer path")
	assert.Equal(t, directBitmap(t, code), b.buf)
	assert.Zero(t, JumpdestCacheLen())
}

func TestJumpdestCache_ResetDetachesCachedBuffer(t *testing.T) {
	withFreshCache(t, 64)

	code := makeJumpdestTestCode(t, 1024, 5)
	hash := types.BytesToHash(crypto.Keccak256(code))

	var b bitmap

	b.setCodeWithCache(hash, code) // miss
	require.True(t, b.cached, "miss must publish the buffer and adopt the shared reference")

	cached := b.buf
	b.reset()
	assert.False(t, b.cached, "reset() should clear the cached flag")
	assert.Nil(t, b.buf, "reset() must detach (not zero) a shared cache buffer")

	// Re-fetch and verify the cached buffer is unmodified — i.e. reset() did
	// NOT zero it out from underneath us.
	b.setCodeWithCache(hash, code)
	require.True(t, b.cached)

	want := directBitmap(t, code)
	assert.Equal(t, want, b.buf, "cached buffer must not have been mutated by reset()")
	assert.Same(t, &cached[0], &b.buf[0], "cached buffer should be the same backing array")
}

func TestJumpdestCache_ResetReusesOwnedBuffer(t *testing.T) {
	withFreshCache(t, 0)

	code := makeJumpdestTestCode(t, 1024, 11)

	var b bitmap

	b.setCode(code)
	require.False(t, b.cached, "setCode must always own the buffer")

	cap1 := cap(b.buf)
	require.Greater(t, cap1, 0)

	b.reset()
	assert.NotNil(t, b.buf, "owned buffer should be retained for reuse")
	assert.Zero(t, len(b.buf))

	// Reuse for a same-size or smaller code: the underlying allocation should
	// still serve.
	b.setCode(code)
	assert.Equal(t, cap1, cap(b.buf), "owned-buffer reuse must avoid reallocation")
}

func TestJumpdestCache_ResizingDropsExistingEntries(t *testing.T) {
	withFreshCache(t, 16)

	code := makeJumpdestTestCode(t, 1024, 6)
	hash := types.BytesToHash(crypto.Keccak256(code))

	var b bitmap

	b.setCodeWithCache(hash, code)
	require.Equal(t, 1, JumpdestCacheLen())

	SetJumpdestCacheSize(8)
	assert.Zero(t, JumpdestCacheLen(), "resizing the cache must reset its contents")
	hits, misses, enabled := JumpdestCacheStats()
	assert.True(t, enabled)
	assert.Zero(t, hits)
	assert.Zero(t, misses, "stats must reset along with the LRU")
}

func TestJumpdestCache_LRUEviction(t *testing.T) {
	const capacity = 4

	withFreshCache(t, capacity)

	// Insert capacity+2 distinct codes to force eviction of the oldest two.
	codes := make([][]byte, capacity+2)
	hashes := make([]types.Hash, capacity+2)

	for i := range codes {
		codes[i] = makeJumpdestTestCode(t, 256, int64(100+i))
		hashes[i] = types.BytesToHash(crypto.Keccak256(codes[i]))

		var b bitmap

		b.setCodeWithCache(hashes[i], codes[i])
	}

	assert.LessOrEqual(t, JumpdestCacheLen(), capacity, "LRU must not exceed capacity")

	// Re-fetching the most recently inserted entry must be a HIT.
	hitsBefore, missesBefore, _ := JumpdestCacheStats()

	var b bitmap

	b.setCodeWithCache(hashes[len(hashes)-1], codes[len(codes)-1])

	hitsAfter, missesAfter, _ := JumpdestCacheStats()
	assert.Equal(t, hitsBefore+1, hitsAfter, "most recent insertion should still be hot")
	assert.Equal(t, missesBefore, missesAfter)

	// Re-fetching an evicted entry must be a MISS that re-populates the cache.
	missesBefore = missesAfter
	b.reset()
	b.setCodeWithCache(hashes[0], codes[0])
	_, missesAfter, _ = JumpdestCacheStats()
	assert.Equal(t, missesBefore+1, missesAfter, "evicted entry should miss and re-populate")
}

func TestJumpdestCache_ConcurrentSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency stress test in -short mode")
	}

	withFreshCache(t, 256)

	const (
		distinctContracts = 32
		workers           = 16
		iterations        = 500
	)

	codes := make([][]byte, distinctContracts)
	hashes := make([]types.Hash, distinctContracts)
	expected := make([][]byte, distinctContracts)

	for i := range codes {
		codes[i] = makeJumpdestTestCode(t, 1024+(i*64), int64(7000+i))
		hashes[i] = types.BytesToHash(crypto.Keccak256(codes[i]))
		expected[i] = directBitmap(t, codes[i])
	}

	var (
		wg     sync.WaitGroup
		errCnt atomic.Uint64
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)

		go func(seed int64) {
			defer wg.Done()

			r := rand.New(rand.NewSource(seed))

			for i := 0; i < iterations; i++ {
				idx := r.Intn(distinctContracts)

				var b bitmap

				b.setCodeWithCache(hashes[idx], codes[idx])

				// Read back enough bytes to make sure no concurrent writer
				// is corrupting the cached buffer.
				if !slicesEqual(b.buf, expected[idx]) {
					errCnt.Add(1)
				}

				b.reset()
			}
		}(int64(w))
	}

	wg.Wait()

	assert.Zero(t, errCnt.Load(), "concurrent readers observed a corrupted bitmap")
}

func slicesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestJumpdestCache_BitmapWidthMatchesCodeSize(t *testing.T) {
	for _, size := range []int{1, 7, 8, 9, 1024, 4097, 24 * 1024, 50 * 1024} {
		size := size

		t.Run("size", func(t *testing.T) {
			code := makeJumpdestTestCode(t, size, 13)

			var b bitmap

			b.setCode(code)

			assert.Equal(t, size/bitmapSize+1, len(b.buf),
				"bitmap width must be ceil(codeSize/bitmapSize)+1 for code size %d", size)
		})
	}
}

func TestJumpdestCache_EvmRunUsesCache(t *testing.T) {
	withFreshCache(t, 64)

	// Realistic flow through EVM.Run with a non-empty code hash so the
	// cache is exercised end-to-end (not just at the bitmap layer).
	code := []byte{
		byte(PUSH1), 0x01, byte(PUSH1), 0x02, byte(ADD),
		byte(PUSH1), 0x00, byte(MSTORE8),
		byte(PUSH1), 0x01, byte(PUSH1), 0x00, byte(RETURN),
	}
	hash := types.BytesToHash(crypto.Keccak256(code))

	host := &mockHost{}
	host.On("GetCodeHash", types.ZeroAddress).Return(hash.String())

	evm := NewEVM()
	contract := newMockContract(nil, 5000, code)

	res := evm.Run(contract, host, &chain.ForksInTime{})
	require.NoError(t, res.Err)

	hits, misses, _ := JumpdestCacheStats()
	assert.Equal(t, uint64(0), hits)
	assert.Equal(t, uint64(1), misses, "first EVM Run() should be a cache miss")

	res = evm.Run(contract, host, &chain.ForksInTime{})
	require.NoError(t, res.Err)

	hits, misses, _ = JumpdestCacheStats()
	assert.Equal(t, uint64(1), hits, "second EVM Run() with same code should hit the cache")
	assert.Equal(t, uint64(1), misses)
}
