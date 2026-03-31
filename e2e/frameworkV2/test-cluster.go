package frameworkV2

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/command/genesis"
	"github.com/0xPolygon/polygon-edge/consensus/polybft"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/contractsapi"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/secrets/helper"
	"github.com/0xPolygon/polygon-edge/secrets/local"
	"github.com/0xPolygon/polygon-edge/server"
	"github.com/0xPolygon/polygon-edge/txrelayerv2"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

const (
	// envE2ETestsEnabled signal whether the e2e tests will run
	envE2ETestsEnabled = "E2E_TESTS"

	// envLogsEnabled signal whether the output of the nodes get piped to a log file
	envLogsEnabled = "E2E_LOGS"

	// envLogLevel specifies log level of each node
	envLogLevel = "E2E_LOG_LEVEL"

	// envStdoutEnabled signal whether the output of the nodes get piped to stdout
	envStdoutEnabled = "E2E_STDOUT"

	// prefix for validator directory
	defaultValidatorPrefix = "test-chain-"

	// prefix for non validators directory
	nonValidatorPrefix = "test-non-validator-"

	// NativeTokenMintableTestCfg is the test native token config for Supernets originated native tokens
	NativeTokenMintableTestCfg = "Mintable Edge Coin:MEC:18" //nolint:gosec
)

type NodeType int

const (
	None      NodeType = 0
	Validator NodeType = 1
	Relayer   NodeType = 2
)

func (nt NodeType) IsSet(value NodeType) bool {
	return nt&value == value
}

func (nt *NodeType) Append(value NodeType) {
	*nt |= value
}

var (
	startTime              int64
	testRewardWalletAddr   = types.StringToAddress("0xFFFFFFFF")
	ProxyContractAdminAddr = "0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed"
)

func init() {
	startTime = time.Now().UTC().UnixMilli()
}

func resolveBinary() string {
	bin := os.Getenv("EDGE_BINARY")
	if bin != "" {
		return bin
	}
	// fallback
	return "polygon-edge"
}

type TestClusterConfig struct {
	t *testing.T

	Name                 string
	Premine              []string // address[:amount]
	StakeAmounts         []*big.Int
	BootnodeCount        int
	NonValidatorCount    int
	WithLogs             bool
	WithStdout           bool
	HasBridge            bool
	LogsDir              string
	TmpDir               string
	BlockGasLimit        uint64
	BlockTime            time.Duration
	BurnContract         *polybft.BurnContractInfo
	ValidatorPrefix      string
	Binary               string
	ValidatorSetSize     uint64
	EpochSize            int
	EpochReward          int
	NativeTokenConfigRaw string
	BaseFeeConfig        string
	SecretsCallback      func([]types.Address, *TestClusterConfig)
	RewardWallet         string

	ContractDeployerAllowListAdmin   []types.Address
	ContractDeployerAllowListEnabled []types.Address
	ContractDeployerBlockListAdmin   []types.Address
	ContractDeployerBlockListEnabled []types.Address
	TransactionsAllowListAdmin       []types.Address
	TransactionsAllowListEnabled     []types.Address
	TransactionsBlockListAdmin       []types.Address
	TransactionsBlockListEnabled     []types.Address

	NumBlockConfirmations uint64

	InitialTrieDB    string
	InitialStateRoot types.Hash

	IsPropertyTest  bool
	TestRewardToken string

	ProxyContractsAdmin string

	logsDirOnce sync.Once

	UseTLS      bool
	TLSCertFile string
	TLSKeyFile  string

	Consensus server.ConsensusType
}

func (c *TestClusterConfig) Dir(name string) string {
	return filepath.Join(c.TmpDir, name)
}

