package e2e

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/consensus/polybft"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/contractsapi"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/e2e/frameworkV2"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/txrelayer"
	"github.com/0xPolygon/polygon-edge/txrelayerv2"
	"github.com/0xPolygon/polygon-edge/types"
)

func TestE2E_TxPool_Transfer(t *testing.T) {
	// premine an account in the genesis file
	sender, err := wallet.GenerateKey()
	require.NoError(t, err)

	cluster := frameworkV2.NewTestCluster(t, 5,
		frameworkV2.WithPremine(map[types.Address]*big.Int{
			types.Address(sender.Address()): ethgo.Ether(1),
		}),
		frameworkV2.WithBurnContract(&polybft.BurnContractInfo{BlockNumber: 0, Address: types.ZeroAddress}),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	cluster.WaitForReady(t)

	client := cluster.Servers[0].JSONRPC()

	sendAmount := 1
	num := 20

	receivers := []ethgo.Address{}

	for i := 0; i < num; i++ {
		key, err := wallet.GenerateKey()
		require.NoError(t, err)

		receivers = append(receivers, key.Address())
	}

	var wg sync.WaitGroup
	for i := 0; i < num; i++ {
		wg.Add(1)

		go func(i int, to ethgo.Address) {
			defer wg.Done()

			toAddr := types.Address(to)

			txn := &types.Transaction{
				From:  types.Address(sender.Address()),
				To:    &toAddr,
				Gas:   30000,
				Value: big.NewInt(int64(sendAmount)),
				Nonce: uint64(i),
			}

			// Send every second transaction as a dynamic fees one
			if i%2 == 0 {
				txn.Type = types.DynamicFeeTx
				txn.GasFeeCap = big.NewInt(1000000000)
				txn.GasTipCap = big.NewInt(100000000)
			} else {
				txn.Type = types.LegacyTx
				txn.GasPrice = ethgo.Gwei(2)
			}

			sendTransaction(t, client, sender, txn)
		}(i, receivers[i])
	}

	wg.Wait()

	err = cluster.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		for _, receiver := range receivers {
			balance, err := client.GetBalance(types.Address(receiver), jsonrpc.LatestBlockNumberOrHash)
			if err != nil {
				return true
			}

			t.Logf("Balance %s %s", receiver, balance)

			if balance.Uint64() != uint64(sendAmount) {
				return false
			}
		}

		return true
	})
	require.NoError(t, err)
}

