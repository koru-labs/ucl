package loadtest

import (
	"fmt"
	"time"

	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/command/helper"
	"github.com/0xPolygon/polygon-edge/loadtest/runner"
	"github.com/0xPolygon/polygon-edge/server"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/spf13/cobra"
)

var (
	params loadTestParams
)

func GetCommand() *cobra.Command {
	loadTestCmd := &cobra.Command{
		Use:     "load-test",
		Short:   "Runs a load test on a specified network",
		PreRunE: preRunCommand,
		Run:     runCommand,
	}

	setFlags(loadTestCmd)

	return loadTestCmd
}

func preRunCommand(cmd *cobra.Command, _ []string) error {
	return params.validateFlags()
}

func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringSliceVar(
		&params.jsonRPCAddresses,
		command.JSONRPCFlag,
		[]string{fmt.Sprintf("%s:%d", helper.AllInterfacesBinding, server.DefaultJSONRPCPort)},
		"the JSON-RPC interface addresses",
	)

	cmd.Flags().StringVar(
		&params.mnemonic,
		MnemonicFlag,
		"",
		"the mnemonic used to generate and fund virtual users",
	)

	cmd.Flags().StringVar(
		&params.loadTestType,
		loadTestTypeFlag,
		"erc20",
		"the type of load test to run (for now only supported erc20)",
	)

	cmd.Flags().StringVar(
		&params.loadTestName,
		loadTestNameFlag,
		"load test",
		"the name of the load test",
	)

	cmd.Flags().IntVar(
		&params.vus,
		vusFlag,
		1,
		"the number of virtual users",
	)

	cmd.Flags().IntVar(
		&params.txsPerUser,
		txsPerUserFlag,
		1,
		"the number of transactions per virtual user",
	)

	cmd.Flags().BoolVar(
		&params.dynamicTxs,
		dynamicTxsFlag,
		false,
		"indicates whether the load test should generate dynamic transactions",
	)

	cmd.Flags().DurationVar(
		&params.receiptsTimeout,
		ReceiptsTimeoutFlag,
		30*time.Second,
		"the timeout for waiting for transaction receipts",
	)

	cmd.Flags().DurationVar(
		&params.txPoolTimeout,
		txPoolTimeoutFlag,
		10*time.Minute,
		"the timeout for waiting for the transaction pool to empty",
	)

	cmd.Flags().BoolVar(
		&params.toJSON,
		SaveToJSONFlag,
		false,
		"saves results to JSON file",
	)

	cmd.Flags().BoolVar(
		&params.waitForTxPoolToEmpty,
		waitForTxPoolToEmptyFlag,
		false,
		"waits for tx pool to empty before collecting results",
	)

	cmd.Flags().IntVar(
		&params.batchSize,
		batchSizeFlag,
		1,
		"size of a batch of transactions to send to rpc node",
	)

	cmd.Flags().DurationVar(
		&params.executionTime,
		executionTimeFlag,
		0,
		"the duration of the load test expressed in time. When set, the load test will run for the specified duration",
	)

	cmd.Flags().IntVar(
		&params.stateReadThreads,
		stateReadThreadsFlag,
		0,
		"the number of state read threads (threads that read the state of the blockchain)",
	)

	cmd.Flags().IntVar(
		&params.txpoolReadThreads,
		txpoolReadThreadsFlag,
		0,
		"the number of txpool read threads (threads that read the transaction pool)",
	)

	cmd.Flags().IntVar(
		&params.sendWorkers,
		sendWorkersFlag,
		0,
		"the number of workers that send transactions on behalf of the VUs (each worker sends transactions for multiple VUs); 0 means normal behavior without workers.", //nolint:lll
	)

	cmd.Flags().IntVar(
		&params.receiversNum,
		receiversNumFlag,
		1,
		"the number of receivers for tokens being sent in load test",
	)

	cmd.Flags().Uint64Var(
		&params.blockNumberDeadband,
		blockNumberDeadbandFlag,
		0,
		"the deadband of block numbers, that is used when checking whether all the nodes are on the same block number",
	)

	cmd.Flags().BoolVar(
		&params.tearDown,
		tearDownFlag,
		false,
		"indicates whether to tear down the load test",
	)

	cmd.Flags().StringVar(
		&params.tokenContractAddress,
		tokenContractAddressFlag,
		"",
		"token smart contract address",
	)

	cmd.Flags().IntVar(
		&params.txsPerSecond,
		txsPerSecondFlag,
		0,
		"number of transactions per second",
	)

	_ = cmd.MarkFlagRequired(MnemonicFlag)
	_ = cmd.MarkFlagRequired(loadTestTypeFlag)
}

func runCommand(cmd *cobra.Command, _ []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	loadTestRunner := &runner.LoadTestRunner{}

	err := loadTestRunner.Run(cmd.Context(), runner.LoadTestConfig{
		Mnemonic:             params.mnemonic,
		LoadTestType:         params.loadTestType,
		LoadTestName:         params.loadTestName,
		JSONRPCUrls:          params.jsonRPCAddresses,
		ReceiptsTimeout:      params.receiptsTimeout,
		TxPoolTimeout:        params.txPoolTimeout,
		VUs:                  params.vus,
		TxsPerUser:           params.txsPerUser,
		BatchSize:            params.batchSize,
		DynamicTxs:           params.dynamicTxs,
		ResultsToJSON:        params.toJSON,
		WaitForTxPoolToEmpty: params.waitForTxPoolToEmpty,
		ExecutionTime:        params.executionTime,
		StateReadThreads:     params.stateReadThreads,
		TxPoolReadThreads:    params.txpoolReadThreads,
		SendWorkers:          params.sendWorkers,
		ReceiversNum:         params.receiversNum,
		BlockNumberDeadband:  params.blockNumberDeadband,
		TearDown:             params.tearDown,
		TokenContractAddress: types.StringToAddress(params.tokenContractAddress),
		TxsPerSecond:         params.txsPerSecond,
	})
	if err != nil {
		outputter.SetError(err)
	}
}