func (c *TestClusterConfig) GetStdout(name string, custom ...io.Writer) io.Writer {
	writers := []io.Writer{}

	if c.WithLogs {
		c.logsDirOnce.Do(func() {
			c.initLogsDir()
		})

		f, err := os.OpenFile(filepath.Join(c.LogsDir, name+".log"), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0600)
		if err != nil {
			c.t.Fatal(err)
		}

		writers = append(writers, f)

		c.t.Cleanup(func() {
			err = f.Close()
			if err != nil {
				c.t.Logf("Failed to close file. Error: %s", err)
			}
		})
	}

	if c.WithStdout {
		writers = append(writers, os.Stdout)
	}

	if len(custom) > 0 {
		writers = append(writers, custom...)
	}

	if len(writers) == 0 {
		return io.Discard
	}

	return io.MultiWriter(writers...)
}

func (c *TestClusterConfig) initLogsDir() {
	logsDir := path.Join("..", fmt.Sprintf("e2e-logs-%d", startTime), c.t.Name())
	if c.IsPropertyTest {
		// property tests run cluster multiple times, so each cluster run will be in the main folder
		// e2e-logs-{someNumber}/NameOfPropertyTest/NameOfPropertyTest-{someNumber}
		// to have a separation between logs of each cluster run
		logsDir = path.Join(logsDir, fmt.Sprintf("%v-%d", c.t.Name(), time.Now().UTC().Unix()))
	}

	if err := common.CreateDirSafe(logsDir, 0750); err != nil {
		c.t.Fatal(err)
	}

	c.t.Logf("logs enabled for e2e test: %s", logsDir)
	c.LogsDir = logsDir
}

func (c *TestClusterConfig) GetProxyContractsAdmin() string {
	proxyAdminAddr := c.ProxyContractsAdmin
	if proxyAdminAddr == "" {
		proxyAdminAddr = ProxyContractAdminAddr
	}

	return proxyAdminAddr
}

func (c *TestClusterConfig) getStakeAmount(validatorIndex int) *big.Int {
	l := len(c.StakeAmounts)
	if l == 0 || l <= validatorIndex || validatorIndex < 0 {
		return command.DefaultStake
	}

	return c.StakeAmounts[validatorIndex]
}

type TestCluster struct {
	Config      *TestClusterConfig
	Servers     []*TestServer
	initialPort int64

	once         sync.Once
	failCh       chan struct{}
	executionErr error
}

type ClusterOption func(*TestClusterConfig)

func WithPremine(amounts map[types.Address]*big.Int) ClusterOption {
	return func(h *TestClusterConfig) {
		for addr, amount := range amounts {
			h.Premine = append(h.Premine, fmt.Sprintf("%s:0x%s", addr.String(), amount.String()))
		}
	}
}
func WithSecretsCallback(fn func([]types.Address, *TestClusterConfig)) ClusterOption {
	return func(h *TestClusterConfig) {
		h.SecretsCallback = fn
	}
}

func WithNonValidators(num int) ClusterOption {
	return func(h *TestClusterConfig) {
		h.NonValidatorCount = num
	}
}

func WithValidatorSnapshot(validatorsLen uint64) ClusterOption {
	return func(h *TestClusterConfig) {
		h.ValidatorSetSize = validatorsLen
	}
}

func WithBaseFeeConfig(config string) ClusterOption {
	return func(h *TestClusterConfig) {
		if config == "" {
			h.BaseFeeConfig = command.DefaultGenesisBaseFeeConfig
		} else {
			h.BaseFeeConfig = config
		}
	}
}

func WithGenesisState(databasePath string, stateRoot types.Hash) ClusterOption {
	return func(h *TestClusterConfig) {
		h.InitialTrieDB = databasePath
		h.InitialStateRoot = stateRoot
	}
}

func WithBootnodeCount(cnt int) ClusterOption {
	return func(h *TestClusterConfig) {
		h.BootnodeCount = cnt
	}
}

func WithEpochSize(epochSize int) ClusterOption {
	return func(h *TestClusterConfig) {
		h.EpochSize = epochSize
	}
}

