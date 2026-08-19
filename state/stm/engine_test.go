package stm_test

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/Ethernal-Tech/ethgo"
	"github.com/hashicorp/go-hclog"
	iradix "github.com/hashicorp/go-immutable-radix"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/blockstm"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/state/statetesthelper"
	"github.com/0xPolygon/polygon-edge/state/stm"
	"github.com/0xPolygon/polygon-edge/types"
)

// newSTMTestExecutor builds a fresh Executor over an empty in-memory trie, ready for
// WriteGenesis/BeginTxn calls.
func newSTMTestExecutor(t *testing.T) *state.Executor {
	t.Helper()

	executor := state.NewExecutor(&chain.Params{
		ChainID:      100,
		Forks:        chain.AllForksEnabled,
		BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
	}, itrie.NewState(itrie.NewMemoryStorage()), hclog.NewNullLogger())

	executor.GetHash = func(*types.Header) func(uint64) types.Hash {
		return func(uint64) types.Hash { return types.Hash{} }
	}

	return executor
}

// runSequential executes txs one at a time via the plain sequential Txn backend and returns
// the committed root and receipts - the reference this test's STM runs must match exactly.
func runSequential(
	t *testing.T, executor *state.Executor, parentRoot types.Hash, header *types.Header, coinbase types.Address,
	txs []*types.Transaction,
) (types.Hash, []*types.Receipt) {
	t.Helper()

	tran, err := executor.BeginTxn(parentRoot, header, coinbase)
	require.NoError(t, err)

	receipts := make([]*types.Receipt, len(txs))

	for i, tx := range txs {
		receipt, err := tran.Write(tx)
		require.NoError(t, err, "tx %d must execute successfully", i)
		receipts[i] = receipt
	}

	_, root, err := tran.Commit()
	require.NoError(t, err)

	return root, receipts
}

// runSTM executes txs as a single STM batch and returns the committed root and the batch
// outcome, mirroring how consensus_backend.go would build one block-wide TxnVerifier-backed
// Transition and merge every batch into it.
func runSTM(
	t *testing.T, executor *state.Executor, workers int, parentRoot types.Hash, header *types.Header,
	coinbase types.Address, txs []*types.Transaction,
) (types.Hash, *stm.BatchOutcome) {
	t.Helper()

	gasLimit := new(uint64)
	*gasLimit = header.GasLimit

	blockMutex := &sync.RWMutex{}
	blockRadix := iradix.New().Txn()

	var dst *state.TxnVerifier

	tran, err := executor.BeginTxnWithCustomTxn(parentRoot, header, coinbase, gasLimit, func(s state.Snapshot) state.ITransitionTxn {
		dst = state.NewTxnVerifier(s, blockMutex, blockRadix)

		return dst
	})
	require.NoError(t, err)

	engine := stm.NewEngine(stm.EngineConfig{Workers: workers}, hclog.NewNullLogger())

	batchGasPool := new(uint64)
	*batchGasPool = header.GasLimit

	outcome, err := engine.RunBatch(context.Background(), executor, header, coinbase, dst, batchGasPool, header.GasLimit, txs)
	require.NoError(t, err)

	tran.AddPendingBalances()

	_, root, err := tran.Commit()
	require.NoError(t, err)

	return root, outcome
}

