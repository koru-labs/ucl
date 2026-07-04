package state_test

import (
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/state/statetesthelper"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestExecutor_ProcessBlock_SmartContractDeps(t *testing.T) {
	newProcessBlockExecutor := func(
		t *testing.T, alloc map[types.Address]*chain.GenesisAccount, deployTx *types.Transaction,
	) (*state.Executor, types.Hash) {
		t.Helper()

		mstate := itrie.NewState(itrie.NewMemoryStorage())
		executor := state.NewExecutor(&chain.Params{
			ChainID:      100,
			Forks:        chain.AllForksEnabled,
			BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
		}, mstate, hclog.NewNullLogger())

		executor.GetHash = func(*types.Header) func(uint64) types.Hash {
			return func(uint64) types.Hash { return types.Hash{} }
		}

		root, err := executor.WriteGenesis(alloc, types.ZeroHash)
		require.NoError(t, err)

		header := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}

		tran, err := executor.BeginTxn(root, header, types.ZeroAddress)
		require.NoError(t, err)
		_, err = tran.Write(deployTx)
		require.NoError(t, err)

		_, newRoot, err := tran.Commit()
		require.NoError(t, err)

		return executor, newRoot
	}

	testCases := []struct {
		name string
		deps [][]uint64
	}{
		{
			name: "graph matching TestIBFTBackend_BuildBlock",
			deps: [][]uint64{{}, {}, {0}, {1}, {3}, {}, {}, {5, 6}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			blockCreator := types.StringToAddress("fffaaafffaaafffaffccbbaa3454772")

			alloc, deployTx, callTxs, _, _ := statetesthelper.SetupParallelVerificationData(t)
			block := &types.Block{
				Header:       &types.Header{Number: 2, GasLimit: 5_000_000, Timestamp: 2},
				Transactions: callTxs,
			}

			// --- sequential reference: identical txs, plain in-order execution ---
			seqExecutor, seqGenesisRoot := newProcessBlockExecutor(t, alloc, deployTx)

			seqTxn, _, err := seqExecutor.ProcessBlock(seqGenesisRoot, block, blockCreator)
			require.NoError(t, err)

			_, seqRoot, err := seqTxn.Commit()
			require.NoError(t, err)

			// --- parallel: Executor.ProcessBlock with the tested dependency graph, repeated to
			// surface any scheduling-dependent divergence from the sequential reference ---
			const iterations = 50

			for iter := range iterations {
				parExecutor, parGenesisRoot := newProcessBlockExecutor(t, alloc, deployTx)
				require.Equal(t, seqGenesisRoot, parGenesisRoot)

				parExecutor.GetTxDependencyHook = func(*types.Header) [][]uint64 { return tc.deps }

				parTran, _, err := parExecutor.ProcessBlock(parGenesisRoot, block, blockCreator)
				require.NoError(t, err, "iteration %d: ProcessBlock must not error", iter)

				_, parRoot, err := parTran.Commit()
				require.NoError(t, err)

				require.Equal(t, seqRoot, parRoot,
					"iteration %d: parallel ProcessBlock must match sequential execution", iter)
			}
		})
	}
}

// TestExecutor_ProcessBlock_EOADeps mirrors TestIBFTBackend_BuildBlock "Test EOA": txs 0, 2 and 4
// share receivers[0] (write-write conflict) and tx 3 reuses senders[1]'s next nonce, producing the
// dependency graph {{}, {}, {0}, {1}, {2}} that the proposer computes for this block.
func TestExecutor_ProcessBlock_EOADeps(t *testing.T) {
	newProcessBlockExecutor := func(
		t *testing.T, alloc map[types.Address]*chain.GenesisAccount,
	) (*state.Executor, types.Hash) {
		t.Helper()

		mstate := itrie.NewState(itrie.NewMemoryStorage())
		executor := state.NewExecutor(&chain.Params{
			ChainID:      100,
			Forks:        chain.AllForksEnabled,
			BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
		}, mstate, hclog.NewNullLogger())

		executor.GetHash = func(*types.Header) func(uint64) types.Hash {
			return func(uint64) types.Hash { return types.Hash{} }
		}

		root, err := executor.WriteGenesis(alloc, types.ZeroHash)
		require.NoError(t, err)

		return executor, root
	}

	senders := make([]types.Address, 4)
	for i := range senders {
		senders[i] = types.BytesToAddress([]byte{'e', 'o', 'a', '-', 's', byte(i)})
	}

	receivers := make([]types.Address, 3)
	for i := range receivers {
		receivers[i] = types.BytesToAddress([]byte{'e', 'o', 'a', '-', 'r', byte(i)})
	}

	alloc := map[types.Address]*chain.GenesisAccount{}
	for _, s := range senders {
		alloc[s] = &chain.GenesisAccount{Balance: big.NewInt(1_000_000_000_000)}
	}

	transfers := []struct {
		from  types.Address
		to    types.Address
		nonce uint64
		value int64
	}{
		{senders[0], receivers[0], 0, 100},
		{senders[1], receivers[1], 0, 150},
		{senders[2], receivers[0], 0, 200},
		{senders[1], receivers[2], 1, 250}, // senders[1]'s own next nonce
		{senders[3], receivers[0], 0, 300},
	}

	txs := make([]*types.Transaction, len(transfers))
	for i, tr := range transfers {
		to := tr.to
		txs[i] = &types.Transaction{
			Hash: types.Hash{byte(i + 1)}, From: tr.from, To: &to, Value: big.NewInt(tr.value),
			Gas: 21000, GasPrice: big.NewInt(10000), Nonce: tr.nonce, Type: types.LegacyTx,
			Input: []byte{},
		}
	}

	deps := [][]uint64{{}, {}, {0}, {1}, {2}} // graph matching TestIBFTBackend_BuildBlock "Test EOA"

	blockCreator := types.StringToAddress("fffaaafffaaafffaffccbbaa3454772")
	block := &types.Block{
		Header:       &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1},
		Transactions: txs,
	}

	// --- sequential reference: identical txs, plain in-order execution ---
	seqExecutor, seqGenesisRoot := newProcessBlockExecutor(t, alloc)

	seqTxn, _, err := seqExecutor.ProcessBlock(seqGenesisRoot, block, blockCreator)
	require.NoError(t, err)

	_, seqRoot, err := seqTxn.Commit()
	require.NoError(t, err)

	// --- parallel: repeated to surface any scheduling-dependent divergence ---
	const iterations = 50

	for iter := range iterations {
		parExecutor, parGenesisRoot := newProcessBlockExecutor(t, alloc)
		require.Equal(t, seqGenesisRoot, parGenesisRoot)

		parExecutor.GetTxDependencyHook = func(*types.Header) [][]uint64 { return deps }

		parTran, _, err := parExecutor.ProcessBlock(parGenesisRoot, block, blockCreator)
		require.NoError(t, err, "iteration %d: ProcessBlock must not error", iter)

		_, parRoot, err := parTran.Commit()
		require.NoError(t, err)

		require.Equal(t, seqRoot, parRoot,
			"iteration %d: parallel ProcessBlock must match sequential execution", iter)
	}

	// final balances, same expectations as TestIBFTBackend_BuildBlock "Test EOA"
	tran, err := seqExecutor.BeginTxn(seqRoot, block.Header, types.ZeroAddress)
	require.NoError(t, err)

	require.Equal(t, big.NewInt(600), tran.GetBalance(receivers[0]))
	require.Equal(t, big.NewInt(150), tran.GetBalance(receivers[1]))
	require.Equal(t, big.NewInt(250), tran.GetBalance(receivers[2]))
}
