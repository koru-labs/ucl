package evm

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
)

// BenchmarkJumpdestCache_* compare cold (cache disabled) vs warm (cache hit
// every call) performance of `EVM.Run()` with a tiny program that immediately
// halts. Per-call overhead is therefore dominated by the JUMPDEST analysis,
// which is exactly the cost we are eliminating.
//
// Run with:
//
//	go test ./state/runtime/evm/ -bench=BenchmarkJumpdestCache -benchmem -run=^$ -count=3 -benchtime=1.5s

func makeBenchCode(size int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))

	code := make([]byte, size)
	for i := 0; i < size; i++ {
		b := byte(r.Intn(256))
		if isPushOp(b) {
			b = 0x01
		}

		code[i] = b
	}

	for i := 0; i < size; i += 64 {
		code[i] = JUMPDEST
	}

	// Make sure the very first instruction halts so we measure entry cost,
	// not bytecode execution cost.
	code[0] = byte(STOP)

	return code
}

type benchHost struct {
	hash types.Hash
}

func (h *benchHost) AccountExists(types.Address) bool                { return true }
func (h *benchHost) GetStorage(types.Address, types.Hash) types.Hash { return types.Hash{} }
func (h *benchHost) SetStorage(types.Address, types.Hash, types.Hash, *chain.ForksInTime) runtime.StorageStatus {
	return runtime.StorageUnchanged
}
func (h *benchHost) SetState(types.Address, types.Hash, types.Hash) {}
func (h *benchHost) SetNonPayable(bool)                             {}
func (h *benchHost) GetBalance(types.Address) *big.Int              { return nil }
func (h *benchHost) GetCodeSize(types.Address) int                  { return 0 }
func (h *benchHost) GetCodeHash(types.Address) types.Hash           { return h.hash }
func (h *benchHost) GetCode(types.Address) []byte                   { return nil }
func (h *benchHost) Selfdestruct(types.Address, types.Address)      {}
func (h *benchHost) GetTxContext() runtime.TxContext                { return runtime.TxContext{} }
func (h *benchHost) GetBlockHash(int64) types.Hash                  { return types.Hash{} }
func (h *benchHost) EmitLog(types.Address, []types.Hash, []byte)    {}
func (h *benchHost) Callx(*runtime.Contract, runtime.Host) *runtime.ExecutionResult {
	return &runtime.ExecutionResult{}
}
func (h *benchHost) Empty(types.Address) bool                                { return false }
func (h *benchHost) GetNonce(types.Address) uint64                           { return 0 }
func (h *benchHost) Transfer(types.Address, types.Address, *big.Int) error   { return nil }
func (h *benchHost) GetTracer() runtime.VMTracer                             { return nil }
func (h *benchHost) GetRefund() uint64                                       { return 0 }
func (h *benchHost) GetTransientState(types.Address, types.Hash) types.Hash  { return types.Hash{} }
func (h *benchHost) SetTransientState(types.Address, types.Hash, types.Hash) {}
func (h *benchHost) TouchTransientStorage()
func (h *benchHost) BALRecorder() runtime.BALRecorder { return nil }

func benchmarkRun(b *testing.B, codeSize int, cacheSize int) {
	b.Helper()

	prev := JumpdestCacheLen
	_ = prev // silence unused if linter ever runs this on a stripped build

	SetJumpdestCacheSize(cacheSize)
	PurgeJumpdestCache()

	b.Cleanup(func() {
		SetJumpdestCacheSize(DefaultJumpdestCacheSize)
		PurgeJumpdestCache()
	})

	code := makeBenchCode(codeSize, int64(codeSize))
	hash := types.BytesToHash(crypto.Keccak256(code))

	host := &benchHost{hash: hash}
	if cacheSize == 0 {
		// With the cache disabled, hash doesn't matter for the bypass path.
		// Use ZeroHash so that even if a future change re-enables caching,
		// the bench keeps measuring the bypass path.
		host.hash = types.ZeroHash
	}

	contract := runtime.NewContractCall(
		1,
		types.ZeroAddress,
		types.ZeroAddress,
		types.ZeroAddress,
		nil,
		1_000_000,
		code,
		nil,
	)
	evm := NewEVM()
	cfg := &chain.ForksInTime{}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		evm.Run(contract, host, cfg)
	}
}

func BenchmarkJumpdestCache_500B_Cached(b *testing.B)   { benchmarkRun(b, 500, DefaultJumpdestCacheSize) }
func BenchmarkJumpdestCache_500B_Uncached(b *testing.B) { benchmarkRun(b, 500, 0) }

func BenchmarkJumpdestCache_5KB_Cached(b *testing.B) {
	benchmarkRun(b, 5*1024, DefaultJumpdestCacheSize)
}
func BenchmarkJumpdestCache_5KB_Uncached(b *testing.B) { benchmarkRun(b, 5*1024, 0) }

func BenchmarkJumpdestCache_24KB_Cached(b *testing.B) {
	benchmarkRun(b, 24*1024, DefaultJumpdestCacheSize)
}
func BenchmarkJumpdestCache_24KB_Uncached(b *testing.B) { benchmarkRun(b, 24*1024, 0) }

func BenchmarkJumpdestCache_50KB_Cached(b *testing.B) {
	benchmarkRun(b, 50*1024, DefaultJumpdestCacheSize)
}
func BenchmarkJumpdestCache_50KB_Uncached(b *testing.B) { benchmarkRun(b, 50*1024, 0) }