// runSTMChunked mirrors consensus_backend.go's buildTransactions loop: txs are split into
// fixed-size chunks, each run as its own RunBatch call against the SAME block-wide dst, with the
// DAG fed across chunks using a continuously-incrementing index. Isolates whether a bug is
// specific to splitting one candidate list across multiple batches (as the real gas-limited
// build loop does) rather than a single RunBatch call over the whole list.
func runSTMChunked(
	t *testing.T, executor *state.Executor, workers, chunkSize int, parentRoot types.Hash, header *types.Header,
	coinbase types.Address, txs []*types.Transaction,
) (types.Hash, *blockstm.DepsBuilder, []*types.Transaction, []*types.Receipt) {
	t.Helper()

	gasLimit := new(uint64)
	*gasLimit = header.GasLimit

	blockMutex := &sync.RWMutex{}
	blockRadix := iradix.New().Txn()

	var dst *state.TxnVerifier

	tran, err := executor.BeginTxnWithCustomTxn(parentRoot, header, coinbase, gasLimit, func(s state.Snapshot) state.ITransitionTxn {
		dst = state.NewTxnVerifier(s, blockMutex, blockRadix)

		return dst
	})
	require.NoError(t, err)

	engine := stm.NewEngine(stm.EngineConfig{Workers: workers}, hclog.NewNullLogger())

	batchGasPool := new(uint64)
	*batchGasPool = header.GasLimit

	depsBuilder := blockstm.NewDepsBuilder()
	nextDAGIndex := 0
	blockGasUsed := uint64(0)

	var included []*types.Transaction

	var receipts []*types.Receipt

	for i := 0; i < len(txs); i += chunkSize {
		end := min(i+chunkSize, len(txs))
		chunk := txs[i:end]

		outcome, err := engine.RunBatch(context.Background(), executor, header, coinbase, dst, batchGasPool, *batchGasPool, chunk)
		require.NoError(t, err)
		require.Len(t, outcome.Included, len(chunk), "chunk [%d:%d): every tx must be included", i, end)

		for _, rws := range outcome.ReadWriteSets {
			require.NoError(t, depsBuilder.AddTransaction(nextDAGIndex, rws.ReadList, rws.WriteList))
			nextDAGIndex++
		}

		for _, r := range outcome.Receipts {
			r.CumulativeGasUsed += blockGasUsed
		}

		included = append(included, outcome.Included...)
		receipts = append(receipts, outcome.Receipts...)
		blockGasUsed += outcome.GasUsed
		*batchGasPool -= outcome.GasUsed
	}

	tran.AddPendingBalances()

	_, root, err := tran.Commit()
	require.NoError(t, err)

	return root, depsBuilder, included, receipts
}

// verifyViaDAG builds a dependency DAG from outcome's read/write sets (exactly like
// consensus_backend.go now does) and replays outcome.Included through the DAG-driven parallel
// executor (state.TxDependancyExecutor) - the same one the verification side already uses -
// from the same parent root, returning its committed root. Isolates whether a build/verify root
// mismatch comes from an imprecise DAG (missing edge) rather than the STM engine's own execution.
func verifyViaDAG(
	t *testing.T, executor *state.Executor, workers int, parentRoot types.Hash, header *types.Header,
	coinbase types.Address, outcome *stm.BatchOutcome,
) types.Hash {
	t.Helper()

	depsBuilder := blockstm.NewDepsBuilder()
	for i, rws := range outcome.ReadWriteSets {
		require.NoError(t, depsBuilder.AddTransaction(i, rws.ReadList, rws.WriteList))
	}

	graph := depsBuilder.GetDeps()
	require.NotNil(t, graph)

	pool := state.NewTxDependancyPool(outcome.Included, graph)
	exec := state.NewTxDependancyExecutor(workers, hclog.NewNullLogger())

	verifyTran, _, err := exec.Execute(pool, executor, parentRoot, header, coinbase)
	require.NoError(t, err)

	_, verifyRoot, err := verifyTran.Commit()
	require.NoError(t, err)

	return verifyRoot
}

func requireReceiptsEqual(t *testing.T, seq, got []*types.Receipt) {
	t.Helper()

	require.Len(t, got, len(seq))

	for i := range seq {
		require.Equal(t, seq[i].GasUsed, got[i].GasUsed, "tx %d GasUsed", i)
		require.Equal(t, seq[i].CumulativeGasUsed, got[i].CumulativeGasUsed, "tx %d CumulativeGasUsed", i)
		require.Equal(t, seq[i].Status, got[i].Status, "tx %d Status", i)
		require.Equal(t, seq[i].Logs, got[i].Logs, "tx %d Logs", i)
		require.Equal(t, seq[i].ContractAddress, got[i].ContractAddress, "tx %d ContractAddress", i)
	}
}

