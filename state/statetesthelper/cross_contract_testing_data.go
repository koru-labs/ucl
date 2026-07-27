package statetesthelper

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/stretchr/testify/require"
)

// Selector returns the 4-byte function selector for a Solidity signature, e.g.
// Selector("incBalance(address,uint256)").
func Selector(sig string) []byte {
	return crypto.Keccak256([]byte(sig))[:4]
}

// CallData builds calldata: 4-byte selector for sig followed by the (already 32-byte padded) args.
func CallData(sig string, args ...[]byte) []byte {
	data := Selector(sig)
	for _, a := range args {
		data = append(data, a...)
	}

	return data
}

// MustDecodeHex decodes a hex string, failing the test on error.
func MustDecodeHex(tb testing.TB, s string) []byte {
	tb.Helper()

	b, err := hex.DecodeString(s)
	require.NoError(tb, err)

	return b
}

// AppendCtorAddr appends an ABI-encoded (32-byte padded) address constructor argument to init bytecode.
func AppendCtorAddr(initCode []byte, addr types.Address) []byte {
	out := make([]byte, 0, len(initCode)+32)
	out = append(out, initCode...)
	out = append(out, ContractPaddAddress(addr)...)

	return out
}

// DeployTx builds a contract-creation transaction (To == nil) for the given deployer/nonce.
func DeployTx(deployer types.Address, nonce uint64, initCode []byte) *types.Transaction {
	return &types.Transaction{
		Hash: types.Hash{byte(nonce + 1), 0xDE}, From: deployer, To: nil, Value: big.NewInt(0),
		Gas: 2_000_000, GasPrice: ethgo.Gwei(2), Nonce: nonce, Type: types.LegacyTx,
		Input: initCode,
	}
}

// CallTx builds a message-call transaction to `to` with the given calldata.
func CallTx(
	hashSeed byte, from types.Address, to types.Address, nonce uint64, input []byte,
) *types.Transaction {
	dst := to

	return &types.Transaction{
		Hash: types.Hash{hashSeed}, From: from, To: &dst, Value: big.NewInt(0),
		Gas: 1_000_000, GasPrice: ethgo.Gwei(2), Nonce: nonce, Type: types.LegacyTx,
		Input: input,
	}
}

// FundedAlloc returns a genesis allocation giving each address 100 ether.
func FundedAlloc(addrs ...types.Address) map[types.Address]*chain.GenesisAccount {
	alloc := map[types.Address]*chain.GenesisAccount{}
	for _, a := range addrs {
		alloc[a] = &chain.GenesisAccount{Balance: ethgo.Ether(100)}
	}

	return alloc
}
