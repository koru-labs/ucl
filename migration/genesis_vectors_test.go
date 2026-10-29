package migration_test

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/command/genesis"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
)

// genesisVector is a ucl genesis file and ucl's block 0 for it: pitau's genesis converter
// (`pitau-node migration convert`) must compute the same header and hash from the file alone.
type genesisVector struct {
	Genesis   json.RawMessage `json:"genesis"`
	Header    string          `json:"header"`
	Hash      string          `json:"hash"`
	StateRoot string          `json:"state_root"`
}

// TestGenesisVector writes testdata/ucl_genesis.json with UPDATE_MIGRATION_VECTORS=1, and otherwise
// checks the file still holds what ucl computes. The genesis file is the `ucl genesis` command's own
// output for a mainnet-shaped network (IBFT, every fork from block 0, 1 gwei base fee, no burn
// contract), plus one contract account with storage, a zero slot among it, which the command cannot
// write. Block 0 is computed as a ucl node computes it at startup: the allocations written with
// `Executor.WriteGenesis` (server/server.go), then the IBFT header hash.
func TestGenesisVector(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "genesis.json")

	validators := make([]string, 0, 4)
	for i := byte(0); i < 4; i++ {
		validators = append(validators, key(t, i).Address().String())
	}

	cmd := genesis.GetCommand()

	args := append(make([]string, 0, 20+4*len(validators)),
		"--dir", path,
		"--name", "Koru",
		"--consensus", "ibft",
		"--chain-id", "1618033",
		"--block-gas-limit", "200000000",
		"--base-fee-config", "1000000000",
		"--epoch-size", "604800",
		"--bootnode", "/ip4/127.0.0.1/tcp/1478/p2p/16Uiu2HAmJxxH1tScDX2rLGSU9exnuvZKNM9SoK3v315azp68DLPW",
		// A funded account, and one premined with nothing, as mainnet's faucet is: ucl stores it.
		"--premine", "0x523dFd72b47053370eD998999016Ac4375d2d0Cb:50000000000000000000000000000",
		"--premine", "0xd10207393acbc919caAA7E8A1B040397009bB6B8:0",
	)
	for _, v := range validators {
		args = append(args, "--validators", v, "--premine", v+":1000000000000000000000000")
	}

	cmd.SetArgs(args)
	require.NoError(t, cmd.Execute())

	c, err := chain.ImportFromFile(path)
	require.NoError(t, err)

	c.Genesis.Alloc[types.StringToAddress("0x5707")] = &chain.GenesisAccount{
		Code:    []byte{0x60, 0x00, 0x54, 0x00}, // PUSH1 0 SLOAD STOP
		Balance: big.NewInt(7),
		Nonce:   1,
		Storage: map[types.Hash]types.Hash{
			types.StringToHash("0x01"): types.StringToHash("0x2a"),
			types.StringToHash("0x02"): types.ZeroHash,
		},
	}

	raw, err := json.MarshalIndent(c, "", "    ")
	require.NoError(t, err)

	// What the node does with the file it reads.
	c, err = chain.ImportFromFile(writeTemp(t, raw))
	require.NoError(t, err)

	storage := itrie.NewMemoryStorage()
	executor := state.NewExecutor(c.Params, itrie.NewState(storage), hclog.NewNullLogger())
	root, err := executor.WriteGenesis(c.Genesis.Alloc, types.ZeroHash)
	require.NoError(t, err)

	c.Genesis.StateRoot = root
	header := c.Genesis.GenesisHeader()

	// IBFT's hash: ECDSA extra data, so any ECDSA signer computes it.
	hash, err := key(t, 0).CalculateHeaderHash(header)
	require.NoError(t, err)

	file := genesisVector{
		Genesis:   raw,
		Header:    hex.EncodeToString(header.MarshalRLP()),
		Hash:      hash.String(),
		StateRoot: root.String(),
	}

	// The validators are in block 0's extra data, as IBFT reads them.
	extra, err := key(t, 0).GetIBFTExtra(header)
	require.NoError(t, err)
	require.Equal(t, 4, extra.Validators.Len())

	out := filepath.Join("testdata", "ucl_genesis.json")

	if os.Getenv("UPDATE_MIGRATION_VECTORS") == "1" {
		data, err := json.MarshalIndent(file, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out, append(data, '\n'), 0o600))

		return
	}

	data, err := os.ReadFile(out)
	require.NoError(t, err)

	var stored genesisVector
	require.NoError(t, json.Unmarshal(data, &stored))
	require.Equal(t, file.Hash, stored.Hash)
	require.Equal(t, file.Header, stored.Header)
	require.Equal(t, file.StateRoot, stored.StateRoot)
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "genesis.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	return path
}