// TestEngine_SmartContractCalls differentially tests the STM engine against sequential
// execution over statetesthelper's deploy + 8 conflicting-call fixture (same workload
// TestExecutor_ProcessBlock_SmartContractDeps in the state package uses), across worker counts.
func TestEngine_SmartContractCalls(t *testing.T) {
	alloc, deployTx, callTxs, _, _ := statetesthelper.SetupParallelVerificationData(t)
	coinbase := types.StringToAddress("fffaaafffaaafffaffccbbaa3454772")

	setupParent := func(t *testing.T) (*state.Executor, types.Hash) {
		t.Helper()

		executor := newSTMTestExecutor(t)
		genesisRoot, err := executor.WriteGenesis(alloc, types.ZeroHash)
		require.NoError(t, err)

		header := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}

		tran, err := executor.BeginTxn(genesisRoot, header, types.ZeroAddress)
		require.NoError(t, err)
		_, err = tran.Write(deployTx)
		require.NoError(t, err)

		_, root, err := tran.Commit()
		require.NoError(t, err)

		return executor, root
	}

	header := &types.Header{Number: 2, GasLimit: 5_000_000, Timestamp: 2}

	seqExecutor, seqParent := setupParent(t)
	seqRoot, seqReceipts := runSequential(t, seqExecutor, seqParent, header, coinbase, callTxs)

	for _, workers := range []int{1, 2, 4, 8} {
		for iter := 0; iter < 20; iter++ {
			stmExecutor, stmParent := setupParent(t)
			require.Equal(t, seqParent, stmParent)

			stmRoot, outcome := runSTM(t, stmExecutor, workers, stmParent, header, coinbase, callTxs)

			require.Len(t, outcome.Included, len(callTxs), "workers=%d iter=%d: every tx must be included", workers, iter)
			require.Equal(t, seqRoot, stmRoot, "workers=%d iter=%d: state root must match sequential", workers, iter)
			requireReceiptsEqual(t, seqReceipts, outcome.Receipts)
		}
	}
}

// TestEngine_RandomizedWorkload differentially tests the STM engine against sequential
// execution over a larger, conflict-rich randomized workload (including same-sender nonce
// chains within a single batch), across worker counts and sizes.
func TestEngine_RandomizedWorkload(t *testing.T) {
	coinbase := types.StringToAddress("cccccccccccccccccccccccccccccccccccccccc")

	senders := make([]types.Address, 12)
	for i := range senders {
		senders[i] = types.StringToAddress(fmt.Sprintf("%02x11111111111111111111111111111111111111", i))
	}

	targets := make([]types.Address, 6)
	for i := range targets {
		targets[i] = types.StringToAddress(fmt.Sprintf("%02x22222222222222222222222222222222222222", i))
	}

	receivers := make([]types.Address, 6)
	for i := range receivers {
		receivers[i] = types.StringToAddress(fmt.Sprintf("%02x33333333333333333333333333333333333333", i))
	}

	deployer := types.StringToAddress("d0000000000000000000000000000000000000")

	alloc := statetesthelper.FundedAlloc(append(append([]types.Address{deployer}, senders...), coinbase)...)

	setupParent := func(t *testing.T) (*state.Executor, types.Hash, statetesthelper.RandomizedWorkloadContracts) {
		t.Helper()

		executor := newSTMTestExecutor(t)
		genesisRoot, err := executor.WriteGenesis(alloc, types.ZeroHash)
		require.NoError(t, err)

		header := &types.Header{Number: 1, GasLimit: 30_000_000, Timestamp: 1}

		tran, err := executor.BeginTxn(genesisRoot, header, types.ZeroAddress)
		require.NoError(t, err)

		balRes, err := tran.Apply(statetesthelper.DeployTx(deployer, 0,
			statetesthelper.MustDecodeHex(t, statetesthelper.BalancesInitHex)))
		require.NoError(t, err)
		require.NoError(t, balRes.Err)

		router1Res, err := tran.Apply(statetesthelper.DeployTx(deployer, 1,
			statetesthelper.AppendCtorAddr(statetesthelper.MustDecodeHex(t, statetesthelper.RouterInitHex), balRes.Address)))
		require.NoError(t, err)
		require.NoError(t, router1Res.Err)

		router2Res, err := tran.Apply(statetesthelper.DeployTx(deployer, 2,
			statetesthelper.AppendCtorAddr(statetesthelper.MustDecodeHex(t, statetesthelper.RouterInitHex), balRes.Address)))
		require.NoError(t, err)
		require.NoError(t, router2Res.Err)

		proxyRes, err := tran.Apply(statetesthelper.DeployTx(deployer, 3,
			statetesthelper.AppendCtorAddr(statetesthelper.MustDecodeHex(t, statetesthelper.ProxyInitHex), balRes.Address)))
		require.NoError(t, err)
		require.NoError(t, proxyRes.Err)

		_, root, err := tran.Commit()
		require.NoError(t, err)

		return executor, root, statetesthelper.RandomizedWorkloadContracts{
			Balances: balRes.Address,
			Router1:  router1Res.Address,
			Router2:  router2Res.Address,
			Proxy:    proxyRes.Address,
		}
	}

	header := &types.Header{Number: 2, GasLimit: 30_000_000, Timestamp: 2}

	for _, numTxs := range []int{1, 5, 40} {
		seqExecutor, seqParent, contracts := setupParent(t)
		txs := statetesthelper.RandomizedWorkload(1337, numTxs, senders, targets, receivers, contracts)

		seqRoot, seqReceipts := runSequential(t, seqExecutor, seqParent, header, coinbase, txs)

		for _, workers := range []int{1, 2, 4, 8} {
			stmExecutor, stmParent, _ := setupParent(t)
			require.Equal(t, seqParent, stmParent)

			stmRoot, outcome := runSTM(t, stmExecutor, workers, stmParent, header, coinbase, txs)

			require.Len(t, outcome.Included, len(txs), "numTxs=%d workers=%d: every tx must be included", numTxs, workers)
			require.Equal(t, seqRoot, stmRoot, "numTxs=%d workers=%d: state root must match sequential", numTxs, workers)
			requireReceiptsEqual(t, seqReceipts, outcome.Receipts)

			if numTxs > 1 {
				verifyExecutor, verifyParent, _ := setupParent(t)
				require.Equal(t, seqParent, verifyParent)

				verifyRoot := verifyViaDAG(t, verifyExecutor, workers, verifyParent, header, coinbase, outcome)
				require.Equal(t, stmRoot, verifyRoot,
					"numTxs=%d workers=%d: DAG-driven verify must reproduce the STM-built root", numTxs, workers)
			}
		}
	}
}

