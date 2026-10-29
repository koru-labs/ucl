package migration_test

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// gasVector is one transaction and the gas ucl's EVM charges for it: pitau's EVM under its
// `uclRules` gas schedule must charge the same (pitau's ucl-migration.md §9).
type gasVector struct {
	Name     string            `json:"name"`
	Code     string            `json:"code"`
	Storage  map[string]string `json:"storage,omitempty"`
	Create   bool              `json:"create,omitempty"`
	Calldata string            `json:"calldata"`
	GasUsed  uint64            `json:"gas_used"`
	Success  bool              `json:"success"`
}

var (
	gasSender = types.StringToAddress("0x5e0de40000000000000000000000000000000001")
	gasTarget = types.StringToAddress("0x7a46e70000000000000000000000000000000002")
	// gasHelper is an existing contract (code: STOP); gasEOA an existing account without code;
	// gasFresh does not exist.
	gasHelper = types.StringToAddress("0x4e1fe40000000000000000000000000000000003")
	gasEOA    = types.StringToAddress("0xe0a0000000000000000000000000000000000004")
	gasFresh  = types.StringToAddress("0xf4e5400000000000000000000000000000000005")
)

func push20(a types.Address) string { return "73" + hex.EncodeToString(a.Bytes()) }

func call(op string, a types.Address, value string) string {
	// retSize retOffset argsSize argsOffset [value] addr gas OP POP
	code := "6000600060006000"
	if value != "" {
		code += value
	}

	return code + push20(a) + "61ffff" + op + "50"
}

var gasCases = []gasVector{
	{Name: "sload_same_slot_three_times", Code: "6001545060015450600154500" + "0"},
	{Name: "sload_two_slots", Code: "600154506002545000"},
	{Name: "sstore_zero_to_one", Code: "600160055500"},
	{Name: "sstore_one_to_two", Code: "600260055500", Storage: map[string]string{"0x05": "0x01"}},
	{Name: "sstore_one_to_zero", Code: "600060055500", Storage: map[string]string{"0x05": "0x01"}},
	{Name: "sstore_zero_one_zero", Code: "6001600555600060055500"},
	{Name: "sstore_one_two_one", Code: "6002600555600160055500", Storage: map[string]string{"0x05": "0x01"}},
	{Name: "sstore_no_op", Code: "600160055500", Storage: map[string]string{"0x05": "0x01"}},
	{Name: "sload_after_sstore", Code: "60016005556005545000"},
	{Name: "balance_other_twice", Code: push20(gasEOA) + "3150" + push20(gasEOA) + "315000"},
	{Name: "balance_self", Code: "30315000"},
	{Name: "extcodesize_twice", Code: push20(gasHelper) + "3b50" + push20(gasHelper) + "3b5000"},
	{Name: "extcodehash", Code: push20(gasHelper) + "3f5000"},
	{Name: "extcodecopy", Code: "602060006000" + push20(gasHelper) + "3c00"},
	{Name: "call_contract", Code: call("f1", gasHelper, "6000") + "00"},
	{Name: "call_value_to_new_account", Code: call("f1", gasFresh, "6001") + "00"},
	{Name: "call_value_to_eoa", Code: call("f1", gasEOA, "6001") + "00"},
	{Name: "staticcall_contract", Code: call("fa", gasHelper, "") + "00"},
	{Name: "delegatecall_contract", Code: call("f4", gasHelper, "") + "00"},
	{Name: "call_same_contract_twice", Code: call("f1", gasHelper, "6000") + call("f1", gasHelper, "6000") + "00"},
	{Name: "selfdestruct_to_eoa", Code: push20(gasEOA) + "ff"},
	{Name: "transient_storage", Code: "60076001" + "5d" + "6001" + "5c" + "5000"},
	{Name: "push0_and_mcopy", Code: "5f5f5f5e" + "60206000" + "6020" + "5e00"},
	{Name: "clz", Code: "6001" + "1e" + "5000"},
	{Name: "calldata_cost", Code: "00", Calldata: "00010203000000ff"},
	{Name: "create", Create: true, Code: "6000600053600160" + "00f3"},
}

func runGasCase(t *testing.T, c gasVector) (uint64, bool) {
	t.Helper()

	st := itrie.NewState(itrie.NewMemoryStorage())
	exec := state.NewExecutor(&chain.Params{Forks: chain.AllForksEnabled, ChainID: 100}, st, hclog.NewNullLogger())
	exec.GetHash = func(*types.Header) state.GetHashByNumber { return func(uint64) types.Hash { return types.ZeroHash } }

	code, err := hex.DecodeString(c.Code)
	require.NoError(t, err)

	storage := map[types.Hash]types.Hash{}
	for k, v := range c.Storage {
		storage[types.StringToHash(k)] = types.StringToHash(v)
	}

	alloc := map[types.Address]*chain.GenesisAccount{
		gasSender: {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil)},
		gasHelper: {Code: []byte{0x00}, Balance: big.NewInt(0)},
		gasEOA:    {Balance: big.NewInt(1)},
	}
	if !c.Create {
		alloc[gasTarget] = &chain.GenesisAccount{Code: code, Balance: big.NewInt(10), Storage: storage}
	}

	root, err := exec.WriteGenesis(alloc, types.EmptyRootHash)
	require.NoError(t, err)

	header := &types.Header{Number: 1, GasLimit: 30_000_000, BaseFee: 1_000_000_000, Timestamp: 1}

	txn, err := exec.BeginTxn(root, header, types.StringToAddress("0xc0"))
	require.NoError(t, err)

	calldata, err := hex.DecodeString(c.Calldata)
	require.NoError(t, err)

	tx := &types.Transaction{
		Type:     types.LegacyTx,
		From:     gasSender,
		Nonce:    0,
		Gas:      1_000_000,
		GasPrice: big.NewInt(2_000_000_000),
		Value:    big.NewInt(0),
		Input:    calldata,
	}
	if c.Create {
		tx.Input = code
	} else {
		to := gasTarget
		tx.To = &to
	}

	result, err := txn.Apply(tx)
	require.NoError(t, err)

	return result.GasUsed, !result.Failed()
}

// TestGasVectors writes testdata/gas.json with UPDATE_MIGRATION_VECTORS=1 and otherwise checks
// that ucl's EVM still charges what the file says.
func TestGasVectors(t *testing.T) {
	vectors := make([]gasVector, len(gasCases))
	for i, c := range gasCases {
		c.GasUsed, c.Success = runGasCase(t, c)
		vectors[i] = c
	}

	path := filepath.Join("testdata", "gas.json")

	if os.Getenv("UPDATE_MIGRATION_VECTORS") == "1" {
		data, err := json.MarshalIndent(map[string]interface{}{
			"sender": gasSender, "target": gasTarget, "helper": gasHelper, "eoa": gasEOA, "fresh": gasFresh,
			"cases": vectors,
		}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o644))

		return
	}

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var stored struct {
		Cases []gasVector `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &stored))
	require.Equal(t, vectors, stored.Cases)
}
