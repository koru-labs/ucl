package e2e

import (
	"math/big"
	"testing"
	"time"

	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/wallet"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/consensus/polybft"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/e2e/frameworkV2"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/state/statetesthelper"
	"github.com/0xPolygon/polygon-edge/types"
)

// TestE2E_Parallel_Verification replays TestIBFTBackend_BuildBlock's two dependency-graph
// scenarios (a shared contract, and EOA transfers with nonce/recipient conflicts) against a
// live cluster: the proposer must compute the tx dependencies and every other validator must
// independently re-verify the block through the parallel dependency executor.
func TestE2E_Parallel_Verification(t *testing.T) {
	alloc, deployTx, txs, accounts, _ := statetesthelper.SetupParallelVerificationData(t)

	premine := make(map[types.Address]*big.Int, len(alloc))

	for addr, val := range alloc {
		premine[addr] = val.Balance
	}

	cluster := frameworkV2.NewTestCluster(t, 5,
		frameworkV2.WithBlockTime(time.Second*4), // all txs must end in same block
		frameworkV2.WithPremine(premine),
		frameworkV2.WithBurnContract(
			&polybft.BurnContractInfo{BlockNumber: 0, Address: types.ZeroAddress}),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	cluster.WaitForReady(t)

	client := cluster.Servers[0].JSONRPC()

	t.Run("smart contracts", func(t *testing.T) {
		verifyParallelSmartContract(t, cluster, client, deployTx, txs, accounts)
	})

	t.Run("EOA transfers", func(t *testing.T) {
		verifyParallelEOATransfers(t, cluster, client, accounts)
	})
}

func verifyParallelSmartContract(
	t *testing.T,
	cluster *frameworkV2.TestCluster,
	client *jsonrpc.EthClient,
	deployTx *types.Transaction,
	txs []*types.Transaction,
	accounts []*wallet.Key,
) {
	t.Helper()

	scAddress := crypto.CreateAddress(types.Address(accounts[0].Address()), 0)

	sendTransaction(t, client, accounts[0], deployTx)

	require.NoError(t, cluster.WaitUntil(30*time.Second, 2*time.Second, func() bool {
		code, err := client.GetCode(scAddress, jsonrpc.LatestBlockNumberOrHash)

		return err == nil && code != "0x"
	}))

	b, err := client.GetBlockByNumber(jsonrpc.LatestBlockNumber, false)
	require.NoError(t, err)

	for i, tx := range txs {
		sendTransaction(t, client, accounts[i+1], tx)

		tx.ComputeHash(b.Number())
	}

	require.NoError(t, cluster.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		for _, tx := range txs {
			recpt, err := client.GetTransactionReceipt(tx.Hash)
			if err != nil || recpt == nil {
				return false
			}
		}

		return true
	}))
}

func verifyParallelEOATransfers(
	t *testing.T,
	cluster *frameworkV2.TestCluster,
	client *jsonrpc.EthClient,
	senders []*wallet.Key,
) {
	t.Helper()

	rcvs := make([]types.Address, 3)

	for i := range rcvs {
		w, err := wallet.GenerateKey()
		require.NoError(t, err)

		rcvs[i] = types.Address(w.Address())
	}

	transfers := []struct {
		sender *wallet.Key
		to     types.Address
		nonce  uint64
		value  int64
	}{
		{senders[1], rcvs[0], 1, 100},
		{senders[2], rcvs[1], 1, 150},
		{senders[3], rcvs[0], 1, 200},
		{senders[2], rcvs[2], 2, 250}, // senders[1]'s own next nonce
		{senders[4], rcvs[0], 1, 300},
	}
	txs := make([]*types.Transaction, len(transfers))

	b, err := client.GetBlockByNumber(jsonrpc.LatestBlockNumber, false)
	require.NoError(t, err)

	for i, tr := range transfers {
		txs[i] = &types.Transaction{
			Nonce:    tr.nonce,
			To:       &tr.to,
			Value:    big.NewInt(tr.value),
			Gas:      21000,
			GasPrice: ethgo.Gwei(1),
			Type:     types.LegacyTx,
		}

		sendTransaction(t, client, tr.sender, txs[i])
		txs[i].ComputeHash(b.Number())
	}

	require.NoError(t, cluster.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		for _, tx := range txs {
			recpt, err := client.GetTransactionReceipt(tx.Hash)
			if err != nil || recpt == nil {
				return false
			}
		}

		return true
	}))
}
