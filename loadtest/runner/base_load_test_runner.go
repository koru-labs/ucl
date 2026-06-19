package runner

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0xPolygon/polygon-edge/contracts"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/txrelayer"
	"github.com/0xPolygon/polygon-edge/txrelayerv2"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/wallet"
	"github.com/olekukonko/tablewriter"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/sync/errgroup"
)

const emptyBlocksNum = 10

type stats struct {
	totalTxs    int
	blockInfo   map[uint64]*BlockInfo
	foundErrors []error
}

type feeData struct {
	gasPrice  *big.Int
	gasTipCap *big.Int
	gasFeeCap *big.Int
}

// BaseLoadTestRunner represents a base load test runner.
type BaseLoadTestRunner struct {
	cfg LoadTestConfig

	loadTestAccount *account
	vus             []*account
	vusAddresses    []types.Address

	resultsCollectedCh chan *stats
	done               chan error

	resultsCollector *ResultCollector
	clients          ethClientList
	receivers        receiversList
	batchSenders     batchSendersList

	finality *finalityTracker
}

// NewBaseLoadTestRunner creates a new instance of BaseLoadTestRunner with the provided LoadTestConfig.
// It initializes the load test runner with the given configuration, including the mnemonic for the wallet,
// and sets up the necessary components such as the Ethereum key, binary path, and JSON-RPC client.
// If any error occurs during the initialization process, it returns nil and the error.
// Otherwise, it returns a pointer to the initialized BaseLoadTestRunner and nil error.
func NewBaseLoadTestRunner(cfg LoadTestConfig) (*BaseLoadTestRunner, error) {
	key, err := wallet.NewWalletFromMnemonic(cfg.Mnemonic)
	if err != nil {
		return nil, fmt.Errorf("failed to create wallet from mnemonic: %w", err)
	}

	raw, err := key.MarshallPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key for load test account: %w", err)
	}

	ecdsaKey, err := crypto.NewECDSAKeyFromRawPrivECDSA(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to create ECDSA key for load test account: %w", err)
	}

	ethClientList, err := newEthClientList(cfg.JSONRPCUrls)
	if err != nil {
		return nil, fmt.Errorf("failed to create eth client list: %w", err)
	}

	receiversList, err := newReceiversList(cfg.ReceiversNum)
	if err != nil {
		return nil, fmt.Errorf("failed to create receivers list: %w", err)
	}

	return &BaseLoadTestRunner{
		cfg:                cfg,
		loadTestAccount:    &account{key: ecdsaKey},
		resultsCollectedCh: make(chan *stats),
		done:               make(chan error),
		batchSenders:       newBatchSenders(cfg.JSONRPCUrls),
		resultsCollector:   NewResultCollector(cfg),
		clients:            ethClientList,
		receivers:          receiversList,
		vus:                make([]*account, cfg.VUs),
		vusAddresses:       make([]types.Address, cfg.VUs),
		finality:           newFinalityTracker(ethClientList, cfg.ReceiptsTimeout),
	}, nil
}

func (r *BaseLoadTestRunner) tearDown() error {
	if !r.cfg.TearDown {
		return nil
	}

	fmt.Println("=============================================================")
	fmt.Println("Unfunding users...")

	start := time.Now().UTC()
	bar := progressbar.Default(int64(r.cfg.VUs), "Unfund users")

	defer func() {
		_ = bar.Close()

		fmt.Println("Unfund users took", time.Since(start))
	}()

	txRelayer, err := txrelayerv2.NewTxRelayer(
		txrelayerv2.WithClient(r.clients.getClient()),
		txrelayerv2.WithoutNonceGet(),
	)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(context.Background())

	loadTestAddr := r.loadTestAccount.key.Address()

	chainID, err := r.clients.getClient().ChainID()
	if err != nil {
		return err
	}

	for _, vu := range r.vus {
		vu := vu

		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				addr := vu.key.Address()

				balance, err := r.clients.getClient().GetBalance(addr, jsonrpc.LatestBlockNumberOrHash)
				if err != nil {
					return err
				}

				refundAmount := balance.Sub(balance, ethgo.Ether(1))

				feeData, err := getFeeData(r.clients.getClient(), false)
				if err != nil {
					return err
				}

				var tx *types.Transaction

				if r.cfg.DynamicTxs {
					tx = &types.Transaction{
						Type:      types.DynamicFeeTx,
						Nonce:     vu.nonce,
						To:        &loadTestAddr,
						From:      addr,
						GasFeeCap: feeData.gasFeeCap,
						GasTipCap: feeData.gasTipCap,
						ChainID:   chainID,
						Value:     refundAmount,
					}
				} else {
					tx = &types.Transaction{
						Type:     types.LegacyTx,
						Nonce:    vu.nonce,
						To:       &loadTestAddr,
						From:     addr,
						GasPrice: feeData.gasPrice,
						Value:    refundAmount,
					}
				}

				receipt, err := txRelayer.SendTransaction(tx, vu.key)
				if err != nil {
					return fmt.Errorf("failed to send transaction: %w", err)
				}

				if receipt == nil || receipt.Status != uint64(types.ReceiptSuccess) {
					return fmt.Errorf("failed to tear down user %s", vu.key.Address().String())
				}

				_ = bar.Add(1)

				return nil
			}
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}

// Close closes the BaseLoadTestRunner by closing the underlying client connection.
// It returns an error if there was a problem closing the connection.
func (r *BaseLoadTestRunner) Close() error {
	return r.clients.close()
}

// createVUs creates virtual users (VUs) for the load test.
// It generates ECDSA keys for each VU and stores them in the `vus` slice.
// Returns an error if there was a problem generating the keys.
func (r *BaseLoadTestRunner) createVUs() error {
	fmt.Println("=============================================================")

	start := time.Now().UTC()
	bar := progressbar.Default(int64(r.cfg.VUs), "Creating virtual users")

	defer func() {
		_ = bar.Close()

		fmt.Println("Creating virtual users took", time.Since(start))
	}()

	for i := 0; i < r.cfg.VUs; i++ {
		key, err := crypto.GenerateECDSAKey()
		if err != nil {
			return err
		}

		r.vus[i] = &account{index: i, key: key, id: fmt.Sprintf("Index: %d Address: %s", i, key.Address().String())}
		r.vusAddresses[i] = key.Address()

		_ = bar.Add(1)
	}

	return nil
}

