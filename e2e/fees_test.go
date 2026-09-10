package e2e

import (
	"math/big"
	"testing"
	"time"

	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/wallet"
	"github.com/stretchr/testify/require"

	ibftSigner "github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/consensus/polybft"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/e2e/framework"
	"github.com/0xPolygon/polygon-edge/e2e/frameworkV2"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	"github.com/0xPolygon/polygon-edge/helper/tests"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
)

// These tests pin down where transaction fees go. They cover both fee models the genesis
// command can produce:
//
//   - no burn contract (mainnet model): the block proposer receives gasUsed * effectiveGasPrice,
//     i.e. the base fee AND the priority tip, and nothing is burned;
//   - burn contract configured: the proposer receives only gasUsed * tip and the burn contract
//     receives gasUsed * baseFee.
//
// Every scenario sends legacy (type 0) and dynamic-fee (type 2) transactions, a contract
// deployment and a contract call, and also checks that EIP-2930 (type 1) transactions,
// which this node does not implement, are rejected cleanly by eth_sendRawTransaction.

const (
	feeTestBaseFee = uint64(1_000_000_000) // 1 gwei
	feeTestGas     = uint64(1_000_000)
)

// feeScenario describes one transaction of the fee workload.
type feeScenario struct {
	name  string
	build func(chainID *big.Int, nonce uint64, to *types.Address, input []byte) *types.Transaction
}

// feeWorkload is the list of transaction shapes every fee test sends. Each shape is sent
// twice: once as a plain transfer and once as a contract call (see runFeeWorkload).
var feeWorkload = []feeScenario{
	{
		name: "legacy at base fee",
		build: func(chainID *big.Int, nonce uint64, to *types.Address, input []byte) *types.Transaction {
			return &types.Transaction{
				Type: types.LegacyTx, Nonce: nonce, To: to, Input: input, Gas: feeTestGas,
				GasPrice: new(big.Int).SetUint64(feeTestBaseFee), Value: big.NewInt(1),
			}
		},
	},
	{
		name: "legacy above base fee",
		build: func(chainID *big.Int, nonce uint64, to *types.Address, input []byte) *types.Transaction {
			return &types.Transaction{
				Type: types.LegacyTx, Nonce: nonce, To: to, Input: input, Gas: feeTestGas,
				GasPrice: new(big.Int).SetUint64(3 * feeTestBaseFee), Value: big.NewInt(1),
			}
		},
	},
	{
		name: "dynamic fee with tip",
		build: func(chainID *big.Int, nonce uint64, to *types.Address, input []byte) *types.Transaction {
			return &types.Transaction{
				Type: types.DynamicFeeTx, ChainID: chainID, Nonce: nonce, To: to, Input: input, Gas: feeTestGas,
				GasFeeCap: new(big.Int).SetUint64(3 * feeTestBaseFee),
				GasTipCap: new(big.Int).SetUint64(feeTestBaseFee / 2),
				Value:     big.NewInt(1),
			}
		},
	},
	{
		name: "dynamic fee zero tip",
		build: func(chainID *big.Int, nonce uint64, to *types.Address, input []byte) *types.Transaction {
			return &types.Transaction{
				Type: types.DynamicFeeTx, ChainID: chainID, Nonce: nonce, To: to, Input: input, Gas: feeTestGas,
				GasFeeCap: new(big.Int).SetUint64(feeTestBaseFee),
				GasTipCap: big.NewInt(0),
				Value:     big.NewInt(1),
			}
		},
	},
	{
		name: "dynamic fee capped below base+tip",
		build: func(chainID *big.Int, nonce uint64, to *types.Address, input []byte) *types.Transaction {
			// effective price = min(feeCap, baseFee + tipCap) = feeCap, so the tip is squeezed to feeCap-baseFee
			return &types.Transaction{
				Type: types.DynamicFeeTx, ChainID: chainID, Nonce: nonce, To: to, Input: input, Gas: feeTestGas,
				GasFeeCap: new(big.Int).SetUint64(feeTestBaseFee + feeTestBaseFee/4),
				GasTipCap: new(big.Int).SetUint64(feeTestBaseFee),
				Value:     big.NewInt(1),
			}
		},
	},
}

// effectiveGasPrice mirrors EIP-1559: legacy pays its gas price, dynamic pays min(feeCap, baseFee+tipCap).
func effectiveGasPrice(txn *types.Transaction, baseFee uint64) *big.Int {
	if txn.Type != types.DynamicFeeTx {
		return new(big.Int).Set(txn.GasPrice)
	}

	price := new(big.Int).Add(new(big.Int).SetUint64(baseFee), txn.GasTipCap)
	if price.Cmp(txn.GasFeeCap) > 0 {
		price.Set(txn.GasFeeCap)
	}

	return price
}

