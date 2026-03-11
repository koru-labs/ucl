package e2e

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/txrelayer"

	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/txpool"
	"github.com/Ethernal-Tech/ethgo"
	jsonRpcEthgo "github.com/Ethernal-Tech/ethgo/jsonrpc"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/e2e/framework"
	"github.com/0xPolygon/polygon-edge/helper/tests"
	txpoolOp "github.com/0xPolygon/polygon-edge/txpool/proto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/golang/protobuf/ptypes/any"
	"github.com/stretchr/testify/assert"
)

var (
	oneEth = framework.EthToWei(1)
	signer = crypto.NewSigner(chain.AllForksEnabled.At(0), 100)
)

type generateTxReqParams struct {
	nonce         uint64
	referenceAddr types.Address
	referenceKey  *ecdsa.PrivateKey
	toAddress     types.Address
	gasPrice      *big.Int
	gasFeeCap     *big.Int
	gasTipCap     *big.Int
	value         *big.Int
	t             *testing.T
}

func generateTx(params generateTxReqParams) *types.Transaction {
	unsignedTx := &types.Transaction{
		Nonce: params.nonce,
		From:  params.referenceAddr,
		To:    &params.toAddress,
		Gas:   1000000,
		Value: params.value,
		V:     big.NewInt(27), // it is necessary to encode in rlp
	}

	if params.gasPrice != nil {
		unsignedTx.Type = types.LegacyTx
		unsignedTx.GasPrice = params.gasPrice
	} else {
		unsignedTx.Type = types.DynamicFeeTx
		unsignedTx.GasFeeCap = params.gasFeeCap
		unsignedTx.GasTipCap = params.gasTipCap
	}

	signedTx, err := signer.SignTx(unsignedTx, params.referenceKey)
	require.NoError(params.t, err, "Unable to sign transaction")

	return signedTx
}

func generateReq(params generateTxReqParams) *txpoolOp.AddTxnReq {
	msg := &txpoolOp.AddTxnReq{
		Raw: &any.Any{
			Value: generateTx(params).MarshalRLP(),
		},
		From: types.ZeroAddress.String(),
	}

	return msg
}

func TestTxPool_ErrorCodes(t *testing.T) {
	gasPrice := big.NewInt(1000000000)
	gasFeeCap := big.NewInt(1000000000)
	gasTipCap := big.NewInt(100000000)
	devInterval := 5

	testTable := []struct {
		name           string
		defaultBalance *big.Int
		txValue        *big.Int
		gasPrice       *big.Int
		gasFeeCap      *big.Int
		gasTipCap      *big.Int
		expectedError  error
	}{
		{
			// Test scenario:
			// Add legacy tx with nonce 0
			// -> Check if tx has been parsed
			// Add tx with nonce 0
			// -> tx shouldn't be added, since the nonce is too low
			name:           "ErrNonceTooLow - legacy",
			defaultBalance: framework.EthToWei(10),
			txValue:        oneEth,
			gasPrice:       gasPrice,
			expectedError:  txpool.ErrNonceTooLow,
		},
		{
			// Test scenario:
			// Add dynamic fee tx with nonce 0
			// -> Check if tx has been parsed
			// Add tx with nonce 0
			// -> tx shouldn't be added, since the nonce is too low
			name:           "ErrNonceTooLow - dynamic fees",
			defaultBalance: framework.EthToWei(10),
			txValue:        oneEth,
			gasFeeCap:      gasFeeCap,
			gasTipCap:      gasTipCap,
			expectedError:  txpool.ErrNonceTooLow,
		},
		{
			// Test scenario:
			// Add legacy tx with insufficient funds
			// -> Tx should be discarded because of low funds
			name:           "ErrInsufficientFunds - legacy",
			defaultBalance: framework.EthToWei(1),
			txValue:        framework.EthToWei(5),
			gasPrice:       gasPrice,
			expectedError:  txpool.ErrInsufficientFunds,
		},
		{
			// Test scenario:
			// Add dynamic fee tx with insufficient funds
			// -> Tx should be discarded because of low funds
			name:           "ErrInsufficientFunds - dynamic fee",
			defaultBalance: framework.EthToWei(1),
			txValue:        framework.EthToWei(5),
			gasFeeCap:      gasFeeCap,
			gasTipCap:      gasTipCap,
			expectedError:  txpool.ErrInsufficientFunds,
		},
	}

	for _, testCase := range testTable {
		t.Run(testCase.name, func(t *testing.T) {
			referenceKey, referenceAddr := tests.GenerateKeyAndAddr(t)

			// Set up the test server
			srvs := framework.NewTestServers(t, 1, func(config *framework.TestServerConfig) {
				config.SetConsensus(framework.ConsensusDev)
				config.SetDevInterval(devInterval)
				config.Premine(referenceAddr, testCase.defaultBalance)
			})
			srv := srvs[0]

			// TxPool client
			clt := srv.TxnPoolOperator()
			toAddress := types.StringToAddress("1")

			// Add the initial transaction
			addReq := generateReq(generateTxReqParams{
				nonce:         0,
				referenceAddr: referenceAddr,
				referenceKey:  referenceKey,
				toAddress:     toAddress,
				gasPrice:      testCase.gasPrice,
				gasFeeCap:     testCase.gasFeeCap,
				gasTipCap:     testCase.gasTipCap,
				value:         testCase.txValue,
				t:             t,
			})

			addResponse, addErr := clt.AddTxn(context.Background(), addReq)

			if errors.Is(testCase.expectedError, txpool.ErrNonceTooLow) {
				if addErr != nil {
					t.Fatalf("Unable to add txn, %v", addErr)
				}

				// Wait for the state transition to be executed
				receiptCtx, waitCancelFn := context.WithTimeout(
					context.Background(),
					time.Duration(devInterval*2)*time.Second,
				)
				defer waitCancelFn()

				convertedHash := types.StringToHash(addResponse.TxHash)

				_, receiptErr := tests.WaitForReceipt(receiptCtx, srv.JSONRPC().Eth(), ethgo.Hash(convertedHash))
				if receiptErr != nil {
					t.Fatalf("Unable to get receipt, %v", receiptErr)
				}

				// Add the transaction with lower nonce value than what is
				// currently in the world state
				_, addErr = clt.AddTxn(context.Background(), addReq)
			}

			assert.NotNil(t, addErr)
			assert.Contains(t, addErr.Error(), testCase.expectedError.Error())
		})
	}
}