// fundVUs funds virtual users by transferring a specified amount of Ether to their addresses.
// It uses the provided load test account's private key to sign the transactions.
// The funding process is performed by executing a command-line bridge tool with the necessary arguments.
// The amount to fund is set to 1000 Ether.
// The function returns an error if there was an issue during the funding process.
func (r *BaseLoadTestRunner) fundVUs() error {
	fmt.Println("=============================================================")

	start := time.Now().UTC()
	bar := progressbar.Default(int64(r.cfg.VUs), "Funding virtual users with native tokens")

	defer func() {
		_ = bar.Close()

		fmt.Println("Funding took", time.Since(start))
	}()

	amountToFund := ethgo.Ether(1000)
	if r.cfg.VUs > 1000 {
		amountToFund = ethgo.Ether(uint64(1_000_000 / r.cfg.VUs))
	}

	txRelayer, err := txrelayerv2.NewTxRelayer(
		txrelayerv2.WithClient(r.clients.getClient()),
		txrelayerv2.WithoutNonceGet(),
	)
	if err != nil {
		return err
	}

	nonce, err := r.clients.getClient().GetNonce(r.loadTestAccount.key.Address(), jsonrpc.PendingBlockNumberOrHash)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(context.Background())

	for i, vu := range r.vus {
		i := i
		vu := vu

		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				to := vu.key.Address()

				tx := &types.Transaction{
					To:    &to,
					From:  r.loadTestAccount.key.Address(),
					Nonce: nonce + uint64(i),
					Value: amountToFund,
					Gas:   21000,
				}

				receipt, err := txRelayer.SendTransaction(tx, r.loadTestAccount.key)
				if err != nil {
					return err
				}

				if receipt == nil || receipt.Status != uint64(types.ReceiptSuccess) {
					return fmt.Errorf("failed to fund native tokens to %s", vu.key.Address())
				}

				_ = bar.Add(1)

				return nil
			}
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	return nil
}

// waitForTxPoolToEmpty waits for the transaction pool to become empty.
// It continuously checks the status of the transaction pool and returns
// when there are no pending or queued transactions.
// If the transaction pool does not become empty within the specified timeout,
// it returns an error.
func (r *BaseLoadTestRunner) waitForTxPoolToEmpty() error {
	fmt.Println("=============================================================")
	fmt.Println("Waiting for tx pool to empty...")

	timer := time.NewTimer(r.cfg.TxPoolTimeout)
	defer timer.Stop()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			txPoolStatus, err := r.clients.getClient().TxPoolStatus()
			if err != nil {
				return err
			}

			fmt.Println("Tx pool content. Pending:", txPoolStatus.Pending, "Queued:", txPoolStatus.Queued)

			if txPoolStatus.Pending == 0 && txPoolStatus.Queued == 0 {
				return nil
			}

		case <-timer.C:
			return fmt.Errorf("timeout while waiting for tx pool to empty")
		}
	}
}

// waitForReceiptsParallel waits for the receipts of the given transaction hashes in in a separate go routine.
// It continuously checks for the receipts until they are found or the timeout is reached.
// If the receipts are found, it sends the transaction statistics to the resultsCollectedCh channel.
// If the timeout is reached before the receipts are found, it returns.
// if there is a predefined number of empty blocks, it stops the results gathering before the timer.
func (r *BaseLoadTestRunner) waitForReceiptsParallel(ctx context.Context) {
	client := r.clients.getClient()

	startBlock, err := client.BlockNumber()
	if err != nil {
		fmt.Println("Error getting start block on gathering block info:", err)

		return
	}

	currentBlock := startBlock
	blockInfoMap := make(map[uint64]*BlockInfo)
	foundErrors := make([]error, 0)
	sequentialEmptyBlocks := 0
	totalTxsExecuted := 0

	timer := time.NewTimer(r.cfg.TxPoolTimeout)
	ticker := time.NewTicker(2 * time.Second)

	defer func() {
		timer.Stop()
		ticker.Stop()
		fmt.Println("Gathering results in parallel finished.")

		r.resultsCollectedCh <- &stats{totalTxs: totalTxsExecuted, blockInfo: blockInfoMap, foundErrors: foundErrors}
	}()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Context has been cancelled, aborting receipts retrieval...")

			return

		case <-timer.C:
			fmt.Println("Timeout while gathering block info")

			return

		case <-ticker.C:
			if sequentialEmptyBlocks >= emptyBlocksNum {
				return
			}

			block, err := client.GetBlockByNumber(jsonrpc.BlockNumber(currentBlock), true)
			if err != nil {
				foundErrors = append(foundErrors, err)

				continue
			}

			if block == nil {
				continue
			}

			if (len(block.Transactions) == 1 && block.Transactions[0].From == contracts.SystemCaller) ||
				len(block.Transactions) == 0 {
				sequentialEmptyBlocks++
				currentBlock++

				continue
			}

			sequentialEmptyBlocks = 0

			gasUsed := new(big.Int).SetUint64(block.Header.GasUsed)
			gasLimit := new(big.Int).SetUint64(block.Header.GasLimit)
			gasUtilization := new(big.Int).Mul(gasUsed, big.NewInt(10000))
			gasUtilization = gasUtilization.Div(gasUtilization, gasLimit).Div(gasUtilization, big.NewInt(100))

			gu, _ := gasUtilization.Float64()

			blockInfoMap[block.Number()] = &BlockInfo{
				Number:         block.Number(),
				CreatedAt:      block.Header.Timestamp,
				NumTxs:         len(block.Transactions),
				GasUsed:        new(big.Int).SetUint64(block.Header.GasUsed),
				GasLimit:       new(big.Int).SetUint64(block.Header.GasLimit),
				GasUtilization: gu,
			}

			totalTxsExecuted += len(block.Transactions)
			currentBlock++
		}
	}
}

