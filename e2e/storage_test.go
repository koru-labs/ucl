package e2e

import (
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/e2e/framework"
	"github.com/0xPolygon/polygon-edge/helper/tests"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/jsonrpc"
	"github.com/Ethernal-Tech/ethgo/wallet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type txData struct {
	tx    *types.Transaction
	hash  ethgo.Hash
	index int
}

func TestE2E_Storage(t *testing.T) {
	priceLimit := uint64(1)
	senderKey, senderAddress := tests.GenerateKeyAndAddr(t)

	server := framework.NewTestServers(t, 1, func(config *framework.TestServerConfig) {
		config.SetConsensus(framework.ConsensusDev)
		config.SetBlockLimit(2.5 * 21000)
		config.SetDevInterval(2)
		config.SetPriceLimit(&priceLimit)
		config.Premine(senderAddress, framework.EthToWei(100))
		config.SetBaseFee(1)
	})[0]

	client := server.JSONRPC()

	num := 20

	receivers := []types.Address{}

	for i := 0; i < num; i++ {
		key, err := wallet.GenerateKey()
		require.NoError(t, err)

		receivers = append(receivers, types.Address(key.Address()))
	}

	txList := []*txData{}

	for i := 0; i < num; i++ {
		func(i int, to types.Address) {
			txn := &types.Transaction{}

			if i%2 == 0 {
				txn.Type = types.DynamicFeeTx
				txn.GasFeeCap = big.NewInt(1000000000)
				txn.GasTipCap = big.NewInt(100000000)
			} else {
				txn.Type = types.LegacyTx
				txn.GasPrice = ethgo.Gwei(2)
			}

			txn.From = senderAddress
			txn.To = &to
			txn.Gas = 21000
			txn.Value = big.NewInt(int64(i))
			txn.Nonce = uint64(i)

			signedTx, err := server.SignTx(txn, senderKey)
			require.NoError(t, err)

			txHash, err := client.Eth().SendRawTransaction(signedTx.MarshalRLP())
			require.NoError(t, err)

			txList = append(txList, &txData{tx: txn, hash: txHash, index: i})
		}(i, receivers[i])
	}

	err := framework.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		for i, receiver := range receivers {
			balance, err := client.Eth().GetBalance(ethgo.BytesToAddress(receiver.Bytes()), ethgo.Latest)
			if err != nil {
				return true
			}

			t.Logf("Balance %s %s", receiver, balance)

			if balance.Uint64() != uint64(i) {
				return false
			}
		}

		return true
	})
	require.NoError(t, err)

	checkStorage(t, txList, client)
}

func checkStorage(t *testing.T, txs []*txData, client *jsonrpc.Client) {
	t.Helper()

	for _, td := range txs {
		receipt, err := client.Eth().GetTransactionReceipt(td.hash)
		require.NoError(t, err)
		assert.NotNil(t, receipt)

		bn, err := client.Eth().GetBlockByNumber(ethgo.BlockNumber(receipt.BlockNumber), true)
		require.NoError(t, err)
		assert.NotNil(t, bn)

		bh, err := client.Eth().GetBlockByHash(bn.Hash, true)
		require.NoError(t, err)
		assert.NotNil(t, bh)

		if !reflect.DeepEqual(bn, bh) {
			t.Fatal("blocks dont match")
		}

		bt, err := client.Eth().GetTransactionByHash(td.hash)
		require.NoError(t, err)
		assert.NotNil(t, bt)
		assert.Equal(t, td.tx.Value.Uint64(), bt.Value.Uint64())
		assert.Equal(t, td.tx.Gas, bt.Gas)
		assert.Equal(t, td.tx.Nonce, bt.Nonce)

		v, r, s := bt.V, bt.R, bt.S
		assert.NotNil(t, v)
		assert.NotNil(t, r)
		assert.NotNil(t, s)
		assert.Equal(t, td.tx.From.Bytes(), bt.From.Bytes())
		assert.Equal(t, td.tx.To.Bytes(), bt.To.Bytes())

		if td.index%2 == 0 {
			assert.EqualValues(t, types.DynamicFeeTx, bt.Type)
			assert.EqualValues(t, 0, bt.GasPrice) // dynamic txs don't have gasPrice set
			assert.NotNil(t, bt.MaxPriorityFeePerGas)
			assert.NotNil(t, bt.MaxFeePerGas)
			assert.NotNil(t, bt.ChainID)
		} else {
			assert.Equal(t, ethgo.Gwei(2).Uint64(), bt.GasPrice)
		}

		assert.Equal(t, bt.TxnIndex, receipt.TransactionIndex)
		assert.Equal(t, bt.Hash, receipt.TransactionHash)
		assert.Equal(t, bt.BlockHash, receipt.BlockHash)
		assert.Equal(t, bt.BlockNumber, receipt.BlockNumber)
		assert.NotEmpty(t, receipt.LogsBloom)
		assert.Equal(t, bt.To, receipt.To)
	}
}
