package e2e

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/e2e/framework"
	"github.com/0xPolygon/polygon-edge/helper/tests"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/txrelayer"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"

	"github.com/Ethernal-Tech/ethgo"
	jsonRpcEthgo "github.com/Ethernal-Tech/ethgo/jsonrpc"
	"github.com/stretchr/testify/require"
)

func TestClusterBlockSync(t *testing.T) {
	const (
		numNonValidators = 2
		desiredHeight    = 10
	)

	runTest := func(t *testing.T, validatorType validators.ValidatorType) {
		t.Helper()

		// Start IBFT cluster (4 Validator + 2 Non-Validator)
		ibftManager := framework.NewIBFTServersManager(
			t,
			IBFTMinNodes+numNonValidators,
			IBFTDirPrefix, func(i int, config *framework.TestServerConfig) {
				config.SetValidatorType(validatorType)

				if i >= IBFTMinNodes {
					// Other nodes should not be in the validator set
					dirPrefix := "polygon-edge-non-validator-"
					config.SetIBFTDirPrefix(dirPrefix)
					config.SetIBFTDir(fmt.Sprintf("%s%d", dirPrefix, i))
				}
			})

		startContext, startCancelFn := context.WithTimeout(context.Background(), time.Minute)
		defer startCancelFn()

		ibftManager.StartServers(startContext)

		servers := make([]*framework.TestServer, 0)
		for i := 0; i < IBFTMinNodes+numNonValidators; i++ {
			servers = append(servers, ibftManager.GetServer(i))
		}
		// All nodes should have mined the same block eventually
		waitErrors := framework.WaitForServersToSeal(servers, desiredHeight)

		if len(waitErrors) != 0 {
			t.Fatalf("Unable to wait for all nodes to seal blocks, %v", waitErrors)
		}
	}

	t.Run("ECDSA", func(t *testing.T) {
		runTest(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		runTest(t, validators.BLSValidatorType)
	})
}

func TestClusterTxPoolSync(t *testing.T) {
	const numOfTxs = 10

	senderKey, senderAddress := tests.GenerateKeyAndAddr(t)
	_, receiverAddress := tests.GenerateKeyAndAddr(t)
	to := ethgo.Address(receiverAddress)
	ethgoSenderKey := framework.NewEthgoKeyWrapper(senderKey, senderAddress)

	// Start IBFT cluster with 3 Validator
	ibftManager := framework.NewIBFTServersManager(t,
		3,
		"prefix",
		func(i int, config *framework.TestServerConfig) {
			config.Premine(senderAddress, framework.EthToWei(10))
			// create subdirectory inside logs directory for each node to avoid conflicts
			// when multiple nodes are writing to the same file
			logsDir := path.Join(config.LogsDir, fmt.Sprintf("node%d", i))
			require.NoError(t, os.Mkdir(logsDir, 0755))

			config.SetLogsDir(logsDir)
		},
	)

	// Start only first server
	ibftManager.GetServer(0).Start(context.Background())

	txRelayer, err := txrelayer.NewTxRelayer(
		txrelayer.WithClient(ibftManager.GetServer(0).JSONRPC()),
		txrelayer.WithNumRetries(-1),
	)
	require.NoError(t, err)

	errs := make([]error, numOfTxs)

	wg := sync.WaitGroup{}
	wg.Add(numOfTxs)

	// send transactions to the first node
	for i := range numOfTxs {
		go func(i int) {
			defer wg.Done()

			_, errs[i] = txRelayer.SendTransaction(&ethgo.Transaction{
				Nonce:    uint64(i),
				GasPrice: uint64(100),
				Value:    big.NewInt(int64(i)),
				Gas:      21000,
				From:     ethgoSenderKey.Address(),
				To:       &to,
				Type:     ethgo.TransactionLegacy,
			}, ethgoSenderKey)
		}(i)
	}

	wg.Wait()

	require.NoError(t, errors.Join(errs...))

	t.Log("All transactions sent")

	// Restart the second node
	ibftManager.GetServer(1).Start(context.Background())

	getTxHashMap := func(clt *jsonRpcEthgo.Client) map[types.Hash]bool {
		var out jsonrpc.ContentResponse

		require.NoError(t, clt.Call("txpool_content", &out))

		hashMap := make(map[types.Hash]bool)

		for _, acc := range out.Pending {
			for _, tx := range acc {
				hashMap[tx.Hash] = true
			}
		}

		for _, acc := range out.Queued {
			for _, tx := range acc {
				hashMap[tx.Hash] = true
			}
		}

		return hashMap
	}

	var (
		secondHashMap, firstHashMap map[types.Hash]bool
		timeCh, ticker              = time.After(2 * time.Minute), time.NewTicker(5 * time.Second)
	)

loop:
	for {
		select {
		case <-timeCh:
			t.Fatalf("timeout waiting for txpool sync")
		case <-ticker.C:
			if len(firstHashMap) != numOfTxs {
				firstHashMap = getTxHashMap(ibftManager.GetServer(0).JSONRPC())
			} else {
				secondHashMap = getTxHashMap(ibftManager.GetServer(1).JSONRPC())
				if len(secondHashMap) == len(firstHashMap) {
					break loop
				}
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