func WithEpochReward(epochReward int) ClusterOption {
	return func(h *TestClusterConfig) {
		h.EpochReward = epochReward
	}
}

func WithBlockTime(blockTime time.Duration) ClusterOption {
	return func(h *TestClusterConfig) {
		h.BlockTime = blockTime
	}
}

func WithBlockGasLimit(blockGasLimit uint64) ClusterOption {
	return func(h *TestClusterConfig) {
		h.BlockGasLimit = blockGasLimit
	}
}

func WithBurnContract(burnContract *polybft.BurnContractInfo) ClusterOption {
	return func(h *TestClusterConfig) {
		h.BurnContract = burnContract
	}
}

func WithNumBlockConfirmations(numBlockConfirmations uint64) ClusterOption {
	return func(h *TestClusterConfig) {
		h.NumBlockConfirmations = numBlockConfirmations
	}
}

func WithContractDeployerAllowListAdmin(addr types.Address) ClusterOption {
	return func(h *TestClusterConfig) {
		h.ContractDeployerAllowListAdmin = append(h.ContractDeployerAllowListAdmin, addr)
	}
}

func WithContractDeployerAllowListEnabled(addr types.Address) ClusterOption {
	return func(h *TestClusterConfig) {
		h.ContractDeployerAllowListEnabled = append(h.ContractDeployerAllowListEnabled, addr)
	}
}

func WithContractDeployerBlockListAdmin(addr types.Address) ClusterOption {
	return func(h *TestClusterConfig) {
		h.ContractDeployerBlockListAdmin = append(h.ContractDeployerBlockListAdmin, addr)
	}
}

func WithContractDeployerBlockListEnabled(addr types.Address) ClusterOption {
	return func(h *TestClusterConfig) {
		h.ContractDeployerBlockListEnabled = append(h.ContractDeployerBlockListEnabled, addr)
	}
}

func WithTransactionsAllowListAdmin(addr types.Address) ClusterOption {
	return func(h *TestClusterConfig) {
		h.TransactionsAllowListAdmin = append(h.TransactionsAllowListAdmin, addr)
	}
}

func WithTransactionsAllowListEnabled(addr types.Address) ClusterOption {
	return func(h *TestClusterConfig) {
		h.TransactionsAllowListEnabled = append(h.TransactionsAllowListEnabled, addr)
	}
}

func WithTransactionsBlockListAdmin(addr types.Address) ClusterOption {
	return func(h *TestClusterConfig) {
		h.TransactionsBlockListAdmin = append(h.TransactionsBlockListAdmin, addr)
	}
}

func WithTransactionsBlockListEnabled(addr types.Address) ClusterOption {
	return func(h *TestClusterConfig) {
		h.TransactionsBlockListEnabled = append(h.TransactionsBlockListEnabled, addr)
	}
}

func WithPropertyTestLogging() ClusterOption {
	return func(h *TestClusterConfig) {
		h.IsPropertyTest = true
	}
}

func WithNativeTokenConfig(tokenConfigRaw string) ClusterOption {
	return func(h *TestClusterConfig) {
		h.NativeTokenConfigRaw = tokenConfigRaw
	}
}

func WithTestRewardToken() ClusterOption {
	return func(h *TestClusterConfig) {
		h.TestRewardToken = hex.EncodeToString(contractsapi.TestRewardToken.DeployedBytecode)
	}
}

func WithProxyContractsAdmin(address string) ClusterOption {
	return func(h *TestClusterConfig) {
		h.ProxyContractsAdmin = address
	}
}

func WithRewardWallet(rewardWallet string) ClusterOption {
	return func(h *TestClusterConfig) {
		h.RewardWallet = rewardWallet
	}
}

func WithHTTPS() ClusterOption {
	return func(h *TestClusterConfig) {
		h.UseTLS = true
	}
}

func WithTLSCertificate(certFile string, keyFile string) ClusterOption {
	return func(h *TestClusterConfig) {
		h.TLSCertFile = certFile
		h.TLSKeyFile = keyFile
	}
}