// waitForReceipts waits for the receipts of the given transaction hashes and returns
// a map of block information, transaction statistics, and an error if any.
func (r *BaseLoadTestRunner) waitForReceipts(txHashes []types.Hash) (map[uint64]*BlockInfo, int) {
	fmt.Println("=============================================================")

	start := time.Now().UTC()
	blockInfoMap := make(map[uint64]*BlockInfo)
	txToBlockMap := make(map[types.Hash]uint64)
	bar := progressbar.Default(int64(len(txHashes)), "Gathering receipts")
	client := r.clients.getClient()

	defer func() {
		_ = bar.Close()

		fmt.Println("Waiting for receipts took", time.Since(start))
	}()

	foundErrors := make([]error, 0)

	var lock sync.Mutex

	getTxReceipts := func(txHashes []types.Hash) {
		for _, txHash := range txHashes {
			lock.Lock()
			if _, exists := txToBlockMap[txHash]; exists {
				_ = bar.Add(1)
				lock.Unlock()

				continue
			}

			lock.Unlock()

			receipt, err := r.waitForReceipt(txHash)
			if err != nil {
				lock.Lock()

				foundErrors = append(foundErrors, err)

				lock.Unlock()

				continue
			}

			_ = bar.Add(1)

			block, err := client.GetBlockByNumber(jsonrpc.BlockNumber(receipt.BlockNumber), true)
			if err != nil {
				lock.Lock()

				foundErrors = append(foundErrors, err)

				lock.Unlock()

				continue
			}

			gasUsed := new(big.Int).SetUint64(block.Header.GasUsed)
			gasLimit := new(big.Int).SetUint64(block.Header.GasLimit)
			gasUtilization := new(big.Int).Mul(gasUsed, big.NewInt(10000))
			gasUtilization = gasUtilization.Div(gasUtilization, gasLimit).Div(gasUtilization, big.NewInt(100))

			gu, _ := gasUtilization.Float64()

			lock.Lock()
			blockInfoMap[receipt.BlockNumber] = &BlockInfo{
				Number:         receipt.BlockNumber,
				CreatedAt:      block.Header.Timestamp,
				NumTxs:         len(block.Transactions),
				GasUsed:        new(big.Int).SetUint64(block.Header.GasUsed),
				GasLimit:       new(big.Int).SetUint64(block.Header.GasLimit),
				GasUtilization: gu,
			}

			for _, txn := range block.Transactions {
				txToBlockMap[txn.Hash] = receipt.BlockNumber
			}
			lock.Unlock()
		}
	}

	totalTxns := len(txHashes)

	// split the txHashes into batches so we can get them in parallel routines
	batchSize := totalTxns / 10
	if batchSize == 0 {
		batchSize = 1
	}

	var wg sync.WaitGroup

	for i := 0; i < totalTxns; i += batchSize {
		end := i + batchSize
		if end > totalTxns {
			end = totalTxns
		}

		wg.Add(1)

		go func(txHashes []types.Hash) {
			defer wg.Done()

			getTxReceipts(txHashes)
		}(txHashes[i:end])
	}

	wg.Wait()

	if len(foundErrors) > 0 {
		fmt.Println("Errors found while waiting for receipts:")

		for _, err := range foundErrors {
			fmt.Println(err)
		}
	}

	return blockInfoMap, len(txHashes)
}

// waitForReceipt waits for the transaction receipt of the given transaction hash.
// It continuously checks for the receipt until it is found or the timeout is reached.
// If the receipt is found, it returns the receipt and nil error.
// If the timeout is reached before the receipt is found, it returns nil receipt and an error.
func (r *BaseLoadTestRunner) waitForReceipt(txHash types.Hash) (*ethgo.Receipt, error) {
	timer := time.NewTimer(r.cfg.ReceiptsTimeout)
	defer timer.Stop()

	tickerTimeout := time.Second
	if r.cfg.ReceiptsTimeout <= time.Second {
		tickerTimeout = r.cfg.ReceiptsTimeout / 2
	}

	ticker := time.NewTicker(tickerTimeout)
	defer ticker.Stop()

	client := r.clients.getClient()

	for {
		select {
		case <-ticker.C:
			receipt, err := client.GetTransactionReceipt(txHash)
			if err != nil {
				if err.Error() != "not found" {
					return nil, err
				}
			}

			if receipt != nil {
				return receipt, nil
			}
		case <-timer.C:
			return nil, fmt.Errorf("timeout while waiting for transaction %s to be processed", txHash)
		}
	}
}

// calculateResultsParallel calculates the results of load test.
// Should be used in a separate go routine.
func (r *BaseLoadTestRunner) calculateResultsParallel() {
	stats := <-r.resultsCollectedCh

	if len(stats.foundErrors) > 0 {
		fmt.Println("Errors found while gathering results:")

		for _, err := range stats.foundErrors {
			fmt.Println(err)
		}
	}

	// sending has finished by the time gathering completes, so it is safe to
	// stop the finality tracker and compute the latency distribution here.
	fr := r.finality.stopAndCompute()

	r.done <- r.calculateResults(stats.blockInfo, stats.totalTxs, fr)
}

// calculateResults calculates the results of a load test for a given set of
// block information and transaction statistics.
// It takes a map of block information and an array of transaction statistics as input.
// The function iterates over the transaction statistics and calculates the TPS for each block.
// It also calculates the minimum and maximum TPS values, as well as the total time taken to mine the transactions.
// The calculated TPS values are displayed in a table using the tablewriter package.
// The function returns an error if there is any issue retrieving block information or calculating TPS.
func (r *BaseLoadTestRunner) calculateResults(
	blockInfos map[uint64]*BlockInfo,
	totalTxs int,
	finality finalityResult,
) error {
	fmt.Println("=============================================================")
	fmt.Println("Calculating results...")

	var (
		totalTime           float64
		maxTxsPerSecond     float64
		minTxsPerSecond     = math.MaxFloat64
		blockTimeMap        = make(map[uint64]uint64)
		uniqueBlocks        = map[uint64]struct{}{}
		infos               = make([]*BlockInfo, 0, len(blockInfos))
		totalGasUsed        = big.NewInt(0)
		minGasUtilization   = math.MaxFloat64
		maxGasUtilization   float64
		totalGasUtilization float64
		client              = r.clients.getClient()
	)

	for num, stat := range blockInfos {
		uniqueBlocks[num] = struct{}{}

		infos = append(infos, stat)
	}

	for block := range uniqueBlocks {
		currentBlockTxsNum := 0
		nextBlockNum := block + 1

		if _, exists := blockTimeMap[nextBlockNum]; !exists {
			if nextBlockInfo, exists := blockInfos[nextBlockNum]; !exists {
				nextBlock, err := client.GetBlockByNumber(jsonrpc.BlockNumber(nextBlockNum), false)
				if err != nil {
					return err
				}

				if nextBlock == nil {
					return fmt.Errorf("next block %d not mined yet, increase #txs in test", nextBlockNum)
				}

				blockTimeMap[nextBlockNum] = nextBlock.Header.Timestamp
			} else {
				blockTimeMap[nextBlockNum] = nextBlockInfo.CreatedAt
			}
		}

		nextBlockTimestamp := blockTimeMap[nextBlockNum]

		if _, ok := blockTimeMap[block]; !ok {
			if currentBlockInfo, ok := blockInfos[block]; !ok {
				currentBlock, err := client.GetBlockByNumber(jsonrpc.BlockNumber(block), true)
				if err != nil {
					return err
				}

				blockTimeMap[block] = currentBlock.Header.Timestamp
				currentBlockTxsNum = len(currentBlock.Transactions)
			} else {
				blockTimeMap[block] = currentBlockInfo.CreatedAt
				currentBlockTxsNum = currentBlockInfo.NumTxs
			}
		}

		if currentBlockTxsNum == 0 {
			currentBlockTxsNum = blockInfos[block].NumTxs
		}

		currentBlockTimestamp := blockTimeMap[block]
		blockTime := math.Abs(float64(nextBlockTimestamp - currentBlockTimestamp))

		currentBlockTxsPerSecond := float64(currentBlockTxsNum) / blockTime

		if currentBlockTxsPerSecond > maxTxsPerSecond {
			maxTxsPerSecond = currentBlockTxsPerSecond
		}

		if currentBlockTxsPerSecond < minTxsPerSecond {
			minTxsPerSecond = currentBlockTxsPerSecond
		}

		if blockInfos[block].GasUtilization < minGasUtilization {
			minGasUtilization = blockInfos[block].GasUtilization
		}

		if blockInfos[block].GasUtilization > maxGasUtilization {
			maxGasUtilization = blockInfos[block].GasUtilization
		}

		totalTime += blockTime
		totalGasUtilization += blockInfos[block].GasUtilization
		totalGasUsed.Add(totalGasUsed, blockInfos[block].GasUsed)
	}

	for _, info := range blockInfos {
		info.BlockTime = math.Abs(float64(blockTimeMap[info.Number+1] - info.CreatedAt))
		info.TPS = float64(info.NumTxs) / info.BlockTime
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Number < infos[j].Number
	})

	var (
		avgTxsPerSecond   float64
		avgGasUtilization float64
		avgGasPerTx       = big.NewInt(0)
	)

	if totalTime > 0 {
		avgTxsPerSecond = math.Ceil(float64(totalTxs) / totalTime)
	}

	if totalTxs > 0 {
		avgGasPerTx = new(big.Int).Div(totalGasUsed, big.NewInt(int64(totalTxs)))
	}

	if len(blockInfos) > 0 {
		avgGasUtilization = totalGasUtilization / float64(len(blockInfos))
	}

	if !r.cfg.ResultsToJSON {
		return printResults(
			totalTxs, totalTime, totalGasUsed,
			maxTxsPerSecond, minTxsPerSecond, avgTxsPerSecond, avgGasPerTx,
			minGasUtilization, maxGasUtilization, avgGasUtilization,
			infos, finality,
		)
	}

	return r.saveResultsToJSONFile(
		totalTxs, totalTime, totalGasUsed,
		maxTxsPerSecond, minTxsPerSecond, avgTxsPerSecond, avgGasPerTx,
		minGasUtilization, maxGasUtilization, avgGasUtilization,
		infos, finality)
}

