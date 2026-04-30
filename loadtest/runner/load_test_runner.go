package runner

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
)

const (
	ERC20TestType  = "erc20"
	PTokenTestType = "ptoken"
)

func IsLoadTestSupported(loadTestType string) bool {
	ltp := strings.ToLower(loadTestType)

	return ltp == ERC20TestType || ltp == PTokenTestType
}

type account struct {
	index int
	nonce uint64
	key   *crypto.ECDSAKey
	id    string
}

type BlockInfo struct {
	Number    uint64
	CreatedAt uint64
	NumTxs    int

	GasUsed        *big.Int
	GasLimit       *big.Int
	GasUtilization float64

	TPS       float64
	BlockTime float64
}

// LoadTestConfig represents the configuration for a load test.
type LoadTestConfig struct {
	Mnemonic string // Mnemonnic is the mnemonic phrase used for account generation, and VUs funding.

	LoadTestType string // LoadTestType is the type of load test.
	LoadTestName string // LoadTestName is the name of the load test.

	JSONRPCUrls     []string      // JSONRPCUrls is the URL list of the JSON-RPC servers.
	ReceiptsTimeout time.Duration // ReceiptsTimeout is the timeout for waiting for transaction receipts.
	TxPoolTimeout   time.Duration // TxPoolTimeout is the timeout for waiting for tx pool to empty.

	VUs        int  // VUs is the number of virtual users.
	TxsPerUser int  // TxsPerUser is the number of transactions per user.
	BatchSize  int  // BatchSize is the number of transactions to send in a single batch.
	DynamicTxs bool // DynamicTxs indicates whether the load test should generate dynamic transactions.

	ResultsToJSON        bool // ResultsToJSON indicates whether the results should be written in JSON format.
	WaitForTxPoolToEmpty bool // WaitForTxPoolToEmpty indicates whether the load test
	// should wait for the tx pool to empty before gathering results

	// Performance parameters
	ExecutionTime     time.Duration // ExecutionTime is the duration for which the load test should run.
	StateReadThreads  int           // StateReadThreads is the number of threads to read state.
	TxPoolReadThreads int           // TxPoolReadThreads is the number of threads to read tx pool.

	ReceiversNum int // ReceiversNum is the number of receivers for different types of tokens

	// BlockNumberDeadband is the maximum allowed discrepancy in the latest block numbers among the nodes
	BlockNumberDeadband uint64

	// Tear down for the load test
	TearDown bool // TearDown indicates whether to tear down the load test.

	TokenContractAddress types.Address
}

// LoadTestRunner represents a runner for load tests.
type LoadTestRunner struct{}

// Run executes the load test based on the provided LoadTestConfig.
// It determines the load test type from the configuration and creates
// the corresponding runner. Then, it runs the load test using the
// created runner and returns any error encountered during the process.
func (r *LoadTestRunner) Run(ctx context.Context, cfg LoadTestConfig) error {
	switch strings.ToLower(cfg.LoadTestType) {
	case ERC20TestType:
		erc20Runner, err := NewERC20Runner(cfg)
		if err != nil {
			return err
		}

		return erc20Runner.Run(ctx)
	case PTokenTestType:
		pTokenRunner, err := NewPTokenRunner(cfg)
		if err != nil {
			return err
		}

		return pTokenRunner.Run(ctx)
	default:
		return fmt.Errorf("unknown load test type %s", cfg.LoadTestType)
	}
}
