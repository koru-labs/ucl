package runner

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/0xPolygon/polygon-edge/consensus/polybft/contractsapi"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/contractsapi/artifact"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/txrelayerv2"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/schollz/progressbar/v3"
	"golang.org/x/sync/errgroup"
)

// MintPTokenRunner represents a load test runner for minting pTokens.
type MintPTokenRunner struct {
	*BaseLoadTestRunner

	mintPToken         types.Address
	mintPTokenArtifact *artifact.Artifact
	txInput            []byte
}

// NewMintPTokenRunner creates a new MintPTokenRunner instance with the given LoadTestConfig.
// It returns a pointer to the created MintPTokenRunner and an error, if any.
func NewMintPTokenRunner(cfg LoadTestConfig) (*MintPTokenRunner, error) {
	runner, err := NewBaseLoadTestRunner(cfg)
	if err != nil {
		return nil, err
	}

	return &MintPTokenRunner{BaseLoadTestRunner: runner}, nil
}

// Run executes the PToken load test.
// It performs the following steps:
// 1. Creates virtual users (VUs).
// 2. Funds the VUs with native tokens.
// 3. Deploys the PToken contract.
// 4. Mints PToken to the VUs.
// 5. Sends transactions using the VUs.
// 6. Waits for the transaction pool to empty.
// 7. Waits for transaction receipts.
// 8. Calculates the transactions per second (TPS) based on block information and transaction statistics.
// Returns an error if any of the steps fail.
func (e *MintPTokenRunner) Run(ctx context.Context) error {
	fmt.Println("Running PToken load test", e.cfg.LoadTestName)

	if err := e.createVUs(); err != nil {
		return err
	}

	if err := e.fundVUs(); err != nil {
		return err
	}

	if err := e.deployPToken(); err != nil {
		return err
	}

	if err := e.mintPTokenToVUs(); err != nil {
		return err
	}

	cancelableCtx, cancel := context.WithCancel(ctx)

	defer func() {
		cancel()

		e.resultsCollector.PrintResults()
	}()

	go e.resultsCollector.CollectResults(ctx)
	go e.readState(cancelableCtx)
	go e.readTxPool(cancelableCtx)

	if !e.cfg.WaitForTxPoolToEmpty {
		go e.waitForReceiptsParallel(cancelableCtx)
		go e.calculateResultsParallel()

		_, err := e.sendTransactions(e.createPTokenTransaction)
		if err != nil {
			return err
		}

		if err := <-e.done; err != nil {
			return err
		}

		nodeInfos, err := e.queryLatestBlocks()
		if err != nil {
			return err
		}

		return e.printNodeInfos(nodeInfos)
	}

	txHashes, err := e.sendTransactions(e.createPTokenTransaction)
	if err != nil {
		return err
	}

	if err := e.waitForTxPoolToEmpty(); err != nil {
		return err
	}

	if err := e.calculateResults(e.waitForReceipts(txHashes)); err != nil {
		return err
	}

	nodeInfos, err := e.queryLatestBlocks()
	if err != nil {
		return err
	}

	if err := e.tearDown(); err != nil {
		return err
	}

	return e.printNodeInfos(nodeInfos)
}