type NodeInfoResult struct {
	NodeInfos []*NodeInfo `json:"nodeInfos"`
	OutOfSync []string    `json:"nodesOutOfSync"`
}

type NodeInfo struct {
	URL         string `json:"url"`
	BlockNumber uint64 `json:"blockNumber"`
}

// queryLatestBlocks queries for the latest blocks on all the nodes and
// detects if there are nodes that are out of sync (whose latest block number is outside of predefined deadband)
func (r *BaseLoadTestRunner) queryLatestBlocks() (*NodeInfoResult, error) {
	fmt.Println("=============================================================")
	fmt.Println("Querying latest blocks...")

	if len(r.clients) == 0 {
		return nil, errors.New("no clients available to query the latest blocks")
	}

	if len(r.cfg.JSONRPCUrls) != len(r.clients) {
		return nil, errors.New("number of JSON RPC URLs does not match the number of clients")
	}

	fmt.Println("Number of nodes:", len(r.clients))

	nodeInfos := make([]*NodeInfo, 0, len(r.clients))

	// query each node for the latest block number and store the result
	for i, client := range r.clients {
		nodeURL := r.cfg.JSONRPCUrls[i]

		blockNum, err := client.BlockNumber()
		if err != nil {
			return nil, fmt.Errorf("failed to query the latest block for %s node: %w", nodeURL, err)
		}

		nodeInfos = append(nodeInfos,
			&NodeInfo{
				URL:         nodeURL,
				BlockNumber: blockNum,
			})
	}

	// sort the node infos by block number (descending)
	sort.Slice(nodeInfos, func(i, j int) bool {
		return nodeInfos[i].BlockNumber > nodeInfos[j].BlockNumber
	})

	var (
		nodesOutOfSync     = make([]string, 0)
		largestBlockNumber = nodeInfos[0].BlockNumber
	)

	for _, nodeInfo := range nodeInfos[1:] {
		blockNum := nodeInfo.BlockNumber
		if blockNum == largestBlockNumber {
			continue
		}

		if largestBlockNumber-blockNum > r.cfg.BlockNumberDeadband {
			nodesOutOfSync = append(nodesOutOfSync, nodeInfo.URL)
		}
	}

	return &NodeInfoResult{
		NodeInfos: nodeInfos,
		OutOfSync: nodesOutOfSync,
	}, nil
}