// signAndSend signs txn with the given key, submits it via eth_sendRawTransaction and waits
// for the receipt. It deliberately does not use the tx relayer so the fee fields reach the
// node exactly as the test wrote them.
func signAndSend(t *testing.T, client *jsonrpc.EthClient, key *wallet.Key, txn *types.Transaction) *ethgo.Receipt {
	t.Helper()

	chainID, err := client.ChainID()
	require.NoError(t, err)

	fallbackSigner := crypto.NewEIP155Signer(chainID.Uint64(), true)
	londonSigner := crypto.NewLondonSigner(chainID.Uint64(), true, fallbackSigner)

	var hash types.Hash

	if txn.Type == types.DynamicFeeTx {
		txn.ChainID = chainID
		hash = londonSigner.Hash(txn)
	} else {
		hash = fallbackSigner.Hash(txn)
	}

	sig, err := key.Sign(hash[:])
	require.NoError(t, err)

	txn.R = new(big.Int).SetBytes(sig[:32])
	txn.S = new(big.Int).SetBytes(sig[32:64])

	if txn.Type == types.DynamicFeeTx {
		txn.V = big.NewInt(int64(sig[64]))
	} else {
		txn.V = big.NewInt(int64(chainID.Uint64())*2 + 35 + int64(sig[64]))
	}

	txHash, err := client.SendRawTransaction(txn.MarshalRLPTo(nil))
	require.NoError(t, err)

	var receipt *ethgo.Receipt

	require.Eventually(t, func() bool {
		receipt, err = client.GetTransactionReceipt(txHash)

		return err == nil && receipt != nil
	}, time.Minute, 500*time.Millisecond, "receipt for %s (%s) not received", txHash, txn.Type)

	return receipt
}

// blockProposer recovers the IBFT proposer of a block from the proposer seal in extraData.
// The header's Miner field is always the zero address on this chain, so it cannot be used.
func blockProposer(t *testing.T, client *jsonrpc.EthClient, hash types.Hash) types.Address {
	t.Helper()

	block, err := client.GetBlockByHash(hash, false)
	require.NoError(t, err)
	require.NotNil(t, block)

	extra := &ibftSigner.IstanbulExtra{
		Validators:           validators.NewECDSAValidatorSet(),
		CommittedSeals:       &ibftSigner.SerializedSeal{},
		ParentCommittedSeals: &ibftSigner.SerializedSeal{},
	}
	require.NoError(t, extra.UnmarshalRLP(block.Header.ExtraData[ibftSigner.IstanbulExtraVanity:]))

	proposer, err := framework.EcrecoverFromBlockhash(block.Header.Hash, extra.ProposerSeal)
	require.NoError(t, err)

	return proposer
}

// requireBigEqual compares big integers numerically; require.Equal would deep-compare the
// internal representation, where a zero produced by Sub differs from big.NewInt(0).
func requireBigEqual(t *testing.T, want, got *big.Int, msg string, args ...interface{}) {
	t.Helper()

	if want.Cmp(got) != 0 {
		t.Fatalf(msg+": expected %s, got %s", append(args, want, got)...)
	}
}

func balanceAt(t *testing.T, client *jsonrpc.EthClient, addr types.Address, block uint64) *big.Int {
	t.Helper()

	bn := jsonrpc.BlockNumber(block)
	bal, err := client.GetBalance(addr, jsonrpc.BlockNumberOrHash{BlockNumber: &bn})
	require.NoError(t, err)

	return bal
}

// feeExpectation is what a scenario expects each party to gain from one transaction.
type feeExpectation struct {
	proposerGain func(gasUsed uint64, effective *big.Int, baseFee uint64) *big.Int
	burnGain     func(gasUsed uint64, baseFee uint64) *big.Int
}

var (
	// proposer receives the whole fee, nothing is burned
	expectProposerCollectsAll = feeExpectation{
		proposerGain: func(gasUsed uint64, effective *big.Int, _ uint64) *big.Int {
			return new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), effective)
		},
		burnGain: func(uint64, uint64) *big.Int { return big.NewInt(0) },
	}
	// proposer receives only the tip, the base fee goes to the burn contract
	expectBaseFeeBurned = feeExpectation{
		proposerGain: func(gasUsed uint64, effective *big.Int, baseFee uint64) *big.Int {
			tip := new(big.Int).Sub(effective, new(big.Int).SetUint64(baseFee))

			return new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), tip)
		},
		burnGain: func(gasUsed uint64, baseFee uint64) *big.Int {
			return new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), new(big.Int).SetUint64(baseFee))
		},
	}
)

