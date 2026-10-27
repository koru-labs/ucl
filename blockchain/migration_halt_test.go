package blockchain

import (
	"errors"
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/migration/halt"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// A contract answering shouldHalt(h) with h >= 5 (migration/halt's tests explain the bytes).
var haltsFromFive = []byte{0x60, 0x04, 0x35, 0x60, 0x05, 0x11, 0x15, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3}

// With the attestation contract set, block 6 comes after the cutover block 5 (shouldHalt(5) on
// block 4's state) and is refused; block 5 is not. Without it nothing halts.
func TestMigrationHaltRefusesBlocksAfterTheCutover(t *testing.T) {
	contract := types.StringToAddress("0x4a17")

	for _, withRule := range []bool{true, false} {
		params := &chain.Params{Forks: chain.AllForksEnabled, ChainID: 100, BlockGasTarget: defaultBlockGasTarget}
		config := &chain.Chain{
			Genesis: &chain.Genesis{
				GasLimit: 30_000_000,
				BaseFee:  1_000_000_000,
				Alloc:    map[types.Address]*chain.GenesisAccount{contract: {Code: haltsFromFive, Balance: big.NewInt(0)}},
			},
			Params: params,
		}
		st := itrie.NewState(itrie.NewMemoryStorage())
		executor := state.NewExecutor(config.Params, st, hclog.NewNullLogger())
		executor.GetHash = func(*types.Header) state.GetHashByNumber {
			return func(uint64) types.Hash { return types.ZeroHash }
		}

		b, err := newBlockChain(config, executor)
		require.NoError(t, err)

		if withRule {
			b.SetMigrationAttestations(contract)
		}

		// The test chain's genesis writes no state; the blocks carry the contract's.
		root, err := executor.WriteGenesis(config.Genesis.Alloc, types.EmptyRootHash)
		require.NoError(t, err)

		parent := b.Header()
		headers := []*types.Header{}

		for n := uint64(1); n <= 5; n++ {
			h := &types.Header{
				ParentHash: parent.Hash,
				Number:     n,
				StateRoot:  root,
				GasLimit:   parent.GasLimit,
				BaseFee:    parent.BaseFee,
				Sha3Uncles: types.EmptyUncleHash,
				TxRoot:     types.EmptyRootHash,
			}
			h.ComputeHash()
			headers = append(headers, h)
			parent = h
		}

		require.NoError(t, b.WriteHeadersWithBodies(headers))

		halted, err := b.MigrationHalted(5)
		require.NoError(t, err)
		require.False(t, halted, "block 5 is the cutover block itself")

		halted, err = b.MigrationHalted(6)
		require.NoError(t, err)
		require.Equal(t, withRule, halted, "block 6 comes after it")

		next := &types.Header{
			ParentHash: parent.Hash,
			Number:     6,
			StateRoot:  parent.StateRoot,
			GasLimit:   parent.GasLimit,
			BaseFee:    parent.BaseFee,
			Sha3Uncles: types.EmptyUncleHash,
			TxRoot:     types.EmptyRootHash,
		}
		next.ComputeHash()

		err = b.VerifyPotentialBlock(&types.Block{Header: next})
		require.Equal(t, withRule, errors.Is(err, halt.ErrHalted), "verification refuses block 6: %v", err)
	}
}