func WithConsensusType(consensusType server.ConsensusType) ClusterOption {
	return func(h *TestClusterConfig) {
		h.Consensus = consensusType
	}
}

func isTrueEnv(e string) bool {
	return strings.ToLower(os.Getenv(e)) == "true"
}

func NewPropertyTestCluster(t *testing.T, validatorsCount int, opts ...ClusterOption) *TestCluster {
	t.Helper()

	opts = append(opts, WithPropertyTestLogging())

	return NewTestCluster(t, validatorsCount, opts...)
}

func NewTestCluster(t *testing.T, validatorsCount int, opts ...ClusterOption) *TestCluster {
	t.Helper()

	var err error

	config := &TestClusterConfig{
		t:             t,
		WithLogs:      isTrueEnv(envLogsEnabled),
		WithStdout:    isTrueEnv(envStdoutEnabled),
		Binary:        resolveBinary(),
		EpochSize:     10,
		EpochReward:   1,
		BlockGasLimit: 1e7, // 10M
		StakeAmounts:  []*big.Int{},
		HasBridge:     false,
		Consensus:     server.IBFTConsensus,
	}

	if config.ValidatorPrefix == "" {
		config.ValidatorPrefix = defaultValidatorPrefix
	}

	for _, opt := range opts {
		opt(config)
	}

	if !isTrueEnv(envE2ETestsEnabled) {
		var testType string
		if config.IsPropertyTest {
			testType = "property"
		} else {
			testType = "integration"
		}

		t.Skipf("%s tests are disabled.", testType)
	}

	config.TmpDir, err = os.MkdirTemp("/tmp", "e2e-polybft-")
	require.NoError(t, err)

	cluster := &TestCluster{
		Servers:     []*TestServer{},
		Config:      config,
		initialPort: 30300,
		failCh:      make(chan struct{}),
		once:        sync.Once{},
	}

	// in case no validators are specified in opts, all nodes will be validators
	if cluster.Config.ValidatorSetSize == 0 {
		cluster.Config.ValidatorSetSize = uint64(validatorsCount)
	}

	// run init accounts for validators
	addresses, err := cluster.InitSecrets(cluster.Config.ValidatorPrefix, int(cluster.Config.ValidatorSetSize))
	require.NoError(t, err)

	if cluster.Config.SecretsCallback != nil {
		cluster.Config.SecretsCallback(addresses, cluster.Config)
	}

	if config.NonValidatorCount > 0 {
		// run init accounts for non-validators
		// we don't call secrets callback on non-validators,
		// since we have nothing to premine nor stake for non validators
		_, err = cluster.InitSecrets(nonValidatorPrefix, config.NonValidatorCount)
		require.NoError(t, err)
	}

	genesisPath := path.Join(config.TmpDir, "genesis.json")

	{
		// run genesis configuration population
		args := []string{
			"genesis",
			"--consensus", string(cluster.Config.Consensus),
			"--validators-path", config.TmpDir,
			"--validators-prefix", cluster.Config.ValidatorPrefix,
			"--dir", genesisPath,
			"--block-gas-limit", strconv.FormatUint(cluster.Config.BlockGasLimit, 10),
			"--epoch-size", strconv.Itoa(cluster.Config.EpochSize),
			"--epoch-reward", strconv.Itoa(cluster.Config.EpochReward),
			"--premine", "0x0000000000000000000000000000000000000000:0x" + ethgo.Ether(10).String(),
			"--trieroot", cluster.Config.InitialStateRoot.String(),
		}

		if cluster.Config.RewardWallet != "" {
			args = append(args, "--reward-wallet", cluster.Config.RewardWallet)
		} else {
			args = append(args, "--reward-wallet", testRewardWalletAddr.String())
		}

		if cluster.Config.BlockTime != 0 {
			args = append(args, "--block-time",
				cluster.Config.BlockTime.String())
		}

		if cluster.Config.TestRewardToken != "" {
			args = append(args, "--reward-token-code", cluster.Config.TestRewardToken)
		}

		if cluster.Config.BaseFeeConfig != "" {
			args = append(args, "--base-fee-config", cluster.Config.BaseFeeConfig)
		}

		if cluster.Config.NativeTokenConfigRaw != "" {
			args = append(args, "--native-token-config", cluster.Config.NativeTokenConfigRaw)
		}

		tokenConfig, err := polybft.ParseRawTokenConfig(cluster.Config.NativeTokenConfigRaw)
		require.NoError(t, err)

		if len(cluster.Config.Premine) != 0 {
			// only add premine flags in genesis if token is mintable
			for _, premine := range cluster.Config.Premine {
				args = append(args, "--premine", premine)
			}
		}

		burnContract := cluster.Config.BurnContract
		if burnContract != nil {
			args = append(args, "--burn-contract",
				fmt.Sprintf("%d:%s:%s",
					burnContract.BlockNumber, burnContract.Address, burnContract.DestinationAddress))
		}

		if tokenConfig.IsMintable && len(cluster.Config.StakeAmounts) != 0 {
			for i, addr := range addresses {
				args = append(args, "--stake", fmt.Sprintf("%s:%s", addr.String(), cluster.Config.getStakeAmount(i).String()))
			}
		}

		validators, err := genesis.ReadValidatorsByPrefix(
			cluster.Config.TmpDir, cluster.Config.ValidatorPrefix)
		require.NoError(t, err)

		if cluster.Config.BootnodeCount > 0 {
			bootNodesCnt := cluster.Config.BootnodeCount
			if len(validators) < bootNodesCnt {
				bootNodesCnt = len(validators)
			}

			for i := 0; i < bootNodesCnt; i++ {
				args = append(args, "--bootnode", validators[i].MultiAddr)
			}
		}

		proxyAdminAddr := cluster.Config.ProxyContractsAdmin
		if proxyAdminAddr == "" {
			proxyAdminAddr = ProxyContractAdminAddr
		}

		args = append(args, "--proxy-contracts-admin", proxyAdminAddr)

		// run genesis command with all the arguments
		err = cluster.cmdRun(args...)
		require.NoError(t, err)
	}

	for i := 1; i <= int(cluster.Config.ValidatorSetSize); i++ {
		nodeType := Validator
		if i == 1 {
			nodeType.Append(Relayer)
		}

		dir := cluster.Config.ValidatorPrefix + strconv.Itoa(i)
		cluster.InitTestServer(t, dir, nodeType)
	}

	for i := 1; i <= cluster.Config.NonValidatorCount; i++ {
		dir := nonValidatorPrefix + strconv.Itoa(i)
		cluster.InitTestServer(t, dir, None)
	}

	return cluster
}