// TestEngine_CrossChunkStorageVisibility is a minimal repro: two incBalance calls to the SAME
// target, in TWO SEPARATE chunks of one tx each - the second must see the first's write (cheaper
// gas: already-set slot) rather than treating the slot as pristine.
func TestEngine_CrossChunkStorageVisibility(t *testing.T) {
	deployer := types.StringToAddress("d0000000000000000000000000000000000000")
	caller0 := types.StringToAddress("c0000000000000000000000000000000000000")
	caller1 := types.StringToAddress("c1000000000000000000000000000000000000")
	target := types.StringToAddress("aa00000000000000000000000000000000000a")
	coinbase := types.StringToAddress("cccccccccccccccccccccccccccccccccccccccc")

	alloc := statetesthelper.FundedAlloc(deployer, caller0, caller1, coinbase)

	setupParent := func(t *testing.T) (*state.Executor, types.Hash, types.Address) {
		t.Helper()

		executor := newSTMTestExecutor(t)
		genesisRoot, err := executor.WriteGenesis(alloc, types.ZeroHash)
		require.NoError(t, err)

		header := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}

		tran, err := executor.BeginTxn(genesisRoot, header, types.ZeroAddress)
		require.NoError(t, err)

		res, err := tran.Apply(statetesthelper.DeployTx(deployer, 0,
			statetesthelper.MustDecodeHex(t, statetesthelper.BalancesInitHex)))
		require.NoError(t, err)
		require.NoError(t, res.Err)

		_, root, err := tran.Commit()
		require.NoError(t, err)

		return executor, root, res.Address
	}

	header := &types.Header{Number: 2, GasLimit: 5_000_000, Timestamp: 2}

	_, seqParent, balAddr := setupParent(t)

	txs := []*types.Transaction{
		statetesthelper.CallTx(0x01, caller0, balAddr, 0,
			statetesthelper.CallData("incBalance(address,uint256)",
				statetesthelper.ContractPaddAddress(target), statetesthelper.ContractPaddUint256(1000))),
		statetesthelper.CallTx(0x02, caller1, balAddr, 0,
			statetesthelper.CallData("incBalance(address,uint256)",
				statetesthelper.ContractPaddAddress(target), statetesthelper.ContractPaddUint256(500))),
	}

	seqExecutor, _, _ := setupParent(t)
	seqRoot, seqReceipts := runSequential(t, seqExecutor, seqParent, header, coinbase, txs)

	t.Logf("sequential: tx0 gas=%d tx1 gas=%d", seqReceipts[0].GasUsed, seqReceipts[1].GasUsed)

	stmExecutor, stmParent, _ := setupParent(t)
	require.Equal(t, seqParent, stmParent)

	stmRoot, _, _, receipts := runSTMChunked(t, stmExecutor, 1, 1, stmParent, header, coinbase, txs)

	t.Logf("stm chunked: tx0 gas=%d tx1 gas=%d", receipts[0].GasUsed, receipts[1].GasUsed)

	require.Equal(t, seqRoot, stmRoot, "state root must match sequential")
	requireReceiptsEqual(t, seqReceipts, receipts)
}