// deployPToken deploys a PToken contract.
// It loads the contract artifact from the specified file path,
// encodes the constructor inputs, creates a new transaction,
// sends the transaction using a transaction relayer,
// and retrieves the deployment receipt.
// If the deployment is successful, it sets the PToken address
// and artifact in the MintPTokenRunner instance.
// Returns an error if any step of the deployment process fails.
func (e *MintPTokenRunner) deployPToken() error {
	fmt.Println("=============================================================")
	fmt.Println("Deploying PToken contract")

	start := time.Now().UTC()
	artifact := contractsapi.PToken

	input, err := artifact.Abi.Constructor.Inputs.Encode(map[string]interface{}{
		"coinName":   "ZexCoin",
		"coinSymbol": "ZEX",
		"total":      500000000000,
	})
	if err != nil {
		return err
	}

	txn := &types.Transaction{
		Type:  types.LegacyTx,
		To:    nil,
		Input: append(artifact.Bytecode, input...),
		From:  e.loadTestAccount.key.Address(),
	}

	txrelayerv2, err := txrelayerv2.NewTxRelayer(
		txrelayerv2.WithClient(e.clients.getClient()),
		txrelayerv2.WithReceiptsTimeout(e.cfg.ReceiptsTimeout))
	if err != nil {
		return err
	}

	receipt, err := txrelayerv2.SendTransaction(txn, e.loadTestAccount.key)
	if err != nil {
		return err
	}

	if receipt == nil || receipt.Status == uint64(types.ReceiptFailed) {
		return fmt.Errorf("failed to deploy ERC20 token")
	}

	e.mintPToken = types.Address(receipt.ContractAddress)
	e.mintPTokenArtifact = artifact

	input, err = e.mintPTokenArtifact.Abi.Methods["transfer"].Encode(map[string]interface{}{
		"receiver":  e.receivers.getReceiver(),
		"numTokens": big.NewInt(1),
	})
	if err != nil {
		return err
	}

	e.txInput = input

	fmt.Printf("Deploying ERC20 token took %s\n", time.Since(start))

	return nil
}

// mintPTokenToVUs mints PToken to the specified virtual users (VUs).
// It sends a transfer transaction to each VU's address, minting the specified number of tokens.
// The transaction is sent using a transaction relayer, and the result is checked for success.
// If any error occurs during the minting process, an error is returned.
func (e *MintPTokenRunner) mintPTokenToVUs() error {
	fmt.Println("=============================================================")

	start := time.Now().UTC()
	bar := progressbar.Default(int64(e.cfg.VUs), "Minting PToken to VUs")
	client := e.clients.getClient()

	defer func() {
		_ = bar.Close()

		fmt.Printf("Minting PToken took %s\n", time.Since(start))
	}()

	txRelayer, err := txrelayerv2.NewTxRelayer(
		txrelayerv2.WithClient(client),
		txrelayerv2.WithoutNonceGet(),
		txrelayerv2.WithReceiptsTimeout(e.cfg.ReceiptsTimeout),
	)
	if err != nil {
		return err
	}

	nonce, err := client.GetNonce(e.loadTestAccount.key.Address(), jsonrpc.PendingBlockNumberOrHash)
	if err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(context.Background())

	for i, vu := range e.vus {
		i := i
		vu := vu

		g.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				input, err := e.mintPTokenArtifact.Abi.Methods["transfer"].Encode(map[string]interface{}{
					"receiver":  vu.key.Address(),
					"numTokens": big.NewInt(int64(e.cfg.TxsPerUser)),
				})
				if err != nil {
					return err
				}

				tx := &types.Transaction{
					Type:  types.LegacyTx,
					To:    &e.mintPToken,
					Input: input,
					Nonce: nonce + uint64(i),
					From:  e.loadTestAccount.key.Address(),
				}

				receipt, err := txRelayer.SendTransaction(tx, e.loadTestAccount.key)
				if err != nil {
					return err
				}

				if receipt == nil || receipt.Status != uint64(types.ReceiptSuccess) {
					return fmt.Errorf("failed to mint PToken to %s", vu.key.Address())
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

// createPTokenTransaction creates a PToken transaction
func (e *MintPTokenRunner) createPTokenTransaction(account *account, feeData *feeData,
	chainID *big.Int) (*types.Transaction, error) {
	if e.cfg.DynamicTxs {
		return &types.Transaction{
			Type:      types.DynamicFeeTx,
			Nonce:     account.nonce,
			To:        &e.mintPToken,
			From:      account.key.Address(),
			GasFeeCap: feeData.gasFeeCap,
			GasTipCap: feeData.gasTipCap,
			ChainID:   chainID,
			Input:     e.txInput,
		}, nil
	}

	return &types.Transaction{
		Type:     types.LegacyTx,
		Nonce:    account.nonce,
		To:       &e.mintPToken,
		GasPrice: feeData.gasPrice,
		From:     account.key.Address(),
		Input:    e.txInput,
	}, nil
}