func (c *TestCluster) InitTestServer(t *testing.T,
	dataDir string, nodeType NodeType) {
	t.Helper()

	logLevel := os.Getenv(envLogLevel)

	dataDir = c.Config.Dir(dataDir)
	if c.Config.InitialTrieDB != "" {
		err := CopyDir(c.Config.InitialTrieDB, filepath.Join(dataDir, "trie"))
		if err != nil {
			t.Fatal(err)
		}
	}

	srv := NewTestServer(t, c.Config, func(config *TestServerConfig) {
		config.DataDir = dataDir
		config.Validator = nodeType.IsSet(Validator)
		config.Chain = c.Config.Dir("genesis.json")
		config.P2PPort = c.getOpenPort()
		config.LogLevel = logLevel
		config.Relayer = nodeType.IsSet(Relayer)
		config.NumBlockConfirmations = c.Config.NumBlockConfirmations
		config.UseTLS = c.Config.UseTLS
		config.TLSCertFile = c.Config.TLSCertFile
		config.TLSKeyFile = c.Config.TLSKeyFile
	})

	// watch the server for stop signals. It is important to fix the specific
	// 'node' reference since 'TestServer' creates a new one if restarted.
	go func(node *node) {
		<-node.Wait()

		if !node.ExitResult().Signaled {
			c.Fail(fmt.Errorf("server at dir '%s' has stopped unexpectedly", dataDir))
		}
	}(srv.node)

	c.Servers = append(c.Servers, srv)
}