// printNodeInfos prints the node information to the console.
// It displays the node URL and the latest block number for each node.
// If there are nodes that are out of sync, it displays the URLs of those nodes.
func (r *BaseLoadTestRunner) printNodeInfos(nodesResult *NodeInfoResult) error {
	if !r.cfg.ResultsToJSON {
		fmt.Println("=============================================================")
		fmt.Println("Node information:")

		table := tablewriter.NewWriter(os.Stdout)
		table.Header([]string{"Node URL", "Block Number"})

		for _, nodeInfo := range nodesResult.NodeInfos {
			if err := table.Append([]string{nodeInfo.URL, fmt.Sprint(nodeInfo.BlockNumber)}); err != nil {
				return err
			}
		}

		if err := table.Render(); err != nil {
			return err
		}

		if len(nodesResult.OutOfSync) > 0 {
			fmt.Println("Nodes out of sync:")

			for _, nodeURL := range nodesResult.OutOfSync {
				fmt.Println(nodeURL)
			}
		} else {
			fmt.Println("All nodes are in sync")
		}
	} else {
		fileName := fmt.Sprintf("./%s_%s_node_infos.json", r.cfg.LoadTestName, r.cfg.LoadTestType)

		jsonData, err := json.MarshalIndent(nodesResult, "", "   ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}

		if err := common.SaveFileSafe(fileName, jsonData, 0600); err != nil {
			return err
		}
	}

	return nil
}

// saveResultsToJSONFile saves the load test results to a JSON file.
// It takes the total number of transactions (totalTxs), total time taken (totalTime),
// maximum transactions per second (maxTxsPerSecond), minimum transactions per second (minTxsPerSecond),
// average transactions per second (avgTxsPerSecond), and a map of block information (blockInfos).
// It returns an error if there was a problem saving the results to the file.
func (r *BaseLoadTestRunner) saveResultsToJSONFile(
	totalTxs int, totalTime float64, totalGasUsed *big.Int,
	maxTxsPerSecond, minTxsPerSecond, avgTxsPerSecond float64, avgGasPerTx *big.Int,
	minGasUtilization, maxGasUtilization, avgGasUtilization float64,
	blockInfos []*BlockInfo, finality finalityResult) error {
	fmt.Println("Saving results to JSON file...")

	type Result struct {
		TotalBlocks       int          `json:"totalBlocks"`
		TotalTxs          int          `json:"totalTxs"`
		TotalTime         float64      `json:"totalTime"`
		TotalGasUsed      string       `json:"totalGasUsed"`
		MinTxsPerSecond   float64      `json:"minTxsPerSecond"`
		MaxTxsPerSecond   float64      `json:"maxTxsPerSecond"`
		AvgTxsPerSecond   float64      `json:"avgTxsPerSecond"`
		AvgGasPerTx       string       `json:"avgGasPerTx"`
		MinGasUtilization float64      `json:"minGasUtilization"`
		MaxGasUtilization float64      `json:"maxGasUtilization"`
		AvgGasUtilization float64      `json:"avgGasUtilization"`
		FinalityP50Ms     float64      `json:"finalityP50Ms"`
		FinalityP95Ms     float64      `json:"finalityP95Ms"`
		FinalityP99Ms     float64      `json:"finalityP99Ms"`
		FinalityMeasured  int          `json:"finalityMeasured"`
		FinalityDropped   uint64       `json:"finalityDropped"`
		Blocks            []*BlockInfo `json:"blocks"`
	}

	result := Result{
		TotalBlocks:       len(blockInfos),
		TotalTxs:          totalTxs,
		TotalTime:         totalTime,
		TotalGasUsed:      totalGasUsed.String(),
		MinTxsPerSecond:   minTxsPerSecond,
		MaxTxsPerSecond:   maxTxsPerSecond,
		AvgTxsPerSecond:   avgTxsPerSecond,
		Blocks:            blockInfos,
		AvgGasPerTx:       avgGasPerTx.String(),
		MinGasUtilization: minGasUtilization,
		MaxGasUtilization: maxGasUtilization,
		AvgGasUtilization: avgGasUtilization,
		FinalityP50Ms:     float64(finality.p50.Microseconds()) / 1000.0,
		FinalityP95Ms:     float64(finality.p95.Microseconds()) / 1000.0,
		FinalityP99Ms:     float64(finality.p99.Microseconds()) / 1000.0,
		FinalityMeasured:  finality.measured,
		FinalityDropped:   finality.dropped,
	}

	jsonData, err := json.MarshalIndent(result, "", "   ")
	if err != nil {
		return err
	}

	fileName := fmt.Sprintf("./%s_%s.json", r.cfg.LoadTestName, r.cfg.LoadTestType)

	err = common.SaveFileSafe(fileName, jsonData, 0600)
	if err != nil {
		return err
	}

	fmt.Println("Results saved to JSON file", fileName)

	return nil
}

// sendTransactions sends transactions for each virtual user (vu) and returns the transaction hashes.
// It retrieves the chain ID from the client and uses it to send transactions for each user.
// The function runs concurrently for each user using errgroup.
// If the context is canceled, the function returns the context error.
// The transaction hashes are appended to the allTxnHashes slice.
// Finally, the function prints the time taken to send the transactions
// and returns the transaction hashes and nil error.
// recordSubmitTimes feeds the finality tracker with submitted transaction hashes
// and the wall-clock time each was sent. Hashes and times are produced in the same
// order (one per successful send), so they are paired positionally.
func (r *BaseLoadTestRunner) recordSubmitTimes(hashes []types.Hash, times []time.Time) {
	if r.finality == nil {
		return
	}

	n := len(hashes)
	if len(times) < n {
		n = len(times)
	}

	for i := 0; i < n; i++ {
		r.finality.record(hashes[i], times[i])
	}
}

func (r *BaseLoadTestRunner) sendTransactions(
	createTxnFn func(*account, *feeData, *big.Int) (*types.Transaction, error),
) ([]types.Hash, error) {
	fmt.Println("=============================================================")

	client := r.clients.getClient()
	totalTxs := r.calculateTotalTxs()
	foundErrs := make([]error, 0)
	bar := progressbar.Default(totalTxs, "Sending transactions")
	start := time.Now().UTC()

	chainID, err := client.ChainID()
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = bar.Close()

		fmt.Println("Sending transactions took", time.Since(start))
	}()

	var (
		allTxnHashes []types.Hash
		appendMux    sync.Mutex
		g, ctx       = errgroup.WithContext(context.Background())
	)

	if totalTxs > 0 {
		allTxnHashes = make([]types.Hash, 0, totalTxs)
	}

	sendFn := r.sendTransactionsForUser
	if r.cfg.ExecutionTime > 0 {
		if r.cfg.TxsPerSecond > 0 {
			sendFn = r.sendTransactionsRateLimited
		} else {
			sendFn = r.sendTransactionsInTime
		}
	} else if r.cfg.BatchSize > 1 {
		sendFn = r.sendTransactionsForUserInBatches
	}

	for _, vu := range r.vus {
		vu := vu

		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()

			default:
				txnHashes, sendErrors, err := sendFn(vu, chainID, bar, createTxnFn)
				if err != nil {
					return err
				}

				appendMux.Lock()

				foundErrs = append(foundErrs, sendErrors...)
				allTxnHashes = append(allTxnHashes, txnHashes...)

				appendMux.Unlock()

				return nil
			}
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	if len(foundErrs) > 0 {
		fmt.Println("Errors found while sending transactions:")

		for _, err := range foundErrs {
			fmt.Println(err)
		}
	}

	return allTxnHashes, nil
}

// readState continuously reads nonce and balance from blockchain
// for each account, with a max of StateReadThreads concurrent workers.
func (r *BaseLoadTestRunner) readState(ctx context.Context) {
	if r.cfg.StateReadThreads == 0 {
		return
	}

	contractMap := contracts.GetProxyImplementationMapping()

	for i := 0; i < r.cfg.StateReadThreads; i++ {
		i := i

		go func() {
			client := r.clients.getClientForAccount(i)

			for {
				select {
				case <-ctx.Done():
					return
				default:
					// read non stop the state of the accounts
					r.readBasicState(client, contractMap)
				}
			}
		}()
	}
}

// readBasicState reads the basic state of the accounts and contracts.
func (r *BaseLoadTestRunner) readBasicState(client *jsonrpc.EthClient, contractsMap map[types.Address]types.Address) {
	for _, senderAddr := range r.vusAddresses {
		r.readBalance(client, senderAddr)
		r.readNonce(client, senderAddr)
	}

	for _, receiver := range r.receivers {
		r.readBalance(client, receiver)
		r.readNonce(client, receiver)
	}

	for _, contractAddr := range contractsMap {
		r.readCode(client, contractAddr)
	}
}

