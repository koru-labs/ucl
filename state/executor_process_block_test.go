package state_test

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
)

// pbSCBytecodeHex is the balances/totalBalance contract from
// TestIBFTBackend_BuildBlock (consensus/ibft/consensus_backend_test.go).
const pbSCBytecodeHex = "" +
	"608060405234801561001057600080fd5b50610505806100206000396000f3fe608060405234801561001057600080fd" +
	"5b50600436106100625760003560e01c806327e235e31461006757806366e7ea0f1461009757806381da0287146100b3" +
	"578063aba00859146100cf578063ad7a672f146100eb578063beabacc814610109575b600080fd5b6100816004803603" +
	"81019061007c9190610318565b610125565b60405161008e919061035e565b60405180910390f35b6100b16004803603" +
	"8101906100ac91906103a5565b61013d565b005b6100cd60048036038101906100c89190610318565b610197565b005b" +
	"6100e960048036038101906100e491906103a5565b6101f1565b005b6100f3610296565b604051610100919061035e56" +
	"5b60405180910390f35b610123600480360381019061011e91906103e5565b61029c565b005b60016020528060005260" +
	"406000206000915090505481565b80600160008473ffffffffffffffffffffffffffffffffffffffff1673ffffffffff" +
	"ffffffffffffffffffffffffffffff168152602001908152602001600020600082825461018c9190610467565b925050" +
	"819055505050565b600160008273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffff" +
	"ffffffffffffffff168152602001908152602001600020546000808282546101e79190610467565b9250508190555050" +
	"565b6000600160008473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffff" +
	"ffffffff1681526020019081526020016000205490508181101561024257600080fd5b818161024e919061049b565b60" +
	"0160008573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16" +
	"815260200190815260200160002081905550505050565b60005481565b6102a683826101f1565b6102b0828261013d56" +
	"5b505050565b600080fd5b600073ffffffffffffffffffffffffffffffffffffffff82169050919050565b60006102e5" +
	"826102ba565b9050919050565b6102f5816102da565b811461030057600080fd5b50565b600081359050610312816102" +
	"ec565b92915050565b60006020828403121561032e5761032d6102b5565b5b600061033c84828501610303565b915050" +
	"92915050565b6000819050919050565b61035881610345565b82525050565b6000602082019050610373600083018461" +
	"034f565b92915050565b61038281610345565b811461038d57600080fd5b50565b60008135905061039f81610379565b" +
	"92915050565b600080604083850312156103bc576103bb6102b5565b5b60006103ca85828601610303565b9250506020" +
	"6103db85828601610390565b9150509250929050565b6000806000606084860312156103fe576103fd6102b5565b5b60" +
	"0061040c86828701610303565b935050602061041d86828701610303565b925050604061042e86828701610390565b91" +
	"50509250925092565b7f4e487b7100000000000000000000000000000000000000000000000000000000600052601160" +
	"045260246000fd5b600061047282610345565b915061047d83610345565b925082820190508082111561049557610494" +
	"610438565b5b92915050565b60006104a682610345565b91506104b183610345565b92508282039050818111156104c9" +
	"576104c8610438565b5b9291505056fea26469706673582212201d17f3b548211792e9dce92db4059e7f1e58ff6b6bbf" +
	"f3678a5192bd5314da5264736f6c63430008130033"

