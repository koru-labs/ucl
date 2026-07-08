package ibft

import (
	"fmt"
	"testing"

	"github.com/0xPolygon/polygon-edge/consensus/ibft/blockstm"
	"github.com/0xPolygon/polygon-edge/state"
	sth "github.com/0xPolygon/polygon-edge/state/statetesthelper"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func getBenchData() (txsCount []int, workers []int) {
	workers = []int{1, 8, 16, 32}
	txsCount = []int{100, 1000, 10000, 50000}

	return
}

// BenchmarkParallelBuild measures the proposer-side cost of turning a tx list into a block:
// a sequential, access-tracked execution of every tx, DAG derivation from the read/write
// sets, and a state commit. It deliberately bypasses buildBlock's fixed block-time window
// (writeTransactions blocks on it on every call), which is a policy delay, not compute, and
// would otherwise both drown the signal and truncate large blocks - see buildProposer.
func BenchmarkParallelBuild(b *testing.B) {
	benchSizes, _ := getBenchData()

	for _, size := range benchSizes {
		b.Run(fmt.Sprintf("txs=%d", size), func(b *testing.B) {
			h, txs := benchScenario(b, 12, size)
			header := h.benchHeader()

			b.ReportAllocs()
			b.ResetTimer()

			for range b.N {
				h.buildProposer(b, header, txs)
			}
		})
	}
}

// BenchmarkParallelVerify measures the verifier-side cost: re-executing a pre-built block
// through the parallel dependency executor using the DAG the proposer derived, then committing.
// It sweeps worker counts because the merge/scheduling cost scales with parallelism.
func BenchmarkParallelVerify(b *testing.B) {
	benchSizes, benchWorkers := getBenchData()

	for _, size := range benchSizes {
		h, txs := benchScenario(b, 12, size)
		block, graph := h.benchBlock(b, txs)

		for _, workers := range benchWorkers {
			b.Run(fmt.Sprintf("txs=%d/workers=%d", size, workers), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()

				for range b.N {
					root := h.verifyParallel(b, block, graph, workers)
					if root != block.Header.StateRoot {
						b.Fatalf("verifier root %s != builder root %s", root, block.Header.StateRoot)
					}
				}
			})
		}
	}
}

// benchHeader synthesizes a minimal block-N+1 header carrying the consensus fields
// BeginTxnWithCustomTxn reads (number, gas limit; base fee/timestamp default to zero). Both the
// build and verify paths use one such header, so their roots are directly comparable.
func (h *parHarness) benchHeader() *types.Header {
	return &types.Header{
		Number:   h.parentHeader.Number + 1,
		GasLimit: h.parentHeader.GasLimit,
	}
}

// benchBlock builds the block and its dependency DAG once, via the same tracked-execution path the
// proposer uses (buildProposer), so the verify benchmark has a block + graph to replay. It bypasses
// the windowed buildBlock, so it works at any block size.
func (h *parHarness) benchBlock(tb testing.TB, txs []*types.Transaction) (*types.Block, [][]uint64) {
	tb.Helper()

	header := h.benchHeader()
	root, graph := h.buildProposer(tb, header, txs)
	header.StateRoot = root

	return &types.Block{Header: header, Transactions: txs}, graph
}

// buildProposer replicates the compute buildBlock does per block - a sequential, access-tracked
// execution of every tx, DAG derivation and a state commit - without the fixed block-time window
// writeTransactions waits out. That window is a wall-clock policy delay, not proposer work, so
// including it would make the benchmark measure a constant sleep instead of the actual cost (and
// at large sizes it also truncates the block once the timer beats execution). Returns the committed
// root and the derived DAG in the same [][]uint64 shape buildBlock packs into the header.
func (h *parHarness) buildProposer(
	tb testing.TB, header *types.Header, txs []*types.Transaction,
) (types.Hash, [][]uint64) {
	tb.Helper()

	transition, err := h.executor.BeginTxnWithCustomTxn(
		h.parentHeader.StateRoot, header, h.proposer, func(s state.Snapshot) state.ITransitionTxn {
			return state.NewTxnWithTxAccessTracker(s, state.TxAccessTrackerFactory(false))
		})
	require.NoError(tb, err)

	depsBuilder := blockstm.NewDepsBuilder()

	for idx, tx := range txs {
		_, err := transition.Write(tx)
		require.NoError(tb, err, "every tx must execute successfully")

		rw := transition.GetTxReadWriteSet(idx)
		require.NoError(tb, depsBuilder.AddTransaction(rw.Index, rw.ReadList, rw.WriteList))
	}

	deps := depsBuilder.GetDeps()
	require.NotNil(tb, deps, "DAG derivation must not error")

	graph := make([][]uint64, len(txs))
	for i := range txs {
		for j := range deps[i] {
			graph[i] = append(graph[i], uint64(j))
		}
	}

	_, root, err := transition.Commit()
	require.NoError(tb, err)

	return root, graph
}

// verifyParallel re-executes the block through the parallel dependency executor with the given
// DAG and worker count, mirroring the verifier path in Executor.ProcessBlock, and returns the
// committed root.
func (h *parHarness) verifyParallel(
	tb testing.TB, block *types.Block, graph [][]uint64, workers int,
) types.Hash {
	tb.Helper()

	pool := state.NewTxDependancyPool(block.Transactions, graph)
	exc := state.NewTxDependancyExecutor(workers, hclog.NewNullLogger())

	tran, _, err := exc.Execute(pool, h.executor, h.parentHeader.StateRoot, block.Header, h.proposer)
	require.NoError(tb, err)

	_, root, err := tran.Commit()
	require.NoError(tb, err)

	return root
}

// benchScenario sets up a parent (Balances + two Router CALL forwarders + a Proxy DELEGATECALL
// forwarder) and generates a deterministic, conflict-rich workload of numTxs txs from numSenders
// senders via sth.RandomizedWorkload - the same mix TestParallel_RandomizedBlock exercises, sized
// for benchmarking.
func benchScenario(tb testing.TB, numSenders, numTxs int) (*parHarness, []*types.Transaction) {
	tb.Helper()

	h := newParHarness(tb)

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

	txs := sth.RandomizedWorkload(1337, numTxs, senders, targets, receivers,
		sth.RandomizedWorkloadContracts{
			Balances: balAddr,
			Router1:  more[0],
			Router2:  more[1],
			Proxy:    more[2],
		})

	return h, txs
}