// First account send some amount to second one and then second one to third account
func TestE2E_TxPool_Transfer_Linear(t *testing.T) {
	premine, err := wallet.GenerateKey()
	require.NoError(t, err)

	// first account should have some matics premined
	cluster := frameworkV2.NewTestCluster(t, 5,
		frameworkV2.WithPremine(map[types.Address]*big.Int{
			types.Address(premine.Address()): ethgo.Ether(1),
		}),
		frameworkV2.WithBurnContract(&polybft.BurnContractInfo{BlockNumber: 0, Address: types.ZeroAddress}),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	cluster.WaitForReady(t)

	client := cluster.Servers[0].JSONRPC()

	waitUntilBalancesChanged := func(acct ethgo.Address) error {
		err := cluster.WaitUntil(30*time.Second, 2*time.Second, func() bool {
			balance, err := client.GetBalance(types.Address(acct), jsonrpc.LatestBlockNumberOrHash)
			if err != nil {
				return true
			}

			return balance.Cmp(big.NewInt(0)) > 0
		})

		return err
	}

	num := 4
	receivers := []*wallet.Key{
		premine,
	}

	for i := 0; i < num-1; i++ {
		key, err := wallet.GenerateKey()
		assert.NoError(t, err)

		receivers = append(receivers, key)
	}

	const sendAmount = 3000000

	// We are going to fund the accounts in linear fashion:
	// A (premined account) -> B -> C -> D -> E
	// At the end, all of them (except the premined account) will have the same `sendAmount`
	// of balance.
	for i := 1; i < num; i++ {
		// we have to send enough value to account `i` so that it has enough to fund
		// its child i+1 (cover costs + send amounts).
		// This means that since gasCost and sendAmount are fixed, account C must receive gasCost * 2
		// (to cover two more transfers C->D and D->E) + sendAmount * 3 (one bundle for each C,D and E).
		recipient := receivers[i].Address()

		toAddr := types.Address(recipient)

		txn := &types.Transaction{
			Value: big.NewInt(int64(sendAmount * (num - i))),
			To:    &toAddr,
			Gas:   21000,
		}

		if i%2 == 0 {
			txn.Type = types.DynamicFeeTx
			txn.GasFeeCap = big.NewInt(1000000000)
			txn.GasTipCap = big.NewInt(1000000000)
		} else {
			txn.Type = types.LegacyTx
			txn.GasPrice = ethgo.Gwei(1)
		}

		// Add remaining fees to finish the cycle
		gasCostTotal := new(big.Int).Mul(txCost(txn), new(big.Int).SetInt64(int64(num-i-1)))
		txn.Value = txn.Value.Add(txn.Value, gasCostTotal)

		sendTransaction(t, client, receivers[i-1], txn)

		err := waitUntilBalancesChanged(receivers[i].Address())
		require.NoError(t, err)
	}

	for i := 1; i < num; i++ {
		balance, err := client.GetBalance(types.Address(receivers[i].Address()), jsonrpc.LatestBlockNumberOrHash)
		require.NoError(t, err)
		require.Equal(t, uint64(sendAmount), balance.Uint64())
	}
}

func TestE2E_TxPool_TransactionWithHeaderInstructions(t *testing.T) {
	sidechainKey, err := wallet.GenerateKey()
	require.NoError(t, err)

	cluster := frameworkV2.NewTestCluster(t, 4,
		frameworkV2.WithPremine(map[types.Address]*big.Int{
			types.Address(sidechainKey.Address()): ethgo.Ether(1),
		}),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	require.NoError(t, cluster.WaitForBlock(1, 20*time.Second))

	relayer, err := txrelayer.NewTxRelayer(txrelayer.WithIPAddress(cluster.Servers[0].JSONRPCAddr()))
	require.NoError(t, err)

	tx := &ethgo.Transaction{
		Type:  ethgo.TransactionLegacy,
		Input: contractsapi.TestWriteBlockMetadata.Bytecode,
	}

	receipt, err := relayer.SendTransaction(tx, sidechainKey)
	require.NoError(t, err)
	require.Equal(t, uint64(types.ReceiptSuccess), receipt.Status)

	// wait for state root update after contract deployment
	time.Sleep(500 * time.Millisecond)

	receipt, err = ABITransaction(relayer, sidechainKey, contractsapi.TestWriteBlockMetadata,
		receipt.ContractAddress, "init", []interface{}{})
	require.NoError(t, err)
	require.Equal(t, uint64(types.ReceiptSuccess), receipt.Status)

	require.NoError(t, cluster.WaitForBlock(10, 1*time.Minute))
}

// TestE2E_TxPool_BroadcastTransactions sends several transactions (legacy and dynamic fees) to the cluster
// with the 1 amount of eth and checks that all cluster nodes have the recipient balance updated.
func TestE2E_TxPool_BroadcastTransactions(t *testing.T) {
	var (
		sendAmount = ethgo.Ether(1)
	)

	const (
		txNum = 10
	)

	// Create recipient key
	key, err := wallet.GenerateKey()
	assert.NoError(t, err)

	recipient := key.Address()
	toAddr := types.Address(recipient)

	t.Logf("Recipient %s\n", recipient)

	// Create pre-mined balance for sender
	sender, err := wallet.GenerateKey()
	require.NoError(t, err)

	// First account should have some matics premined
	cluster := frameworkV2.NewTestCluster(t, 5,
		frameworkV2.WithPremine(map[types.Address]*big.Int{
			types.Address(sender.Address()): ethgo.Ether(1),
		}),
		frameworkV2.WithBurnContract(&polybft.BurnContractInfo{BlockNumber: 0, Address: types.ZeroAddress}),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	// Wait until the cluster is up and running
	cluster.WaitForReady(t)

	client := cluster.Servers[0].JSONRPC()

	sentAmount := new(big.Int)
	nonce := uint64(0)

	txn := &types.Transaction{
		Value: sendAmount,
		To:    &toAddr,
		Gas:   21000,
	}

	for i := 0; i < txNum; i++ {
		if i%2 == 0 {
			txn.Type = types.DynamicFeeTx
			txn.GasFeeCap = big.NewInt(1000000000)
			txn.GasTipCap = big.NewInt(100000000)
		} else {
			txn.Type = types.LegacyTx
			txn.GasPrice = ethgo.Gwei(2)
		}

		txn.Nonce = nonce

		sendTransaction(t, client, sender, txn)
		sentAmount = sentAmount.Add(sentAmount, txn.Value)
		nonce++
	}

	// Wait until the balance has changed on all nodes in the cluster
	err = cluster.WaitUntil(time.Minute, time.Second*3, func() bool {
		for _, srv := range cluster.Servers {
			balance, err := srv.WaitForNonZeroBalance(types.Address(recipient), time.Second*10)
			assert.NoError(t, err)

			if balance != nil && balance.BitLen() > 0 {
				assert.Equal(t, sentAmount, balance)
			} else {
				return false
			}
		}

		return true
	})
	assert.NoError(t, err)
}

func TestE2E_TxPool_TestSync(t *testing.T) {
	const (
		numOfTxs = 10
	)

	sender, err := crypto.GenerateECDSAKey()
	require.NoError(t, err)

	cluster := frameworkV2.NewTestCluster(t, 3,
		frameworkV2.WithPremine(map[types.Address]*big.Int{
			sender.Address(): ethgo.Ether(1),
		}),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	cluster.WaitForReady(t)

	// Stop the 2nd & 3rd nodes
	cluster.Servers[1].Stop()
	cluster.Servers[2].Stop()

	txRelayer, err := txrelayerv2.NewTxRelayer(
		txrelayerv2.WithIPAddress(cluster.Servers[0].JSONRPCAddr()),
		txrelayerv2.WithoutNonceGet(),
		txrelayerv2.WithNoWaiting(),
	)
	require.NoError(t, err)

	wg := sync.WaitGroup{}
	wg.Add(numOfTxs)

	addr := sender.Address()

	// send transactions to the first node
	for i := range numOfTxs {
		go func(i int) {
			defer wg.Done()

			tx := &types.Transaction{
				From:  addr,
				To:    &addr,
				Value: big.NewInt(int64(i)),
				Gas:   21000,
				Nonce: uint64(i),
			}

			_, err := txRelayer.SendTransaction(tx, sender)
			require.NoError(t, err)
		}(i)
	}

	wg.Wait()

	t.Log("All transactions sent")

	// Restart the second node
	cluster.Servers[1].Start()

	getTxHashMap := func(clt *jsonrpc.EthClient) map[types.Hash]bool {
		content, err := clt.TxPoolContent()
		require.NoError(t, err)

		hashMap := make(map[types.Hash]bool)

		for _, acc := range content.Pending {
			for _, tx := range acc {
				hashMap[tx.Hash] = true
			}
		}

		for _, acc := range content.Queued {
			for _, tx := range acc {
				hashMap[tx.Hash] = true
			}
		}

		return hashMap
	}

	firstHashMap := getTxHashMap(cluster.Servers[0].JSONRPC())

	timeCh, ticker := time.After(2*time.Minute), time.NewTicker(5*time.Second)

	var secondHashMap map[types.Hash]bool

loop:
	for {
		select {
		case <-timeCh:
			t.Fatalf("timeout waiting for txpool sync")
		case <-ticker.C:
			secondHashMap = getTxHashMap(cluster.Servers[1].JSONRPC())
			if len(secondHashMap) == len(firstHashMap) {
				break loop
			}
		}
	}

	for key := range firstHashMap {
		if _, ok := secondHashMap[key]; !ok {
			t.Fatalf("transaction %s not found in the second node", key)
		}
	}

	t.Logf("transaction pool sync successful")
}

// sendTransaction is a helper function which signs transaction with provided private key and sends it
func sendTransaction(t *testing.T, client *jsonrpc.EthClient, sender *wallet.Key, txn *types.Transaction) {
	t.Helper()

	chainID, err := client.ChainID()
	require.NoError(t, err)

	fallbackSigner := crypto.NewEIP155Signer(chainID.Uint64(), true)
	signer := crypto.NewLondonSigner(chainID.Uint64(), true, fallbackSigner)

	var hash types.Hash

	if txn.Type == types.DynamicFeeTx {
		txn.ChainID = chainID
		hash = signer.Hash(txn)
	} else {
		hash = fallbackSigner.Hash(txn)
	}

	sig, err := sender.Sign(hash[:])
	require.NoError(t, err)

	txn.R = new(big.Int).SetBytes(sig[:32])
	txn.S = new(big.Int).SetBytes(sig[32:64])

	if txn.Type == types.DynamicFeeTx {
		// EIP-1559: V is just the parity bit (0 or 1)
		txn.V = big.NewInt(int64(sig[64]))
	} else {
		// Legacy/EIP-155: V must be chain_id * 2 + 35 + parity
		parity := int64(sig[64])
		txn.V = big.NewInt(int64(chainID.Uint64())*2 + 35 + parity)
	}

	txnRlp := txn.MarshalRLPTo(nil)

	_, err = client.SendRawTransaction(txnRlp)
	require.NoError(t, err)
}

func txCost(t *types.Transaction) *big.Int {
	var factor *big.Int

	if t.Type == types.DynamicFeeTx {
		factor = new(big.Int).Set(t.GasFeeCap)
	} else {
		factor = new(big.Int).Set(t.GasPrice)
	}

	return new(big.Int).Mul(factor, new(big.Int).SetUint64(t.Gas))
}