// TestEngine_RandomizedWorkload_Chunked mirrors consensus_backend.go's real batching (candidates
// pulled in fixed-size chunks, each its own RunBatch call merged into one block-wide dst) rather
// than TestEngine_RandomizedWorkload's single whole-list RunBatch call, since that's what an
// 8-core build (batch size 32) actually does with a 40-tx block.
func TestEngine_RandomizedWorkload_Chunked(t *testing.T) {
	coinbase := types.StringToAddress("cccccccccccccccccccccccccccccccccccccccc")

	senders := make([]types.Address, 12)
	for i := range senders {
		senders[i] = types.StringToAddress(fmt.Sprintf("%02x11111111111111111111111111111111111111", i))
	}

	targets := make([]types.Address, 6)
	for i := range targets {
		targets[i] = types.StringToAddress(fmt.Sprintf("%02x22222222222222222222222222222222222222", i))
	}

	receivers := make([]types.Address, 6)
	for i := range receivers {
		receivers[i] = types.StringToAddress(fmt.Sprintf("%02x33333333333333333333333333333333333333", i))
	}

	deployer := types.StringToAddress("d0000000000000000000000000000000000000")

	alloc := statetesthelper.FundedAlloc(append(append([]types.Address{deployer}, senders...), coinbase)...)

	setupParent := func(t *testing.T) (*state.Executor, types.Hash, statetesthelper.RandomizedWorkloadContracts) {
		t.Helper()

		executor := newSTMTestExecutor(t)
		genesisRoot, err := executor.WriteGenesis(alloc, types.ZeroHash)
		require.NoError(t, err)

		header := &types.Header{Number: 1, GasLimit: 30_000_000, Timestamp: 1}

		tran, err := executor.BeginTxn(genesisRoot, header, types.ZeroAddress)
		require.NoError(t, err)

		balRes, err := tran.Apply(statetesthelper.DeployTx(deployer, 0,
			statetesthelper.MustDecodeHex(t, statetesthelper.BalancesInitHex)))
		require.NoError(t, err)
		require.NoError(t, balRes.Err)

		router1Res, err := tran.Apply(statetesthelper.DeployTx(deployer, 1,
			statetesthelper.AppendCtorAddr(statetesthelper.MustDecodeHex(t, statetesthelper.RouterInitHex), balRes.Address)))
		require.NoError(t, err)
		require.NoError(t, router1Res.Err)

		router2Res, err := tran.Apply(statetesthelper.DeployTx(deployer, 2,
			statetesthelper.AppendCtorAddr(statetesthelper.MustDecodeHex(t, statetesthelper.RouterInitHex), balRes.Address)))
		require.NoError(t, err)
		require.NoError(t, router2Res.Err)

		proxyRes, err := tran.Apply(statetesthelper.DeployTx(deployer, 3,
			statetesthelper.AppendCtorAddr(statetesthelper.MustDecodeHex(t, statetesthelper.ProxyInitHex), balRes.Address)))
		require.NoError(t, err)
		require.NoError(t, proxyRes.Err)

		_, root, err := tran.Commit()
		require.NoError(t, err)

		return executor, root, statetesthelper.RandomizedWorkloadContracts{
			Balances: balRes.Address,
			Router1:  router1Res.Address,
			Router2:  router2Res.Address,
			Proxy:    proxyRes.Address,
		}
	}

	header := &types.Header{Number: 2, GasLimit: 30_000_000, Timestamp: 2}

	seqExecutor, seqParent, contracts := setupParent(t)
	txs := statetesthelper.RandomizedWorkload(1337, 40, senders, targets, receivers, contracts)

	seqRoot, seqReceipts := runSequential(t, seqExecutor, seqParent, header, coinbase, txs)

	for _, workers := range []int{1, 2, 4, 8} {
		chunkSize := min(max(4*workers, 32), 256)

		stmExecutor, stmParent, _ := setupParent(t)
		require.Equal(t, seqParent, stmParent)

		stmRoot, depsBuilder, included, receipts := runSTMChunked(
			t, stmExecutor, workers, chunkSize, stmParent, header, coinbase, txs)

		require.Len(t, included, len(txs), "workers=%d chunkSize=%d: every tx must be included", workers, chunkSize)
		require.Equal(t, seqRoot, stmRoot, "workers=%d chunkSize=%d: state root must match sequential", workers, chunkSize)
		requireReceiptsEqual(t, seqReceipts, receipts)

		graph := depsBuilder.GetDeps()
		require.NotNil(t, graph)

		verifyExecutor, verifyParent, _ := setupParent(t)
		require.Equal(t, seqParent, verifyParent)

		pool := state.NewTxDependancyPool(included, graph)
		exec := state.NewTxDependancyExecutor(workers, hclog.NewNullLogger())

		verifyTran, _, err := exec.Execute(pool, verifyExecutor, verifyParent, header, coinbase)
		require.NoError(t, err)

		_, verifyRoot, err := verifyTran.Commit()
		require.NoError(t, err)

		require.Equal(t, stmRoot, verifyRoot,
			"workers=%d chunkSize=%d: DAG-driven verify must reproduce the STM-built root", workers, chunkSize)
	}
}

