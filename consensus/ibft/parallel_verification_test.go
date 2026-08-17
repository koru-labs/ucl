package ibft

import (
	"fmt"
	"math/big"
	"runtime"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	sth "github.com/0xPolygon/polygon-edge/state/statetesthelper"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func getBenchData() (txsCount []int, workers []int) {
	txsCount = []int{100, 1000, 5000, 10000}
	for i := 1; i <= runtime.GOMAXPROCS(0); i <<= 1 {
		workers = append(workers, i)
	}
	return
}

// BenchmarkSequentialVerifyNoBAL is the absolute baseline: ProcessBlock with EIP-7928 disabled,
// no BAR recording overhead at all. This is the "before BAL existed" wall time.
func BenchmarkSequentialVerifyNoBAL(b *testing.B) {
	benchSizes, _ := getBenchData()

	for _, size := range benchSizes {
		h, txs := benchScenarioNoBAL(b, 12, size)

		// Build through the non-BAL sequential path — no recorder, no BAR. The committed root
		// is the baseline the verify loop below must reproduce.
		header := h.benchHeader()
		block := &types.Block{Header: header, Transactions: txs}

		tran, err := h.executor.ProcessBlock(h.parentHeader.StateRoot, block, h.proposer)
		require.NoError(b, err)
		require.Empty(b, tran.GetBlockAccessRecord(), "EIP-7928 must be off — BAR must not be built")

		_, root, err := tran.Commit()
		require.NoError(b, err)
		header.StateRoot = root

		b.Run(fmt.Sprintf("txs=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for range b.N {
				require.Equal(b, block.Header.StateRoot,
					h.verifySequential(b, block),
					"sequential (no BAL) verification must reproduce the state root")
			}
		})
	}
}

// BenchmarkSequentialVerifyWithBAL measures ProcessBlock under EIP-7928: sequential execution
// plus BAR recording. The delta vs SequentialVerifyNoBAL is the cost BAL adds on the proposer
// side (and to any verifier that falls back to the sequential path).
func BenchmarkSequentialVerifyWithBAL(b *testing.B) {
	benchSizes, _ := getBenchData()

	for _, size := range benchSizes {
		h, txs := benchScenario(b, 12, size)
		block := h.benchBlockBAL(b, txs)

		b.Run(fmt.Sprintf("txs=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()

			for range b.N {
				require.Equal(b, block.Header.StateRoot,
					h.verifySequential(b, block),
					"sequential (with BAL) verification must reproduce the state root")
			}
		})
	}
}

// BenchmarkParallelVerifyBAL measures verifier-side cost of parallel execution driven by the
// block's BAL: a pre-built block with BlockAccessRecord attached is re-executed via
// ParallelProcessBlock and ApplyBlockAccessRecord, and the resulting state root must reproduce
// the sequential ProcessBlock root. Sweeps worker counts 1..GOMAXPROCS.
func BenchmarkParallelVerifyBAL(b *testing.B) {
	benchSizes, benchWorkers := getBenchData()

	for _, size := range benchSizes {
		h, txs := benchScenario(b, 12, size)
		block := h.benchBlockBAL(b, txs)

		for _, workers := range benchWorkers {
			b.Run(fmt.Sprintf("txs=%d/workers=%d", size, workers), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()

				for range b.N {
					require.Equal(b, block.Header.StateRoot,
						h.verifyParallelBAL(b, block, uint64(workers)),
						"BAL-driven parallel verification must reproduce sequential state root")
				}
			})
		}
	}
}

// benchScenario returns a BAL-enabled harness populated with the RandomizedWorkload.
func benchScenario(tb testing.TB, numSenders, numTxs int) (*parHarness, []*types.Transaction) {
	tb.Helper()

	h := newParHarness(tb)
	return h, populateWorkload(tb, h, numSenders, numTxs)
}

// benchScenarioNoBAL returns a harness with EIP-7928 disabled, populated with the same workload.
func benchScenarioNoBAL(tb testing.TB, numSenders, numTxs int) (*parHarness, []*types.Transaction) {
	tb.Helper()

	h := newParHarnessNoBAL(tb)
	return h, populateWorkload(tb, h, numSenders, numTxs)
}