// runFeeWorkload sends every scenario twice (transfer, then contract call) and verifies,
// for each mined transaction, the proposer's and the burn address's balance change between
// the parent block and the block containing the transaction. Returns the number of blocks
// that carried transactions.
func runFeeWorkload(
	t *testing.T,
	cluster *frameworkV2.TestCluster,
	sender *wallet.Key,
	burnAddr types.Address,
	expect feeExpectation,
) {
	t.Helper()

	client := cluster.Servers[0].JSONRPC()
	senderAddr := types.Address(sender.Address())

	chainID, err := client.ChainID()
	require.NoError(t, err)

	validatorSet, err := cluster.Servers[0].IBFTGetValidators()
	require.NoError(t, err)
	require.NotEmpty(t, validatorSet)

	// Deploy the sample contract with a legacy tx so the contract-call half of the workload has a target.
	nonce, err := client.GetNonce(senderAddr, jsonrpc.LatestBlockNumberOrHash)
	require.NoError(t, err)

	deploy := &types.Transaction{
		Type: types.LegacyTx, Nonce: nonce, Input: hex.MustDecodeHex(sampleByteCode), Gas: feeTestGas,
		GasPrice: new(big.Int).SetUint64(2 * feeTestBaseFee), Value: big.NewInt(0),
	}
	deployReceipt := signAndSend(t, client, sender, deploy)
	require.Equal(t, uint64(types.ReceiptSuccess), deployReceipt.Status, "contract deployment failed")
	contractAddr := types.Address(deployReceipt.ContractAddress)
	verifyFeeSplit(t, client, validatorSet, burnAddr, deploy, deployReceipt, expect)

	_, receiverAddr := tests.GenerateKeyAndAddr(t)
	setA1 := framework.MethodSig("setA1")

	for _, sc := range feeWorkload {
		for _, target := range []struct {
			name  string
			to    types.Address
			input []byte
		}{
			{"transfer", receiverAddr, nil},
			{"contract call", contractAddr, setA1},
		} {
			t.Run(sc.name+"/"+target.name, func(t *testing.T) {
				nonce, err := client.GetNonce(senderAddr, jsonrpc.LatestBlockNumberOrHash)
				require.NoError(t, err)

				to := target.to
				txn := sc.build(chainID, nonce, &to, target.input)
				receipt := signAndSend(t, client, sender, txn)
				require.Equal(t, uint64(types.ReceiptSuccess), receipt.Status)

				verifyFeeSplit(t, client, validatorSet, burnAddr, txn, receipt, expect)
			})
		}
	}
}

// verifyFeeSplit checks one mined transaction: the recovered proposer is a validator and
// gained exactly what the fee model prescribes, the burn address gained exactly its share,
// and no other validator's balance moved in that block.
func verifyFeeSplit(
	t *testing.T,
	client *jsonrpc.EthClient,
	validatorSet []types.Address,
	burnAddr types.Address,
	txn *types.Transaction,
	receipt *ethgo.Receipt,
	expect feeExpectation,
) {
	t.Helper()

	header, err := client.GetHeaderByNumber(jsonrpc.BlockNumber(receipt.BlockNumber))
	require.NoError(t, err)
	require.Equal(t, feeTestBaseFee, header.BaseFee, "base fee of block %d", receipt.BlockNumber)

	// the workload runs sequentially, so the block must hold exactly this transaction
	receipts, err := client.GetBlockReceipts(jsonrpc.BlockNumber(receipt.BlockNumber))
	require.NoError(t, err)
	require.Len(t, receipts, 1, "expected exactly one transaction in block %d", receipt.BlockNumber)

	proposer := blockProposer(t, client, types.Hash(receipt.BlockHash))
	require.Contains(t, validatorSet, proposer, "block proposer is not a validator")

	effective := effectiveGasPrice(txn, header.BaseFee)
	wantProposer := expect.proposerGain(receipt.GasUsed, effective, header.BaseFee)
	wantBurn := expect.burnGain(receipt.GasUsed, header.BaseFee)

	parent := receipt.BlockNumber - 1

	for _, v := range validatorSet {
		gain := new(big.Int).Sub(balanceAt(t, client, v, receipt.BlockNumber), balanceAt(t, client, v, parent))

		if v == proposer {
			requireBigEqual(t, wantProposer, gain,
				"proposer %s gain for %s tx (gasUsed=%d, effective=%s, baseFee=%d)",
				v, txn.Type, receipt.GasUsed, effective, header.BaseFee)
		} else {
			require.Zerof(t, gain.Sign(), "non-proposing validator %s balance changed by %s", v, gain)
		}
	}

	burnGain := new(big.Int).Sub(balanceAt(t, client, burnAddr, receipt.BlockNumber), balanceAt(t, client, burnAddr, parent))
	requireBigEqual(t, wantBurn, burnGain, "burn address gain for %s tx", txn.Type)

	// the sender must have paid exactly value + gasUsed * effective
	senderAddr := types.Address(receipt.From)
	paid := new(big.Int).Sub(balanceAt(t, client, senderAddr, parent), balanceAt(t, client, senderAddr, receipt.BlockNumber))
	wantPaid := new(big.Int).Add(txn.Value, new(big.Int).Mul(new(big.Int).SetUint64(receipt.GasUsed), effective))
	requireBigEqual(t, wantPaid, paid, "sender charge for %s tx", txn.Type)
}