// TestEngine_SelfDestructThenTouch differentially tests STM against sequential execution for:
// a contract writes storage, a second tx selfdestructs it, and a third tx sends it plain value
// afterward (must resurrect it fresh - no code, no storage, just the transferred balance).
func TestEngine_SelfDestructThenTouch(t *testing.T) {
	deployer := types.StringToAddress("d0000000000000000000000000000000000000")
	caller0 := types.StringToAddress("c0000000000000000000000000000000000000")
	caller1 := types.StringToAddress("c1000000000000000000000000000000000000")
	caller2 := types.StringToAddress("c2000000000000000000000000000000000000")
	beneficiary := types.StringToAddress("be000000000000000000000000000000000000")
	coinbase := types.StringToAddress("cccccccccccccccccccccccccccccccccccccccc")

	alloc := statetesthelper.FundedAlloc(deployer, caller0, caller1, caller2, coinbase)

	setupParent := func(t *testing.T) (*state.Executor, types.Hash, types.Address) {
		t.Helper()

		executor := newSTMTestExecutor(t)
		genesisRoot, err := executor.WriteGenesis(alloc, types.ZeroHash)
		require.NoError(t, err)

		header := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}

		tran, err := executor.BeginTxn(genesisRoot, header, types.ZeroAddress)
		require.NoError(t, err)

		res, err := tran.Apply(statetesthelper.DeployTx(deployer, 0,
			statetesthelper.MustDecodeHex(t, statetesthelper.KillableInitHex)))
		require.NoError(t, err)
		require.NoError(t, res.Err)

		_, root, err := tran.Commit()
		require.NoError(t, err)

		return executor, root, res.Address
	}

	header := &types.Header{Number: 2, GasLimit: 5_000_000, Timestamp: 2}

	_, seqParent, kAddr := setupParent(t)

	txs := []*types.Transaction{
		statetesthelper.CallTx(0x71, caller0, kAddr, 0,
			statetesthelper.CallData("setX(uint256)", statetesthelper.ContractPaddUint256(42))),
		statetesthelper.CallTx(0x72, caller1, kAddr, 0,
			statetesthelper.CallData("boom(address)", statetesthelper.ContractPaddAddress(beneficiary))),
		{
			Hash: types.Hash{0x73}, From: caller2, To: &kAddr, Value: big.NewInt(1000),
			Gas: 100_000, GasPrice: ethgo.Gwei(2), Nonce: 0, Type: types.LegacyTx, Input: []byte{},
		},
	}

	seqExecutor, _, _ := setupParent(t)
	seqRoot, seqReceipts := runSequential(t, seqExecutor, seqParent, header, coinbase, txs)

	for _, workers := range []int{1, 2, 4, 8} {
		for iter := 0; iter < 10; iter++ {
			stmExecutor, stmParent, _ := setupParent(t)
			require.Equal(t, seqParent, stmParent)

			stmRoot, outcome := runSTM(t, stmExecutor, workers, stmParent, header, coinbase, txs)

			require.Len(t, outcome.Included, len(txs), "workers=%d iter=%d: every tx must be included", workers, iter)
			require.Equal(t, seqRoot, stmRoot, "workers=%d iter=%d: state root must match sequential", workers, iter)
			requireReceiptsEqual(t, seqReceipts, outcome.Receipts)
		}
	}
}