func TestTxPool_TransactionCoalescing(t *testing.T) {
	// Test scenario:
	// Add tx with nonce 0
	// -> Check if tx has been parsed
	// Add tx with nonce 2
	// -> tx shouldn't be executed, but shelved for later
	// Add tx with nonce 1
	// -> check if both tx with nonce 1 and tx with nonce 2 are parsed
	// Predefined values
	gasPrice := big.NewInt(1000000000)

	referenceKey, referenceAddr := tests.GenerateKeyAndAddr(t)
	defaultBalance := framework.EthToWei(10)

	// Set up the test server
	ibftManager := framework.NewIBFTServersManager(
		t,
		1,
		IBFTDirPrefix,
		func(i int, config *framework.TestServerConfig) {
			config.Premine(referenceAddr, defaultBalance)
			config.SetBlockTime(1)
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	ibftManager.StartServers(ctx)

	srv := ibftManager.GetServer(0)
	client := srv.JSONRPC()

	// Required default values
	signer := crypto.NewEIP155Signer(100, true)

	// TxPool client
	clt := srv.TxnPoolOperator()
	toAddress := types.StringToAddress("1")
	oneEth := framework.EthToWei(1)

	generateTx := func(nonce uint64) *types.Transaction {
		signedTx, signErr := signer.SignTx(&types.Transaction{
			Nonce:    nonce,
			From:     referenceAddr,
			To:       &toAddress,
			GasPrice: gasPrice,
			Gas:      1000000,
			Value:    oneEth,
			V:        big.NewInt(1), // it is necessary to encode in rlp
		}, referenceKey)
		if signErr != nil {
			t.Fatalf("Unable to sign transaction, %v", signErr)
		}

		return signedTx
	}

	generateReq := func(nonce uint64) *txpoolOp.AddTxnReq {
		msg := &txpoolOp.AddTxnReq{
			Raw: &any.Any{
				Value: generateTx(nonce).MarshalRLP(),
			},
			From: types.ZeroAddress.String(),
		}

		return msg
	}

	// testTransaction is a helper structure for
	// keeping track of test transaction execution
	type testTransaction struct {
		txHash ethgo.Hash // the transaction hash
		block  *uint64    // the block the transaction was included in
	}

	testTransactions := make([]*testTransaction, 0)

	// Add the transactions with the following nonce order
	nonces := []uint64{0, 2}
	for i := 0; i < len(nonces); i++ {
		addReq := generateReq(nonces[i])

		addCtx, addCtxCn := context.WithTimeout(context.Background(), framework.DefaultTimeout)

		addResp, addErr := clt.AddTxn(addCtx, addReq)
		if addErr != nil {
			t.Fatalf("Unable to add txn, %v", addErr)
		}

		testTransactions = append(testTransactions, &testTransaction{
			txHash: ethgo.HexToHash(addResp.TxHash),
		})

		addCtxCn()
	}

	// Wait for the first transaction to go through
	ctx, cancelFn := context.WithTimeout(context.Background(), framework.DefaultTimeout)
	defer cancelFn()

	receipt, receiptErr := tests.WaitForReceipt(ctx, client.Eth(), testTransactions[0].txHash)
	if receiptErr != nil {
		t.Fatalf("unable to wait for receipt, %v", receiptErr)
	}

	testTransactions[0].block = &receipt.BlockNumber

	// Get to account balance
	// Only the first tx should've gone through
	toAccountBalance := framework.GetAccountBalance(t, toAddress, client)
	assert.Equalf(t,
		oneEth.String(),
		toAccountBalance.String(),
		"To address balance mismatch after series of transactions",
	)

	// Add the transaction with the gap nonce value
	addReq := generateReq(1)

	addCtx, addCtxCn := context.WithTimeout(context.Background(), framework.DefaultTimeout)
	defer addCtxCn()

	addResp, addErr := clt.AddTxn(addCtx, addReq)
	if addErr != nil {
		t.Fatalf("Unable to add txn, %v", addErr)
	}

	testTransactions = append(testTransactions, &testTransaction{
		txHash: ethgo.HexToHash(addResp.TxHash),
	})

	// Start from 1 since there was previously a txn with nonce 0
	for i := 1; i < len(testTransactions); i++ {
		// Wait for the first transaction to go through
		ctx, cancelFn := context.WithTimeout(context.Background(), framework.DefaultTimeout)

		receipt, receiptErr := tests.WaitForReceipt(ctx, client.Eth(), testTransactions[i].txHash)
		if receiptErr != nil {
			t.Fatalf("unable to wait for receipt, %v", receiptErr)
		}

		testTransactions[i].block = &receipt.BlockNumber

		cancelFn()
	}

	// Now both the added tx and the shelved tx should've gone through
	toAccountBalance = framework.GetAccountBalance(t, toAddress, client)
	assert.Equalf(t,
		framework.EthToWei(3).String(),
		toAccountBalance.String(),
		"To address balance mismatch after gap transaction",
	)

	// Make sure the first transaction and the last transaction didn't get included in the same block
	assert.NotEqual(t, *(testTransactions[0].block), *(testTransactions[2].block))
}

type testAccount struct {
	key     *ecdsa.PrivateKey
	address types.Address
	balance *big.Int
}

func generateTestAccounts(t *testing.T, numAccounts int) []*testAccount {
	t.Helper()

	testAccounts := make([]*testAccount, numAccounts)

	for indx := 0; indx < numAccounts; indx++ {
		testAccount := &testAccount{}
		testAccount.key, testAccount.address = tests.GenerateKeyAndAddr(t)
		testAccounts[indx] = testAccount
	}

	return testAccounts
}

func TestTxPool_RecoverableError(t *testing.T) {
	// Test scenario :
	//
	// 1. Send a first valid transaction with gasLimit = block gas limit - 1
	//
	// 2. Send a second transaction with gasLimit = block gas limit / 2. Since there is not enough gas remaining,
	// the transaction will be pushed back to the pending queue so that can be executed in the next block.
	//
	// 3. Send a third - valid - transaction, both the previous one and this one should be executed.
	//
	senderKey, senderAddress := tests.GenerateKeyAndAddr(t)
	_, receiverAddress := tests.GenerateKeyAndAddr(t)

	transactions := []*types.Transaction{
		{
			Nonce:    0,
			GasPrice: big.NewInt(framework.DefaultGasPriceLegacy),
			Gas:      22000,
			To:       &receiverAddress,
			Value:    oneEth,
			V:        big.NewInt(27),
			From:     senderAddress,
		},
		{
			Nonce:    1,
			GasPrice: big.NewInt(framework.DefaultGasPriceLegacy),
			Gas:      22000,
			To:       &receiverAddress,
			Value:    oneEth,
			V:        big.NewInt(27),
			From:     senderAddress,
		},
		{
			Type:      types.DynamicFeeTx,
			Nonce:     2,
			GasFeeCap: big.NewInt(framework.DefaultGasPriceDynamic),
			GasTipCap: big.NewInt(1000000000),
			Gas:       22000,
			To:        &receiverAddress,
			Value:     oneEth,
			V:         big.NewInt(27),
			From:      senderAddress,
		},
	}

	server := framework.NewTestServers(t, 1, func(config *framework.TestServerConfig) {
		config.SetConsensus(framework.ConsensusDev)
		config.SetBlockLimit(2.5 * 21000)
		config.SetDevInterval(2)
		config.Premine(senderAddress, framework.EthToWei(100))
	})[0]

	client := server.JSONRPC()
	operator := server.TxnPoolOperator()
	hashes := make([]ethgo.Hash, 3)

	for i, tx := range transactions {
		signedTx, err := signer.SignTx(tx, senderKey)
		assert.NoError(t, err)

		response, err := operator.AddTxn(context.Background(), &txpoolOp.AddTxnReq{
			Raw: &any.Any{
				Value: signedTx.MarshalRLP(),
			},
			From: types.ZeroAddress.String(),
		})
		require.NoError(t, err, "Unable to send transaction, %v", err)

		txHash := ethgo.Hash(types.StringToHash(response.TxHash))

		// save for later querying
		hashes[i] = txHash
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	// wait for the last tx to be included in a block
	receipt, err := tests.WaitForReceipt(ctx, client.Eth(), hashes[2])
	require.NoError(t, err)
	require.NotNil(t, receipt)

	// assert balance moved
	balance, err := client.Eth().GetBalance(ethgo.Address(receiverAddress), ethgo.Latest)
	require.NoError(t, err, "failed to retrieve receiver account balance")
	require.Equal(t, framework.EthToWei(3).String(), balance.String())

	// Query 1st and 2nd txs
	firstTx, err := client.Eth().GetTransactionByHash(hashes[0])
	require.NoError(t, err)
	require.NotNil(t, firstTx)

	secondTx, err := client.Eth().GetTransactionByHash(hashes[1])
	require.NoError(t, err)
	require.NotNil(t, secondTx)

	// first two are in one block
	require.Equal(t, firstTx.BlockNumber, secondTx.BlockNumber)

	// last tx is included in next block
	require.NotEqual(t, secondTx.BlockNumber, receipt.BlockNumber)
}

func TestTxPool_GetPendingTx(t *testing.T) {
	senderKey, senderAddress := tests.GenerateKeyAndAddr(t)
	_, receiverAddress := tests.GenerateKeyAndAddr(t)
	// Test scenario:
	// The sender account should send multiple transactions to the receiving address
	// and get correct responses when querying the transaction through JSON-RPC

	startingBalance := framework.EthToWei(100)

	server := framework.NewTestServers(t, 1, func(config *framework.TestServerConfig) {
		config.SetConsensus(framework.ConsensusDev)
		config.SetDevInterval(3)
		config.SetBlockLimit(20000000)
		config.Premine(senderAddress, startingBalance)
	})[0]

	operator := server.TxnPoolOperator()
	client := server.JSONRPC()

	// Construct the transaction
	signedTx, err := signer.SignTx(&types.Transaction{
		Nonce:    0,
		GasPrice: big.NewInt(1000000000),
		Gas:      framework.DefaultGasLimit - 1,
		To:       &receiverAddress,
		Value:    oneEth,
		V:        big.NewInt(1),
		From:     types.ZeroAddress,
	}, senderKey)
	assert.NoError(t, err, "failed to sign transaction")

	// Add the transaction
	response, err := operator.AddTxn(context.Background(), &txpoolOp.AddTxnReq{
		Raw: &any.Any{
			Value: signedTx.MarshalRLP(),
		},
		From: types.ZeroAddress.String(),
	})
	assert.NoError(t, err, "Unable to send transaction, %v", err)

	txHash := ethgo.Hash(types.StringToHash(response.TxHash))

	// Grab the pending transaction from the pool
	tx, err := client.Eth().GetTransactionByHash(txHash)
	assert.NoError(t, err, "Unable to get transaction by hash, %v", err)
	assert.NotNil(t, tx)

	// Make sure the specific fields are not filled yet
	assert.Equal(t, uint64(0), tx.TxnIndex)
	assert.Equal(t, uint64(0), tx.BlockNumber)
	assert.Equal(t, ethgo.ZeroHash, tx.BlockHash)

	// Wait for the transaction to be included into a block
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receipt, err := tests.WaitForReceipt(ctx, client.Eth(), txHash)
	assert.NoError(t, err)
	assert.NotNil(t, receipt)

	assert.Equal(t, tx.TxnIndex, receipt.TransactionIndex)

	// fields should be updated
	tx, err = client.Eth().GetTransactionByHash(txHash)
	assert.NoError(t, err, "Unable to get transaction by hash, %v", err)
	assert.NotNil(t, tx)

	assert.Equal(t, uint64(0), tx.TxnIndex)
	assert.Equal(t, receipt.BlockNumber, tx.BlockNumber)
	assert.Equal(t, receipt.BlockHash, tx.BlockHash)
}

func TestE2E_TxPool_TestSync(t *testing.T) {
	const numOfTxs = 10

	senderKey, senderAddress := tests.GenerateKeyAndAddr(t)
	_, receiverAddress := tests.GenerateKeyAndAddr(t)
	to := ethgo.Address(receiverAddress)
	ethgoSenderKey := framework.NewEthgoKeyWrapper(senderKey, senderAddress)

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