// populateWorkload deploys Balances + two Routers + Proxy against h's parent state and returns
// the conflict-rich RandomizedWorkload built over them.
func populateWorkload(tb testing.TB, h *parHarness, numSenders, numTxs int) []*types.Transaction {
	tb.Helper()

	senders := make([]types.Address, numSenders)
	for i := range senders {
		senders[i] = parAddr(byte(10 + i))
	}

	addrs := h.setupParent(tb, fundAll(senders...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(tb, sth.BalancesInitHex)),
	})
	balAddr := addrs[0]

	more := h.deployMore(tb, [][]byte{
		sth.AppendCtorAddr(sth.MustDecodeHex(tb, sth.RouterInitHex), balAddr),
		sth.AppendCtorAddr(sth.MustDecodeHex(tb, sth.RouterInitHex), balAddr),
		sth.AppendCtorAddr(sth.MustDecodeHex(tb, sth.ProxyInitHex), balAddr),
	})

	targets := make([]types.Address, 6)
	for i := range targets {
		targets[i] = parAddr(byte(200 + i))
	}
	receivers := make([]types.Address, 6)
	for i := range receivers {
		receivers[i] = parAddr(byte(100 + i))
	}

	return sth.RandomizedWorkload(1337, numTxs, senders, targets, receivers,
		sth.RandomizedWorkloadContracts{
			Balances: balAddr,
			Router1:  more[0],
			Router2:  more[1],
			Proxy:    more[2],
		})
}

// parHarness holds the minimum needed to drive ProcessBlock / ParallelProcessBlock against a
// fresh in-memory executor. The forks map is held so newParHarnessNoBAL can flip EIP-7928 off.
type parHarness struct {
	executor      *state.Executor
	forks         *chain.Forks
	proposer      types.Address
	parentHeader  *types.Header
	deployerNonce uint64
}

func newParHarness(tb testing.TB) *parHarness {
	tb.Helper()

	forks := &chain.Forks{
		chain.Homestead:      chain.NewFork(0),
		chain.EIP150:         chain.NewFork(0),
		chain.EIP155:         chain.NewFork(0),
		chain.EIP158:         chain.NewFork(0),
		chain.Byzantium:      chain.NewFork(0),
		chain.Constantinople: chain.NewFork(0),
		chain.Petersburg:     chain.NewFork(0),
		chain.Istanbul:       chain.NewFork(0),
		chain.London:         chain.NewFork(0),
		chain.Ucl:            chain.NewFork(0),
		chain.EIP3607:        chain.NewFork(0),
		chain.EIP3855:        chain.NewFork(0),
		chain.EIP5656:        chain.NewFork(0),
		chain.EIP7939:        chain.NewFork(0),
		chain.EIP1153:        chain.NewFork(0),
		chain.EIP7928:        chain.NewFork(0),
	}

	chainParams := &chain.Params{
		ChainID:      100,
		Forks:        forks,
		BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
	}

	parentHeader := &types.Header{
		Number:   2,
		Hash:     types.Hash{0, 1, 2, 3, 4, 5},
		GasLimit: 1_000_000_000_000,
	}

	executor := state.NewExecutor(
		chainParams, itrie.NewState(itrie.NewMemoryStorage()), hclog.NewNullLogger())

	// RandomizedWorkload never invokes BLOCKHASH, so a no-op suffices.
	executor.GetHash = func(*types.Header) state.GetHashByNumber {
		return func(uint64) types.Hash { return types.ZeroHash }
	}

	return &parHarness{
		executor:     executor,
		forks:        forks,
		proposer:     parAddr(0xFF),
		parentHeader: parentHeader,
	}
}

// newParHarnessNoBAL is newParHarness with EIP-7928 disabled. Everything else — parent state,
// executor, accounts, deployed contracts — is identical, so blocks built here compare
// apples-to-apples with BAL-enabled harness runs on the same workload.
func newParHarnessNoBAL(tb testing.TB) *parHarness {
	tb.Helper()

	h := newParHarness(tb)
	delete(*h.forks, chain.EIP7928)

	return h
}

// setupParent funds accounts and runs deploy txs against a fresh genesis state, then commits and
// pins the resulting root on the parent header. Call once, first.
func (h *parHarness) setupParent(
	tb testing.TB, balances map[types.Address]*big.Int, deploys []*types.Transaction,
) []types.Address {
	tb.Helper()

	tran, err := h.executor.BeginTxn(types.ZeroHash, h.parentHeader, types.ZeroAddress)
	require.NoError(tb, err)

	for addr, bal := range balances {
		require.NoError(tb, tran.SetAccountDirectly(addr,
			&chain.GenesisAccount{Balance: bal, Nonce: 0}))
	}

	addrs := make([]types.Address, 0, len(deploys))
	for _, d := range deploys {
		res, err := tran.Apply(d)
		require.NoError(tb, err)
		require.NoError(tb, res.Err, "deploy tx must succeed")
		addrs = append(addrs, res.Address)
	}

	_, root, err := tran.Commit()
	require.NoError(tb, err)

	h.parentHeader.StateRoot = root
	h.deployerNonce = uint64(len(deploys))

	return addrs
}

