package statetesthelper

import (
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"math/rand"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/wallet"
	"github.com/stretchr/testify/require"
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

func ContractPaddAddress(addr types.Address) []byte {
	padded := make([]byte, 32)
	copy(padded[12:], addr.Bytes())

	return padded
}

func ContractPaddUint256(v uint64) []byte {
	return new(big.Int).SetUint64(v).FillBytes(make([]byte, 32))
}

// depScBalanceSlot mirrors TestIBFTBackend_BuildBlock's storage-slot math for the
// `mapping(address => uint256) balances` field, which lives at slot 1.
func ScBalanceSlot(addr types.Address, slot uint64) types.Hash {
	return types.Hash(crypto.Keccak256(
		append(ContractPaddAddress(addr), ContractPaddUint256(slot)...),
	))
}

func SetupParallelVerificationData(t *testing.T) (
	alloc map[types.Address]*chain.GenesisAccount,
	deployTx *types.Transaction,
	callTxs []*types.Transaction,
	accounts []*wallet.Key,
	expectedBalances map[types.Address]int64,
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

	addr2222 := types.StringToAddress("2222222222222222222222222222222222222222")
	addr1122 := types.StringToAddress("1122222222222222222222222222222222222222")
	addr1112 := types.StringToAddress("1112222222222222222222222222222222222222")
	addrAAAA := types.StringToAddress("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	addrFFFF := types.StringToAddress("ffffffffffffffffffffffffffffffffffffffff")
	inputs := [][]byte{
		pbSCCallData(t, "incBalance", ContractPaddAddress(addr2222), ContractPaddUint256(1000)),
		pbSCCallData(t, "incBalance", ContractPaddAddress(addr1122), ContractPaddUint256(1000)),
		pbSCCallData(t, "decBalance", ContractPaddAddress(addr2222), ContractPaddUint256(511)),
		pbSCCallData(t, "updateTotalBalance", ContractPaddAddress(addr1122)),
		pbSCCallData(t, "updateTotalBalance", ContractPaddAddress(addr1112)),
		pbSCCallData(t, "incBalance", ContractPaddAddress(addrAAAA), ContractPaddUint256(10000)),
		pbSCCallData(t, "incBalance", ContractPaddAddress(addrFFFF), ContractPaddUint256(10000)),
		pbSCCallData(t, "transfer", ContractPaddAddress(addrAAAA), ContractPaddAddress(addrFFFF), ContractPaddUint256(100)),
	}
	expectedBalances = map[types.Address]int64{
		addr1122: 1000,
		addr2222: 489,
		addrAAAA: 9900,
		addrFFFF: 10100,
	}

	accounts = make([]*wallet.Key, len(inputs)+1)

	for i := range accounts {
		w, err := wallet.GenerateKey()
		require.NoError(t, err)

		accounts[i] = w
	}

	deployer := types.Address(accounts[0].Address())
	callers := make([]types.Address, len(inputs))

	for i := range callers {
		callers[i] = types.Address(accounts[i+1].Address())
	}

	alloc = map[types.Address]*chain.GenesisAccount{
		deployer: {Balance: ethgo.Ether(100)},
	}

	for _, c := range callers {
		alloc[c] = &chain.GenesisAccount{Balance: ethgo.Ether(100)}
	}

	deployInput, err := hex.DecodeString(pbSCBytecodeHex)
	require.NoError(t, err)

	deployTx = &types.Transaction{
		Hash: types.Hash{1}, From: deployer, To: nil, Value: big.NewInt(0),
		Gas: 500_000, GasPrice: ethgo.Gwei(2), Nonce: 0, Type: types.LegacyTx,
		Input: deployInput,
	}

	scAddress := crypto.CreateAddress(deployer, 0)

	callTxs = make([]*types.Transaction, len(inputs))
	for i, input := range inputs {
		callTxs[i] = &types.Transaction{
			Hash: types.Hash{byte(i + 2)}, From: callers[i], To: &scAddress, Value: big.NewInt(0),
			Gas: 500_000, GasPrice: ethgo.Gwei(2), Nonce: 0, Type: types.LegacyTx,
			Input: input,
		}
	}

	return alloc, deployTx, callTxs, accounts, expectedBalances
}

// RandomizedWorkloadContracts holds the deployed contract addresses a randomized workload calls
// into: a Balances contract, two Router CALL forwarders and a Proxy DELEGATECALL forwarder, all
// pointing at the same Balances instance.
type RandomizedWorkloadContracts struct {
	Balances types.Address
	Router1  types.Address
	Router2  types.Address
	Proxy    types.Address
}

// RandomizedWorkload generates a deterministic (seeded), conflict-rich block workload of numTxs
// txs from the given funded senders: direct Balances calls, Router CALLs, Proxy DELEGATECALLs,
// in-contract transfers, EOA transfers and zero-value touches (EIP-158 empty-account path), with
// senders reusing nonces across the block. tx0 seeds targets[0] with a large balance so the
// in-contract transfers (max 10 per tx) can never underflow while numTxs*10 stays below it.
// Every tx gets a unique index-derived hash. targets index contract-storage slots; receivers are
// EOA transfer destinations.
func RandomizedWorkload(
	rndSeed int64,
	numTxs int,
	senders, targets, receivers []types.Address,
	contracts RandomizedWorkloadContracts,
) []*types.Transaction {
	rnd := rand.New(rand.NewSource(rndSeed)) //nolint:gosec
	nonces := map[types.Address]uint64{}

	nextSender := func() (types.Address, uint64) {
		s := senders[rnd.Intn(len(senders))]
		n := nonces[s]
		nonces[s]++

		return s, n
	}

	transferTx := func(from, to types.Address, nonce uint64, value uint64) *types.Transaction {
		dst := to

		return &types.Transaction{
			From: from, To: &dst, Value: new(big.Int).SetUint64(value),
			Gas: 21000, GasPrice: ethgo.Gwei(2), Nonce: nonce, Type: types.LegacyTx, Input: []byte{},
		}
	}

	txs := make([]*types.Transaction, 0, numTxs)

	// seed targets[0] first so in-contract transfers below can never underflow
	s, n := nextSender()
	txs = append(txs, CallTx(0x00, s, contracts.Balances, n,
		CallData("incBalance(address,uint256)", ContractPaddAddress(targets[0]), ContractPaddUint256(1_000_000))))

	for i := 1; i < numTxs; i++ {
		s, n := nextSender()
		target := targets[rnd.Intn(len(targets))]
		amount := uint64(rnd.Intn(100) + 1)

		switch rnd.Intn(7) {
		case 0: // direct incBalance
			txs = append(txs, CallTx(0, s, contracts.Balances, n,
				CallData("incBalance(address,uint256)", ContractPaddAddress(target), ContractPaddUint256(amount))))
		case 1: // incBalance through router1 (CALL)
			txs = append(txs, CallTx(0, s, contracts.Router1, n,
				CallData("routerInc(address,uint256)", ContractPaddAddress(target), ContractPaddUint256(amount))))
		case 2: // incBalance through router2 (CALL)
			txs = append(txs, CallTx(0, s, contracts.Router2, n,
				CallData("routerInc(address,uint256)", ContractPaddAddress(target), ContractPaddUint256(amount))))
		case 3: // incBalance through proxy (DELEGATECALL - writes proxy's own storage)
			txs = append(txs, CallTx(0, s, contracts.Proxy, n,
				CallData("pinc(address,uint256)", ContractPaddAddress(target), ContractPaddUint256(amount))))
		case 4: // in-contract transfer from the seeded target (never underflows)
			txs = append(txs, CallTx(0, s, contracts.Balances, n,
				CallData("transfer(address,address,uint256)",
					ContractPaddAddress(targets[0]), ContractPaddAddress(target),
					ContractPaddUint256(uint64(rnd.Intn(10)+1)))))
		case 5: // EOA transfer
			txs = append(txs, transferTx(s, receivers[rnd.Intn(len(receivers))], n, amount))
		case 6: // zero-value touch of a receiver (EIP-158 empty-account path)
			txs = append(txs, transferTx(s, receivers[rnd.Intn(len(receivers))], n, 0))
		}
	}

	// unique index-derived hashes: the single-seed-byte hashes the tx helpers produce collide
	// past 256 txs, and nothing downstream may key on a duplicate
	for i, tx := range txs {
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(i+1))

		tx.Hash = types.BytesToHash(buf)
	}

	return txs
}