func (c *TestCluster) cmdRun(args ...string) error {
	return runCommand(c.Config.Binary, args, c.Config.GetStdout(args[0]))
}

func (c *TestCluster) Fail(err error) {
	c.once.Do(func() {
		c.executionErr = err
		close(c.failCh)
	})
}

func (c *TestCluster) Stop() {
	for _, srv := range c.Servers {
		if srv.isRunning() {
			srv.Stop()
		}
	}
}

func (c *TestCluster) Stats(t *testing.T) {
	t.Helper()

	for index, i := range c.Servers {
		if !i.isRunning() {
			continue
		}

		num, err := i.JSONRPC().BlockNumber()
		t.Log("Stats node", index, "err", err, "block", num, "validator", i.config.Validator)
	}
}

func (c *TestCluster) WaitUntil(timeout, pollFrequency time.Duration, handler func() bool) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return fmt.Errorf("timeout")
		case <-c.failCh:
			return c.executionErr
		case <-time.After(pollFrequency):
		}

		if handler() {
			return nil
		}
	}
}

func (c *TestCluster) WaitForReady(t *testing.T) {
	t.Helper()

	require.NoError(t, c.WaitForBlock(1, time.Minute))
}

func (c *TestCluster) WaitForBlock(n uint64, timeout time.Duration) error {
	timer := time.NewTimer(timeout)

	ok := false
	for !ok {
		select {
		case <-timer.C:
			return fmt.Errorf("wait for block timeout")
		case <-time.After(2 * time.Second):
		}

		ok = true

		for _, i := range c.Servers {
			if !i.isRunning() {
				continue
			}

			num, err := i.JSONRPC().BlockNumber()

			if err != nil || num < n {
				ok = false

				break
			}
		}
	}

	return nil
}

// WaitForGeneric waits until all running servers returns true from fn callback or timeout defined by dur occurs
func (c *TestCluster) WaitForGeneric(dur time.Duration, fn func(*TestServer) bool) error {
	return c.WaitUntil(dur, 2*time.Second, func() bool {
		for _, srv := range c.Servers {
			// query only running servers
			if srv.isRunning() && !fn(srv) {
				return false
			}
		}

		return true
	})
}

func (c *TestCluster) getOpenPort() int64 {
	c.initialPort++

	return c.initialPort
}

// runCommand executes command with given arguments
func runCommand(binary string, args []string, stdout io.Writer) error {
	var stdErr bytes.Buffer

	cmd := exec.Command(binary, args...) //nolint:gosec
	cmd.Stderr = &stdErr
	cmd.Stdout = stdout

	if err := cmd.Run(); err != nil {
		if stdErr.Len() > 0 {
			return fmt.Errorf("failed to execute command: %s", stdErr.String())
		}

		return fmt.Errorf("failed to execute command: %w", err)
	}

	if stdErr.Len() > 0 {
		return fmt.Errorf("error during command execution: %s", stdErr.String())
	}

	return nil
}

// RunEdgeCommand - calls a command line edge function
func RunEdgeCommand(args []string, stdout io.Writer) error {
	return runCommand(resolveBinary(), args, stdout)
}

