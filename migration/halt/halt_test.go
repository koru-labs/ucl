package halt

import (
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

var (
	// Answers shouldHalt(h) with h >= 5, whatever the selector: PUSH1 4 CALLDATALOAD PUSH1 5 GT
	// ISZERO PUSH1 0 MSTORE PUSH1 32 PUSH1 0 RETURN.
	haltsFromFive = []byte{0x60, 0x04, 0x35, 0x60, 0x05, 0x11, 0x15, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}
	// PUSH1 0 DUP1 REVERT.
	reverts = []byte{0x60, 0x00, 0x80, 0xfd}

	haltsAddr   = types.StringToAddress("0x4a17")
	revertsAddr = types.StringToAddress("0x4a18")
	missingAddr = types.StringToAddress("0x4a19")
)

func executorWith(t *testing.T) (*state.Executor, *types.Header) {
	t.Helper()

	st := itrie.NewState(itrie.NewMemoryStorage())
	exec := state.NewExecutor(&chain.Params{Forks: chain.AllForksEnabled, ChainID: 100}, st, hclog.NewNullLogger())
	exec.GetHash = func(*types.Header) state.GetHashByNumber { return func(uint64) types.Hash { return types.ZeroHash } }

	root, err := exec.WriteGenesis(map[types.Address]*chain.GenesisAccount{
		haltsAddr:   {Code: haltsFromFive, Balance: big.NewInt(0)},
		revertsAddr: {Code: reverts, Balance: big.NewInt(0)},
	}, types.EmptyRootHash)
	require.NoError(t, err)

	return exec, &types.Header{StateRoot: root, GasLimit: 30_000_000, BaseFee: 1_000_000_000, Number: 3}
}

func TestShouldHaltReadsTheContractOnTheParentsState(t *testing.T) {
	exec, parent := executorWith(t)

	halted, err := ShouldHalt(exec, haltsAddr, parent, 4)
	require.NoError(t, err)
	require.False(t, halted)

	halted, err = ShouldHalt(exec, haltsAddr, parent, 5)
	require.NoError(t, err)
	require.True(t, halted, "the contract's answer, with the fee checks off")
}

func TestNoContractOrARevertMeansNoHalt(t *testing.T) {
	exec, parent := executorWith(t)

	for _, contract := range []types.Address{revertsAddr, missingAddr} {
		halted, err := ShouldHalt(exec, contract, parent, 100)
		require.NoError(t, err)
		require.False(t, halted, "%s", contract)
	}
}