// deployMore applies additional deploys from parDeployer on top of the current parent state,
// continuing the deployer nonce.
func (h *parHarness) deployMore(tb testing.TB, initCodes [][]byte) []types.Address {
	tb.Helper()

	tran, err := h.executor.BeginTxn(h.parentHeader.StateRoot, h.parentHeader, types.ZeroAddress)
	require.NoError(tb, err)

	addrs := make([]types.Address, 0, len(initCodes))
	for _, code := range initCodes {
		res, err := tran.Apply(sth.DeployTx(parDeployer, h.deployerNonce, code))
		require.NoError(tb, err)
		require.NoError(tb, res.Err, "deploy tx must succeed")
		addrs = append(addrs, res.Address)
		h.deployerNonce++
	}

	_, root, err := tran.Commit()
	require.NoError(tb, err)

	h.parentHeader.StateRoot = root

	return addrs
}

// benchHeader synthesizes a minimal block-N+1 header. Number and gas limit are all BeginTxn
// reads; base fee and timestamp default to zero.
func (h *parHarness) benchHeader() *types.Header {
	return &types.Header{
		Number:   h.parentHeader.Number + 1,
		GasLimit: h.parentHeader.GasLimit,
	}
}

// benchBlockBAL builds the block via ProcessBlock (which records the BAR under EIP-7928),
// extracts the packed BAR from the transition, and attaches it to the block. StateRoot and
// BlockAccessRecordHash are pinned on the header so the verify benches can compare.
func (h *parHarness) benchBlockBAL(tb testing.TB, txs []*types.Transaction) *types.Block {
	tb.Helper()

	header := h.benchHeader()
	block := &types.Block{Header: header, Transactions: txs}

	tran, err := h.executor.ProcessBlock(h.parentHeader.StateRoot, block, h.proposer)
	require.NoError(tb, err)

	bar := tran.GetBlockAccessRecord()
	require.NotEmpty(tb, bar, "EIP-7928 must be active and produce a non-empty BAR")

	_, root, err := tran.Commit()
	require.NoError(tb, err)

	block.BlockAccessRecord = bar
	header.BlockAccessRecordHash = bar.Hash()
	header.StateRoot = root

	return block
}

// verifySequential runs ProcessBlock + Commit. Whether the BAR is built or not depends on
// whether EIP-7928 is active for the block's number at call time — governed by the harness.
func (h *parHarness) verifySequential(tb testing.TB, block *types.Block) types.Hash {
	tb.Helper()

	tran, err := h.executor.ProcessBlock(h.parentHeader.StateRoot, block, h.proposer)
	require.NoError(tb, err)

	_, root, err := tran.Commit()
	require.NoError(tb, err)

	return root
}

// verifyParallelBAL replays the block through the parallel path: ParallelProcessBlock re-executes
// txs in parallel using the pre-computed BAR, we assert the recomputed BAR hash matches, then
// ApplyBlockAccessRecord materializes the state root. Same three-step dance
// blockchain.executeBlockTransactions does in production.
func (h *parHarness) verifyParallelBAL(
	tb testing.TB, block *types.Block, workers uint64,
) types.Hash {
	tb.Helper()

	bar, _, _, err := h.executor.ParallelProcessBlock(
		h.parentHeader.StateRoot, block, h.proposer, workers)
	require.NoError(tb, err)

	packed := bar.Pack()
	require.Equal(tb, block.Header.BlockAccessRecordHash, packed.Hash(),
		"parallel-recomputed BAR hash must match the builder's")

	root, err := h.executor.ApplyBlockAccessRecord(
		block.Header.Number, h.parentHeader.StateRoot, packed)
	require.NoError(tb, err)

	return root
}

var parDeployer = types.Address{0xD0}

func parAddr(b byte) types.Address {
	var a types.Address
	a[19] = b
	return a
}

func fundAll(callers ...types.Address) map[types.Address]*big.Int {
	m := map[types.Address]*big.Int{parDeployer: ethgo.Ether(100)}
	for _, c := range callers {
		m[c] = ethgo.Ether(100)
	}
	return m
}