// readTxPool will read the transaction pool continuously until the context is canceled.
func (r *BaseLoadTestRunner) readTxPool(ctx context.Context) {
	if r.cfg.TxPoolReadThreads == 0 {
		return
	}

	for i := 0; i < r.cfg.TxPoolReadThreads; i++ {
		i := i

		go func() {
			client := r.clients.getClientForAccount(i)

			for {
				select {
				case <-ctx.Done():
					return

				default:
					_, err := client.TxPoolStatus()
					if err != nil {
						r.resultsCollector.TxPoolStatusReadErrorCh <- err

						continue
					}

					r.resultsCollector.TxPoolStatusReadCountCh <- struct{}{}
				}
			}
		}()
	}
}

// sendTransactionsInTime sends transactions for each virtual user (vu) within a specified time duration
// It uses the execution-time, and batch-size parameters to determine how many transactions per iteration
// to send for each user. The function runs concurrently for each user using errgroup.
// - if batch-size is 0 or 1, it sends transactions one by one
// - if batch-size is greater than 1, it sends transactions in batches
// (for example, 5 txns in batch each iteration)
func (r *BaseLoadTestRunner) sendTransactionsInTime(
	account *account, chainID *big.Int,
	bar *progressbar.ProgressBar,
	createTxnFn func(*account, *feeData, *big.Int) (*types.Transaction, error),
) ([]types.Hash, []error, error) {
	executionTimer := time.NewTimer(r.cfg.ExecutionTime)
	defer executionTimer.Stop()

	var (
		txnHashes  []types.Hash
		sendErrors []error
	)

	numOfTxns := 1 // by default we will send transaction per transaction
	if r.cfg.BatchSize > 0 {
		// if batch size is set, then we send batch per batch
		numOfTxns = r.cfg.BatchSize
	}

	batchSender := r.batchSenders.getBatchSenderForAccount(account.index)

	for {
		h, se, err := r.sendTransactionsForUserInBatchesInternal(numOfTxns,
			account, chainID, r.clients.getClientForAccount(account.index), bar, batchSender, createTxnFn)
		if err != nil {
			return nil, nil, err
		}

		txnHashes = append(txnHashes, h...)
		sendErrors = append(sendErrors, se...)

		select {
		case <-executionTimer.C:
			return txnHashes, sendErrors, nil
		default:
			continue
		}
	}
}

// sendTransactionsForUser sends transactions for a given user account.
// It takes an account pointer and a chainID as input parameters.
// It returns a slice of transaction hashes and an error if any.
func (r *BaseLoadTestRunner) sendTransactionsForUser(
	account *account, chainID *big.Int,
	bar *progressbar.ProgressBar,
	createTxnFn func(*account, *feeData, *big.Int) (*types.Transaction, error),
) ([]types.Hash, []error, error) {
	client := r.clients.getClient()

	txRelayer, err := txrelayerv2.NewTxRelayer(
		txrelayerv2.WithClient(client),
		txrelayerv2.WithCollectTxnHashes(),
		txrelayerv2.WithNoWaiting(),
		txrelayerv2.WithEstimateGasFallback(),
		txrelayerv2.WithoutNonceGet(),
	)
	if err != nil {
		return nil, nil, err
	}

	feeData, err := getFeeData(client, r.cfg.DynamicTxs)
	if err != nil {
		return nil, nil, err
	}

	sendErrs := make([]error, 0)
	submitTimes := make([]time.Time, 0, r.cfg.TxsPerUser)
	checkFeeDataNum := r.cfg.TxsPerUser / 5

	for i := 0; i < r.cfg.TxsPerUser; i++ {
		if checkFeeDataNum > 0 && i%checkFeeDataNum == 0 {
			feeData, err = getFeeData(client, r.cfg.DynamicTxs)
			if err != nil {
				return nil, nil, err
			}
		}

		txn, err := createTxnFn(account, feeData, chainID)
		if err != nil {
			sendErrs = append(sendErrs, err)
			_ = bar.Add(1)

			continue
		}

		sentAt := time.Now().UTC()

		_, err = txRelayer.SendTransaction(txn, account.key)
		if err != nil {
			sendErrs = append(sendErrs, err)
		} else {
			submitTimes = append(submitTimes, sentAt)
		}

		r.resultsCollector.VUTxnCountCh <- VUTxnCount{account.id, 1}

		account.nonce++
		_ = bar.Add(1)
	}

	hashes := txRelayer.GetTxnHashes()
	r.recordSubmitTimes(hashes, submitTimes)

	return hashes, sendErrs, nil
}

// sendTransactionsForUserInBatches sends user transactions in batches to the rpc node
func (r *BaseLoadTestRunner) sendTransactionsForUserInBatches(
	account *account, chainID *big.Int,
	bar *progressbar.ProgressBar,
	createTxnFn func(*account, *feeData, *big.Int) (*types.Transaction, error),
) ([]types.Hash, []error, error) {
	return r.sendTransactionsForUserInBatchesInternal(
		r.cfg.TxsPerUser, account, chainID, r.clients.getClient(),
		bar, r.batchSenders.getBatchSender(), createTxnFn)
}

func (r *BaseLoadTestRunner) sendTransactionsForUserInBatchesInternal(
	numOfTxns int,
	account *account,
	chainID *big.Int,
	client *jsonrpc.EthClient,
	bar *progressbar.ProgressBar,
	batchSender *TransactionBatchSender,
	createTxnFn func(*account, *feeData, *big.Int) (*types.Transaction, error),
) ([]types.Hash, []error, error) {
	signer := crypto.NewLondonSigner(chainID.Uint64(), true, crypto.NewEIP155Signer(chainID.Uint64(), true))

	numOfBatches := int(math.Ceil(float64(numOfTxns) / float64(r.cfg.BatchSize)))
	txHashes := make([]types.Hash, 0, numOfTxns)
	sendErrs := make([]error, 0)
	totalTxs := 0

	var gas uint64

	feeData, err := getFeeData(client, r.cfg.DynamicTxs)
	if err != nil {
		return nil, nil, err
	}

	txnExample, err := createTxnFn(account, feeData, chainID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create transaction example: %w", err)
	}

	if txnExample.Gas == 0 {
		// estimate gas initially
		gasLimit, err := client.EstimateGas(txrelayerv2.ConvertTxnToCallMsg(txnExample))
		if err != nil {
			gasLimit = txrelayer.DefaultGasLimit
		}

		gas = gasLimit * 2 // double it just in case
	} else {
		gas = txnExample.Gas
	}

	for i := 0; i < numOfBatches; i++ {
		batchTxs := make([]string, 0, r.cfg.BatchSize)

		feeData, err := getFeeData(client, r.cfg.DynamicTxs)
		if err != nil {
			return nil, nil, err
		}

		for j := 0; j < r.cfg.BatchSize; j++ {
			if totalTxs >= numOfTxns {
				break
			}

			txn, err := createTxnFn(account, feeData, chainID)
			if err != nil {
				sendErrs = append(sendErrs, err)

				continue
			}

			if txn.Gas == 0 {
				txn.Gas = gas
			}

			signedTxn, err := signer.SignTx(txn, account.key.PrivateKey())
			if err != nil {
				sendErrs = append(sendErrs, err)

				continue
			}

			batchTxs = append(batchTxs, "0x"+hex.EncodeToString(signedTxn.MarshalRLP()))
			account.nonce++
			totalTxs++
		}

		sentAt := time.Now().UTC()

		hashes, err := batchSender.SendBatch(batchTxs)
		if err != nil {
			return nil, nil, err
		}

		for _, h := range hashes {
			r.finality.record(h, sentAt)
		}

		r.resultsCollector.VUTxnCountCh <- VUTxnCount{account.id, len(hashes)}

		txHashes = append(txHashes, hashes...)
		_ = bar.Add(len(batchTxs))
	}

	return txHashes, sendErrs, nil
}