// InitSecrets initializes account(s) secrets with given prefix.
// (secrets are being stored in the temp directory created by given e2e test execution)
func (c *TestCluster) InitSecrets(prefix string, count int) ([]types.Address, error) {
	var b bytes.Buffer

	args := []string{
		"secrets", "init",
		"--data-dir", path.Join(c.Config.TmpDir, prefix),
		"--num", strconv.Itoa(count),
		"--insecure",
	}
	stdOut := c.Config.GetStdout("secrets-init", &b)

	if err := runCommand(c.Config.Binary, args, stdOut); err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`\(address\) = 0x([a-fA-F0-9]+)`)
	parsed := re.FindAllStringSubmatch(b.String(), -1)
	result := make([]types.Address, len(parsed))

	for i, v := range parsed {
		result[i] = types.StringToAddress(v[1])
	}

	return result, nil
}

func CopyDir(source, destination string) error {
	err := os.Mkdir(destination, 0755) //nolint:gosec
	if err != nil {
		return err
	}

	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		relPath := strings.Replace(path, source, "", 1)
		if relPath == "" {
			return nil
		}

		data, err := os.ReadFile(filepath.Join(source, relPath))
		if err != nil {
			return err
		}

		return os.WriteFile(filepath.Join(destination, relPath), data, 0600)
	})
}

func (c *TestCluster) Deploy(t *testing.T, sender *crypto.ECDSAKey, bytecode []byte) *TestTxn {
	t.Helper()

	tx := &types.Transaction{
		Type:  types.LegacyTx,
		From:  sender.Address(),
		Input: bytecode,
	}

	return c.SendTxn(t, sender, tx)
}

func (c *TestCluster) Transfer(t *testing.T, sender *crypto.ECDSAKey, target types.Address, value *big.Int) *TestTxn {
	t.Helper()

	tx := &types.Transaction{
		Type:  types.LegacyTx,
		From:  sender.Address(),
		Value: value,
		To:    &target,
	}

	return c.SendTxn(t, sender, tx)
}

// SendTxn sends a transaction
func (c *TestCluster) SendTxn(t *testing.T, sender *crypto.ECDSAKey, txn *types.Transaction) *TestTxn {
	t.Helper()

	txRelayer, err := txrelayerv2.NewTxRelayer(
		txrelayerv2.WithIPAddress(c.Servers[0].JSONRPCAddr()),
		txrelayerv2.WithReceiptsTimeout(1*time.Minute),
		txrelayerv2.WithEstimateGasFallback(),
	)
	require.NoError(t, err)

	receipt, err := txRelayer.SendTransaction(txn, sender)
	if err != nil {
		t.Errorf("failed to send transaction: %s", err.Error())
	}

	return &TestTxn{
		txn:     txn,
		receipt: receipt,
	}
}

type TestTxn struct {
	txn     *types.Transaction
	receipt *ethgo.Receipt
}

// Txn returns the raw transaction that was sent
func (t *TestTxn) Txn() *types.Transaction {
	return t.txn
}

// Receipt returns the receipt of the transaction
func (t *TestTxn) Receipt() *ethgo.Receipt {
	return t.receipt
}

// Succeed returns whether the transaction succeed and it was not reverted
func (t *TestTxn) Succeed() bool {
	return t.receipt != nil && t.receipt.Status == uint64(types.ReceiptSuccess)
}

// Failed returns whether the transaction failed
func (t *TestTxn) Failed() bool {
	return t.receipt == nil || t.receipt.Status == uint64(types.ReceiptFailed)
}

// Reverted returns whether the transaction failed and was reverted consuming
// all the gas from the call
func (t *TestTxn) Reverted() bool {
	return t.Failed() && t.txn.Gas == t.receipt.GasUsed
}

// ReadValidatorBLSKey reads the BLS public key for a validator at the given dataDir
// using the local secrets manager — same approach as the rest of the framework.
func ReadValidatorBLSKey(dataDir string) (string, error) {
	sm, err := local.SecretsManagerFactory(
		nil,
		&secrets.SecretsManagerParams{
			Logger: hclog.NewNullLogger(),
			Extra: map[string]interface{}{
				secrets.Path: dataDir,
			},
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to create secrets manager for %s: %w", dataDir, err)
	}

	return helper.LoadBLSPublicKey(sm)
}