// TestEngine_MetamorphicCreate2Redeploy differentially tests STM against sequential execution
// for: CREATE2-deploy a contract, write its storage, selfdestruct it, CREATE2-redeploy at the
// SAME address (must only succeed because the selfdestruct already ran), then write storage
// again - the redeployed contract must end up with fresh storage (no leftover slot from before
// the selfdestruct).
func TestEngine_MetamorphicCreate2Redeploy(t *testing.T) {
	deployer := types.StringToAddress("d0000000000000000000000000000000000000")
	callers := make([]types.Address, 5)

	for i := range callers {
		callers[i] = types.StringToAddress(fmt.Sprintf("%x1000000000000000000000000000000000000", i))
	}

	beneficiary := types.StringToAddress("be000000000000000000000000000000000000")
	coinbase := types.StringToAddress("cccccccccccccccccccccccccccccccccccccccc")

	alloc := statetesthelper.FundedAlloc(append(append([]types.Address{}, callers...), deployer, coinbase)...)

	setupParent := func(t *testing.T) (*state.Executor, types.Hash, types.Address) {
		t.Helper()

		executor := newSTMTestExecutor(t)
		genesisRoot, err := executor.WriteGenesis(alloc, types.ZeroHash)
		require.NoError(t, err)

		header := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}

		tran, err := executor.BeginTxn(genesisRoot, header, types.ZeroAddress)
		require.NoError(t, err)

		res, err := tran.Apply(statetesthelper.DeployTx(deployer, 0,
			statetesthelper.MustDecodeHex(t, statetesthelper.Create2FactoryInitHex)))
		require.NoError(t, err)
		require.NoError(t, res.Err)

		_, root, err := tran.Commit()
		require.NoError(t, err)

		return executor, root, res.Address
	}

	header := &types.Header{Number: 2, GasLimit: 5_000_000, Timestamp: 2}

	_, seqParent, factoryAddr := setupParent(t)

	salt := [32]byte{0x01}
	childAddr := crypto.CreateAddress2(factoryAddr, salt, statetesthelper.MustDecodeHex(t, statetesthelper.KillableInitHex))

	txs := []*types.Transaction{
		statetesthelper.CallTx(0xD0, callers[0], factoryAddr, 0, statetesthelper.CallData("make2(bytes32)", salt[:])),
		statetesthelper.CallTx(0xD1, callers[1], childAddr, 0,
			statetesthelper.CallData("setX(uint256)", statetesthelper.ContractPaddUint256(42))),
		statetesthelper.CallTx(0xD2, callers[2], childAddr, 0,
			statetesthelper.CallData("boom(address)", statetesthelper.ContractPaddAddress(beneficiary))),
		statetesthelper.CallTx(0xD3, callers[3], factoryAddr, 0, statetesthelper.CallData("make2(bytes32)", salt[:])),
		statetesthelper.CallTx(0xD4, callers[4], childAddr, 0,
			statetesthelper.CallData("setX(uint256)", statetesthelper.ContractPaddUint256(7))),
	}

	seqExecutor, _, _ := setupParent(t)
	seqRoot, seqReceipts := runSequential(t, seqExecutor, seqParent, header, coinbase, txs)

	for i, r := range seqReceipts {
		require.Equal(t, types.ReceiptSuccess, *r.Status, "sequential reference: tx %d must succeed", i)
	}

	for _, workers := range []int{1, 2, 4, 8} {
		for iter := 0; iter < 10; iter++ {
			stmExecutor, stmParent, _ := setupParent(t)
			require.Equal(t, seqParent, stmParent)

			stmRoot, outcome := runSTM(t, stmExecutor, workers, stmParent, header, coinbase, txs)

			require.Len(t, outcome.Included, len(txs), "workers=%d iter=%d: every tx must be included", workers, iter)
			require.Equal(t, seqRoot, stmRoot, "workers=%d iter=%d: state root must match sequential", workers, iter)
			requireReceiptsEqual(t, seqReceipts, outcome.Receipts)
		}
	}
}