// calculateTotalTxs calculates the total number of transactions to be sent based on the load test configuration.
func (r *BaseLoadTestRunner) calculateTotalTxs() int64 {
	var totalTxs int64

	if r.cfg.ExecutionTime > 0 {
		// we can not be sure how many txns we will send in this case
		// so we will use a spinner instead of a progress bar
		totalTxs = -1
	} else {
		totalTxs = int64(r.cfg.TxsPerUser * r.cfg.VUs)
	}

	return totalTxs
}

// readBalance reads the balance of the given address from the blockchain
// and reports the result to the results collector.
func (r *BaseLoadTestRunner) readBalance(client *jsonrpc.EthClient, addr types.Address) {
	_, err := client.GetBalance(addr, jsonrpc.LatestBlockNumberOrHash)
	if err != nil {
		r.resultsCollector.BalanceReadErrorCh <- fmt.Errorf("failed to read balance for %s account: %w",
			addr, err)

		return
	}

	r.resultsCollector.BalanceReadCountCh <- struct{}{}
}

// readNonce reads the nonce of the given address from the blockchain
// and reports the result to the results collector.
func (r *BaseLoadTestRunner) readNonce(client *jsonrpc.EthClient, addr types.Address) {
	_, err := client.GetNonce(addr, jsonrpc.LatestBlockNumberOrHash)
	if err != nil {
		r.resultsCollector.NonceReadErrorCh <- fmt.Errorf("failed to read nonce for %s account: %w",
			addr, err)

		return
	}

	r.resultsCollector.NonceReadCountCh <- struct{}{}
}

// readCode reads the code of the given contract address from the blockchain
// and reports the result to the results collector.
func (r *BaseLoadTestRunner) readCode(client *jsonrpc.EthClient, addr types.Address) {
	_, err := client.GetCode(addr, jsonrpc.LatestBlockNumberOrHash)
	if err != nil {
		r.resultsCollector.CodeReadErrorCh <- fmt.Errorf("failed to read code for %s contract: %w",
			addr, err)

		return
	}

	r.resultsCollector.CodeReadCountCh <- struct{}{}
}

// getFeeData retrieves fee data based on the provided JSON-RPC Ethereum client and dynamicTxs flag.
// If dynamicTxs is true, it calculates the gasTipCap and gasFeeCap based on the MaxPriorityFeePerGas,
// FeeHistory, and BaseFee values obtained from the client. If dynamicTxs is false, it calculates the
// gasPrice based on the GasPrice value obtained from the client.
// The function returns a feeData struct containing the calculated fee values.
// If an error occurs during the retrieval or calculation, the function returns nil and the error.
func getFeeData(client *jsonrpc.EthClient, dynamicTxs bool) (*feeData, error) {
	feeData := &feeData{}

	if dynamicTxs {
		mpfpg, err := client.MaxPriorityFeePerGas()
		if err != nil {
			return nil, err
		}

		gasTipCap := new(big.Int).Mul(mpfpg, big.NewInt(2))

		feeHistory, err := client.FeeHistory(1, jsonrpc.LatestBlockNumber, nil)
		if err != nil {
			return nil, err
		}

		baseFee := big.NewInt(0)

		if len(feeHistory.BaseFee) != 0 {
			baseFee = baseFee.SetUint64(feeHistory.BaseFee[len(feeHistory.BaseFee)-1])
		}

		gasFeeCap := new(big.Int).Add(baseFee, mpfpg)
		gasFeeCap.Mul(gasFeeCap, big.NewInt(2))

		feeData.gasTipCap = gasTipCap
		feeData.gasFeeCap = gasFeeCap
	} else {
		gp, err := client.GasPrice()
		if err != nil {
			return nil, err
		}

		gasPrice := new(big.Int).SetUint64(gp + (gp * 50 / 100))

		feeData.gasPrice = gasPrice
	}

	return feeData, nil
}

// printResults prints the results of the load test to stdout in a form of a table
func printResults(totalTxs int, totalTime float64, totalGasUsed *big.Int,
	maxTxsPerSecond, minTxsPerSecond, avgTxsPerSecond float64, avgGasPerTx *big.Int,
	minGasUtilization, maxGasUtilization, avgGasUtilization float64,
	blockInfos []*BlockInfo, finality finalityResult) error {
	table := tablewriter.NewWriter(os.Stdout)
	table.Header([]string{
		"Block Number",
		"Block Time (s)",
		"Num Txs",
		"Gas Used",
		"Gas Limit",
		"Gas Utilization",
		"TPS",
	})

	for _, blockInfo := range blockInfos {
		if err := table.Append([]string{
			fmt.Sprintf("%d", blockInfo.Number),
			fmt.Sprintf("%.2f", blockInfo.BlockTime),
			fmt.Sprintf("%d", blockInfo.NumTxs),
			fmt.Sprintf("%d", blockInfo.GasUsed.Uint64()),
			fmt.Sprintf("%d", blockInfo.GasLimit.Uint64()),
			fmt.Sprintf("%.2f", blockInfo.GasUtilization),
			fmt.Sprintf("%.2f", blockInfo.TPS),
		}); err != nil {
			return err
		}
	}

	if err := table.Render(); err != nil {
		return err
	}

	table = tablewriter.NewWriter(os.Stdout)
	table.Header([]string{
		"Total Blocks",
		"Total Txs",
		"Total Time To Mine (s)",
		"Total Gas Used",
		"Average Gas Per Tx",
		"Min TPS",
		"Max TPS",
		"Average TPS",
		"Min Gas Utilization",
		"Max Gas Utilization",
		"Average Gas Utilization",
	})

	if err := table.Append([]string{
		fmt.Sprintf("%d", len(blockInfos)),
		fmt.Sprintf("%d", totalTxs),
		fmt.Sprintf("%.2f", totalTime),
		totalGasUsed.String(),
		avgGasPerTx.String(),
		fmt.Sprintf("%.2f", minTxsPerSecond),
		fmt.Sprintf("%.2f", maxTxsPerSecond),
		fmt.Sprintf("%.2f", avgTxsPerSecond),
		fmt.Sprintf("%.2f", minGasUtilization),
		fmt.Sprintf("%.2f", maxGasUtilization),
		fmt.Sprintf("%.2f", avgGasUtilization),
	}); err != nil {
		return err
	}

	if err := table.Render(); err != nil {
		return err
	}

	printFinalityResults(finality)

	return nil
}