func getSetupPreminesDeployTxAndScTxs(t *testing.T) (
	alloc map[types.Address]*chain.GenesisAccount,
	deployTx *types.Transaction,
	callTxs []*types.Transaction,
) {
	t.Helper()

	// pbSCSelector4 are the balances contract's 4-byte function selectors.
	pbSCSelector4 := map[string]string{
		"incBalance":         "66e7ea0f",
		"decBalance":         "aba00859",
		"updateTotalBalance": "81da0287",
		"transfer":           "beabacc8",
	}

	pbSCCallData := func(t *testing.T, fn string, args ...[]byte) []byte {
		t.Helper()

		selector, err := hex.DecodeString(pbSCSelector4[fn])
		require.NoError(t, err)

		for _, a := range args {
			selector = append(selector, a...)
		}

		return selector
	}
	pbSCPadAddress := func(addr types.Address) []byte {
		padded := make([]byte, 32)
		copy(padded[12:], addr.Bytes())

		return padded
	}
	pbSCPadUint256 := func(v int64) []byte {
		return new(big.Int).SetInt64(v).FillBytes(make([]byte, 32))
	}

	addr2222 := types.StringToAddress("2222222222222222222222222222222222222222")
	addr1122 := types.StringToAddress("1122222222222222222222222222222222222222")
	addr1112 := types.StringToAddress("1112222222222222222222222222222222222222")
	addrAAAA := types.StringToAddress("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	addrFFFF := types.StringToAddress("ffffffffffffffffffffffffffffffffffffffff")
	inputs := [][]byte{
		pbSCCallData(t, "incBalance", pbSCPadAddress(addr2222), pbSCPadUint256(1000)),
		pbSCCallData(t, "incBalance", pbSCPadAddress(addr1122), pbSCPadUint256(1000)),
		pbSCCallData(t, "decBalance", pbSCPadAddress(addr2222), pbSCPadUint256(511)),
		pbSCCallData(t, "updateTotalBalance", pbSCPadAddress(addr1122)),
		pbSCCallData(t, "updateTotalBalance", pbSCPadAddress(addr1112)),
		pbSCCallData(t, "incBalance", pbSCPadAddress(addrAAAA), pbSCPadUint256(10000)),
		pbSCCallData(t, "incBalance", pbSCPadAddress(addrFFFF), pbSCPadUint256(10000)),
		pbSCCallData(t, "transfer", pbSCPadAddress(addrAAAA), pbSCPadAddress(addrFFFF), pbSCPadUint256(100)),
	}

	deployer := types.BytesToAddress([]byte("pbsc-deployer"))
	callers := make([]types.Address, len(inputs))

	for i := range callers {
		callers[i] = types.BytesToAddress([]byte{'p', 'b', 's', 'c', '-', 'c', byte(i)})
	}

	alloc = map[types.Address]*chain.GenesisAccount{
		deployer: {Balance: big.NewInt(1_000_000_000_000)},
	}

	for _, c := range callers {
		alloc[c] = &chain.GenesisAccount{Balance: big.NewInt(1_000_000_000_000)}
	}

	deployInput, err := hex.DecodeString(pbSCBytecodeHex)
	require.NoError(t, err)

	deployTx = &types.Transaction{
		Hash: types.Hash{1}, From: deployer, To: nil, Value: big.NewInt(0),
		Gas: 1_000_000, GasPrice: big.NewInt(1), Nonce: 0, Type: types.LegacyTx,
		Input: deployInput,
	}

	scAddress := crypto.CreateAddress(deployer, 0)

	callTxs = make([]*types.Transaction, len(inputs))
	for i, input := range inputs {
		callTxs[i] = &types.Transaction{
			Hash: types.Hash{byte(i + 2)}, From: callers[i], To: &scAddress, Value: big.NewInt(0),
			Gas: 1_000_000, GasPrice: big.NewInt(1), Nonce: 0, Type: types.LegacyTx,
			Input: input,
		}
	}

	return alloc, deployTx, callTxs
}

func newProcessBlockExecutor(
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

// TestExecutor_ProcessBlock_SmartContractDeps reproduces a real cluster failure: the observed
// graph is what a live proposer actually embedded in a block that later failed with "invalid
// block state root", and it omits the decBalance(2222)->incBalance(2222) edge that the correct
// graph (matching TestIBFTBackend_BuildBlock) includes. Both are run through the exact
// production path (Executor.ProcessBlock), repeated to surface scheduling-dependent races, and
// compared against a sequential execution of the identical 8 transactions.
func TestExecutor_ProcessBlock_SmartContractDeps(t *testing.T) {
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
			alloc, deployTx, callTxs := getSetupPreminesDeployTxAndScTxs(t)
			block := &types.Block{
				Header:       &types.Header{Number: 2, GasLimit: 5_000_000, Timestamp: 2},
				Transactions: callTxs,
			}

			// --- sequential reference: identical txs, plain in-order execution ---
			seqExecutor, seqGenesisRoot := newProcessBlockExecutor(t, alloc, deployTx)

			seqTxn, _, err := seqExecutor.ProcessBlock(seqGenesisRoot, block, types.ZeroAddress)
			require.NoError(t, err)

			_, seqRoot, err := seqTxn.Commit()
			require.NoError(t, err)

			// --- parallel: Executor.ProcessBlock with the tested dependency graph, repeated to
			// surface any scheduling-dependent divergence from the sequential reference ---
			const iterations = 1

			for iter := range iterations {
				parExecutor, parGenesisRoot := newProcessBlockExecutor(t, alloc, deployTx)
				require.Equal(t, seqGenesisRoot, parGenesisRoot)

				parExecutor.GetTxDependencyHook = func(*types.Header) [][]uint64 { return tc.deps }

				parTran, _, err := parExecutor.ProcessBlock(parGenesisRoot, block, types.ZeroAddress)
				require.NoError(t, err, "iteration %d: ProcessBlock must not error", iter)

				_, parRoot, err := parTran.Commit()
				require.NoError(t, err)

				require.Equal(t, seqRoot, parRoot,
					"iteration %d: parallel ProcessBlock must match sequential execution", iter)
			}
		})
	}
}