func newFeeTestCluster(t *testing.T, sender *wallet.Key, opts ...frameworkV2.ClusterOption) *frameworkV2.TestCluster {
	t.Helper()

	base := []frameworkV2.ClusterOption{
		frameworkV2.WithPremine(map[types.Address]*big.Int{
			types.Address(sender.Address()): ethgo.Ether(100),
		}),
		frameworkV2.WithBaseFeeConfig("1000000000:2:8"),
		frameworkV2.WithBlockTime(time.Second),
		frameworkV2.WithEpochSize(10),
		frameworkV2.WithBootnodeCount(1),
	}

	cluster := frameworkV2.NewTestCluster(t, 4, append(base, opts...)...)
	cluster.WaitForReady(t)

	return cluster
}

// TestE2E_Fees_ProposerCollectsWholeFee is the mainnet model: London enabled, no burn
// contract, base fee fixed. Validators start unfunded and must end up with exactly the fees
// of the blocks they proposed; nothing is burned.
func TestE2E_Fees_ProposerCollectsWholeFee(t *testing.T) {
	sender, err := wallet.GenerateKey()
	require.NoError(t, err)

	cluster := newFeeTestCluster(t, sender,
		frameworkV2.WithFixedBaseFee(),
		frameworkV2.WithPriceLimit(1),
	)
	defer cluster.Stop()

	client := cluster.Servers[0].JSONRPC()

	validatorSet, err := cluster.Servers[0].IBFTGetValidators()
	require.NoError(t, err)

	for _, v := range validatorSet {
		require.Zero(t, balanceAt(t, client, v, 0).Sign(), "validator %s must start with zero balance", v)
	}

	runFeeWorkload(t, cluster, sender, types.ZeroAddress, expectProposerCollectsAll)

	// The base fee must not have moved from genesis while blocks were mostly empty.
	latest, err := client.BlockNumber()
	require.NoError(t, err)

	history, err := client.FeeHistory(latest, jsonrpc.BlockNumber(latest), nil)
	require.NoError(t, err)

	for i, bf := range history.BaseFee {
		require.Equal(t, feeTestBaseFee, bf, "base fee drifted at fee-history index %d", i)
	}
}

// TestE2E_Fees_BurnContractReceivesBaseFee keeps the classic EIP-1559 split: proposer gets
// the tip, the burn contract gets the base fee.
func TestE2E_Fees_BurnContractReceivesBaseFee(t *testing.T) {
	sender, err := wallet.GenerateKey()
	require.NoError(t, err)

	burnAddr := types.StringToAddress("0x000000000000000000000000000000000000dead")

	cluster := newFeeTestCluster(t, sender,
		frameworkV2.WithFixedBaseFee(),
		frameworkV2.WithBurnContract(&polybft.BurnContractInfo{BlockNumber: 0, Address: burnAddr}),
	)
	defer cluster.Stop()

	runFeeWorkload(t, cluster, sender, burnAddr, expectBaseFeeBurned)
}