// printFinalityResults prints the submit->finalized latency distribution.
func printFinalityResults(finality finalityResult) {
	table := tablewriter.NewWriter(os.Stdout)
	table.Header([]string{
		"Finality p50 (ms)",
		"Finality p95 (ms)",
		"Finality p99 (ms)",
		"Measured Txs",
		"Dropped Samples",
	})

	if err := table.Append([]string{
		fmt.Sprintf("%.2f", float64(finality.p50.Microseconds())/1000.0),
		fmt.Sprintf("%.2f", float64(finality.p95.Microseconds())/1000.0),
		fmt.Sprintf("%.2f", float64(finality.p99.Microseconds())/1000.0),
		fmt.Sprintf("%d", finality.measured),
		fmt.Sprintf("%d", finality.dropped),
	}); err != nil {
		fmt.Println("Error rendering finality results:", err)

		return
	}

	if err := table.Render(); err != nil {
		fmt.Println("Error rendering finality results:", err)
	}
}

// sendTransactionsRateLimited sends transactions at a controlled rate defined by TxsPerSecond config.
// It pre-signs batches in a background goroutine to avoid signing overhead during send,
// and uses a ticker to dispatch one batch per second per VU.
// VUs are staggered to avoid thundering herd on the RPC node.
func (r *BaseLoadTestRunner) sendTransactionsRateLimited(
	acc *account, chainID *big.Int,
	bar *progressbar.ProgressBar,
	createTxnFn func(*account, *feeData, *big.Int) (*types.Transaction, error),
) ([]types.Hash, []error, error) {
	numOfTxns := r.cfg.TxsPerSecond / r.cfg.VUs
	if numOfTxns < 1 {
		numOfTxns = 1
	}

	client := r.clients.getClientForAccount(acc.index)
	signer := crypto.NewLondonSigner(chainID.Uint64(), true, crypto.NewEIP155Signer(chainID.Uint64(), true))

	feeData, err := getFeeData(client, r.cfg.DynamicTxs)
	if err != nil {
		return nil, nil, err
	}

	txnExample, err := createTxnFn(acc, feeData, chainID)
	if err != nil {
		return nil, nil, err
	}

	var gas uint64

	if txnExample.Gas == 0 {
		gasLimit, err := client.EstimateGas(txrelayerv2.ConvertTxnToCallMsg(txnExample))
		if err != nil {
			gasLimit = txrelayer.DefaultGasLimit
		}

		gas = gasLimit * 2
	} else {
		gas = txnExample.Gas
	}

	type signedBatch struct {
		rawTxs []string
	}

	totalBatches := int(r.cfg.ExecutionTime.Seconds())

	signedCh := make(chan signedBatch, 3)

	go func() {
		defer close(signedCh)

		startNonce := acc.nonce

		for i := 0; i < totalBatches; i++ {
			feeData, err := getFeeData(client, r.cfg.DynamicTxs)
			if err != nil {
				return
			}

			batch := signedBatch{
				rawTxs: make([]string, 0, numOfTxns),
			}

			for j := 0; j < numOfTxns; j++ {
				txn, err := createTxnFn(acc, feeData, chainID)
				if err != nil {
					continue
				}

				txn.Nonce = startNonce + uint64(i*numOfTxns+j)
				if txn.Gas == 0 {
					txn.Gas = gas
				}

				signed, err := signer.SignTx(txn, acc.key.PrivateKey())
				if err != nil {
					continue
				}

				batch.rawTxs = append(batch.rawTxs, "0x"+hex.EncodeToString(signed.MarshalRLP()))
			}

			signedCh <- batch
		}

		acc.nonce += uint64(totalBatches * numOfTxns)
	}()

	firstBatch, ok := <-signedCh
	if !ok {
		return nil, nil, nil
	}

	// VU0=0ms, VU1=100ms, VU2=200ms... (for 10 VUs)
	if r.cfg.VUs > 1 {
		slotInterval := time.Second / time.Duration(r.cfg.VUs)
		time.Sleep(slotInterval * time.Duration(acc.index))
	}

	batchSender := r.batchSenders.getBatchSenderForAccount(acc.index)

	executionTimer := time.NewTimer(r.cfg.ExecutionTime)
	defer executionTimer.Stop()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		txnHashes  []types.Hash
		sendErrors []error
	)

	sendBatch := func(b signedBatch) {
		defer wg.Done()

		var (
			hashes []types.Hash
			err    error
		)

		var sentAt time.Time

		for attempt := 0; attempt < 3; attempt++ {
			fmt.Println("sending batch...", time.Now())

			sentAt = time.Now().UTC()

			hashes, err = batchSender.SendBatch(b.rawTxs)
			if err == nil {
				break
			}

			// retry only on transient connection errors the node may close
			// idle keep-alive connections, especially on long test runs
			if strings.Contains(err.Error(), "server closed connection") ||
				strings.Contains(err.Error(), "connection reset by peer") ||
				strings.Contains(err.Error(), "EOF") {
				time.Sleep(50 * time.Millisecond)

				continue
			}

			break
		}

		if err != nil {
			mu.Lock()

			sendErrors = append(sendErrors, err)

			mu.Unlock()

			return
		}

		_ = bar.Add(len(b.rawTxs))
		r.resultsCollector.VUTxnCountCh <- VUTxnCount{acc.id, len(hashes)}

		for _, h := range hashes {
			r.finality.record(h, sentAt)
		}

		mu.Lock()

		txnHashes = append(txnHashes, hashes...)

		mu.Unlock()
	}

	// send the first batch immediately without waiting for the ticker
	wg.Add(1)

	go sendBatch(firstBatch)

	for {
		select {
		case <-executionTimer.C:
			wg.Wait()

			return txnHashes, sendErrors, nil

		case <-ticker.C:
			batch, ok := <-signedCh
			if !ok {
				wg.Wait()

				return txnHashes, sendErrors, nil
			}

			wg.Add(1)

			go sendBatch(batch)
		}
	}
}