// TestE2E_Fees_DynamicBaseFeeStillAdjusts guards the non-fixed path: without
// --with-base-fee-fixed the base fee must decay on an idle chain, and fees must still be
// paid out consistently with the base fee of the block in which each transaction landed.
func TestE2E_Fees_DynamicBaseFeeStillAdjusts(t *testing.T) {
	sender, err := wallet.GenerateKey()
	require.NoError(t, err)

	cluster := newFeeTestCluster(t, sender)
	defer cluster.Stop()

	client := cluster.Servers[0].JSONRPC()

	require.NoError(t, cluster.WaitForBlock(12, time.Minute))

	genesisFee, err := client.GetHeaderByNumber(jsonrpc.BlockNumber(0))
	require.NoError(t, err)
	require.Equal(t, feeTestBaseFee, genesisFee.BaseFee)

	later, err := client.GetHeaderByNumber(jsonrpc.BlockNumber(12))
	require.NoError(t, err)
	require.Less(t, later.BaseFee, genesisFee.BaseFee, "base fee did not decay on an idle chain")
	require.NotZero(t, later.BaseFee, "base fee dropped to zero")

	// A transaction priced at the current base fee (below the genesis base fee) must be
	// accepted and the proposer must receive gasUsed * effectiveGasPrice at that block's base fee.
	senderAddr := types.Address(sender.Address())
	_, receiverAddr := tests.GenerateKeyAndAddr(t)

	nonce, err := client.GetNonce(senderAddr, jsonrpc.LatestBlockNumberOrHash)
	require.NoError(t, err)

	txn := &types.Transaction{
		Type: types.DynamicFeeTx, Nonce: nonce, To: &receiverAddr, Gas: feeTestGas, Value: big.NewInt(1),
		GasFeeCap: new(big.Int).SetUint64(later.BaseFee),
		GasTipCap: big.NewInt(0),
	}
	receipt := signAndSend(t, client, sender, txn)
	require.Equal(t, uint64(types.ReceiptSuccess), receipt.Status)

	header, err := client.GetHeaderByNumber(jsonrpc.BlockNumber(receipt.BlockNumber))
	require.NoError(t, err)
	require.LessOrEqual(t, header.BaseFee, later.BaseFee)

	validatorSet, err := cluster.Servers[0].IBFTGetValidators()
	require.NoError(t, err)

	proposer := blockProposer(t, client, types.Hash(receipt.BlockHash))
	require.Contains(t, validatorSet, proposer)

	gain := new(big.Int).Sub(balanceAt(t, client, proposer, receipt.BlockNumber), balanceAt(t, client, proposer, receipt.BlockNumber-1))
	want := new(big.Int).Mul(new(big.Int).SetUint64(receipt.GasUsed), effectiveGasPrice(txn, header.BaseFee))
	requireBigEqual(t, want, gain, "proposer did not receive the whole fee at the block's base fee")
}

// TestE2E_Fees_UnsupportedTxTypesRejected documents that EIP-2930 (type 1) transactions are
// not part of this node's transaction set: eth_sendRawTransaction must reject them with a
// clear error instead of mis-decoding them, while types 0 and 2 keep working.
func TestE2E_Fees_UnsupportedTxTypesRejected(t *testing.T) {
	sender, err := wallet.GenerateKey()
	require.NoError(t, err)

	cluster := newFeeTestCluster(t, sender, frameworkV2.WithFixedBaseFee())
	defer cluster.Stop()

	client := cluster.Servers[0].JSONRPC()
	senderAddr := types.Address(sender.Address())
	_, receiverAddr := tests.GenerateKeyAndAddr(t)

	// Build a valid legacy transaction, then re-wrap its payload as a typed envelope with
	// an unsupported type byte. The node must refuse it at decode time.
	nonce, err := client.GetNonce(senderAddr, jsonrpc.LatestBlockNumberOrHash)
	require.NoError(t, err)

	legacy := &types.Transaction{
		Type: types.LegacyTx, Nonce: nonce, To: &receiverAddr, Gas: feeTestGas, Value: big.NewInt(1),
		GasPrice: new(big.Int).SetUint64(feeTestBaseFee),
	}
	receipt := signAndSend(t, client, sender, legacy) // also proves type 0 works and consumes the nonce
	require.Equal(t, uint64(types.ReceiptSuccess), receipt.Status)

	for _, unsupported := range []byte{0x01, 0x03, 0x04} {
		raw := append([]byte{unsupported}, legacy.MarshalRLPTo(nil)...)

		_, err := client.SendRawTransaction(raw)
		require.Errorf(t, err, "type %d transaction must be rejected", unsupported)
		require.Containsf(t, err.Error(), "unknown transaction type", "unexpected error for type %d", unsupported)
	}

	// A type 2 transaction with the same nonce must still be accepted afterwards.
	nonce, err = client.GetNonce(senderAddr, jsonrpc.LatestBlockNumberOrHash)
	require.NoError(t, err)

	dynamic := &types.Transaction{
		Type: types.DynamicFeeTx, Nonce: nonce, To: &receiverAddr, Gas: feeTestGas, Value: big.NewInt(1),
		GasFeeCap: new(big.Int).SetUint64(2 * feeTestBaseFee), GasTipCap: new(big.Int).SetUint64(feeTestBaseFee),
	}
	receipt = signAndSend(t, client, sender, dynamic)
	require.Equal(t, uint64(types.ReceiptSuccess), receipt.Status)
}
