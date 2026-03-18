package frameworkV2

import (
	"fmt"
	"math/big"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/command/polybftsecrets"
	"github.com/0xPolygon/polygon-edge/consensus/polybft/wallet"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/server/proto"
	txpoolProto "github.com/0xPolygon/polygon-edge/txpool/proto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type TestServerConfig struct {
	Name                  string
	JSONRPCPort           int64
	GRPCPort              int64
	P2PPort               int64
	Validator             bool
	DataDir               string
	Chain                 string
	LogLevel              string
	Relayer               bool
	NumBlockConfirmations uint64
	BridgeJSONRPC         string
	UseTLS                bool
	TLSCertFile           string
	TLSKeyFile            string
}

type TestServerConfigCallback func(*TestServerConfig)

const hostIP = "127.0.0.1"

var initialPortForServer = int64(12000)

func getOpenPortForServer() int64 {
	return atomic.AddInt64(&initialPortForServer, 1)
}

type TestServer struct {
	t *testing.T

	address       types.Address
	clusterConfig *TestClusterConfig
	config        *TestServerConfig
	node          *node
}

func (t *TestServer) GrpcAddr() string {
	return fmt.Sprintf("%s:%d", hostIP, t.config.GRPCPort)
}

func (t *TestServer) JSONRPCAddr() string {
	if t.config.UseTLS {
		return fmt.Sprintf("https://localhost:%d", t.config.JSONRPCPort)
	} else {
		return fmt.Sprintf("http://%s:%d", hostIP, t.config.JSONRPCPort)
	}
}

func (t *TestServer) BridgeJSONRPCAddr() string {
	return t.config.BridgeJSONRPC
}

func (t *TestServer) JSONRPC() *jsonrpc.EthClient {
	clt, err := jsonrpc.NewEthClient(t.JSONRPCAddr())
	if err != nil {
		t.t.Fatal(err)
	}

	return clt
}

func (t *TestServer) Conn() proto.SystemClient {
	conn, err := grpc.Dial(t.GrpcAddr(), grpc.WithInsecure())
	if err != nil {
		t.t.Fatal(err)
	}

	return proto.NewSystemClient(conn)
}

func (t *TestServer) DataDir() string {
	return t.config.DataDir
}

func (t *TestServer) TxnPoolOperator() txpoolProto.TxnPoolOperatorClient {
	conn, err := grpc.Dial(t.GrpcAddr(), grpc.WithInsecure())
	if err != nil {
		t.t.Fatal(err)
	}

	return txpoolProto.NewTxnPoolOperatorClient(conn)
}

func NewTestServer(t *testing.T, clusterConfig *TestClusterConfig, callback TestServerConfigCallback) *TestServer {
	t.Helper()

	config := &TestServerConfig{
		Name:        uuid.New().String(),
		JSONRPCPort: getOpenPortForServer(),
		GRPCPort:    getOpenPortForServer(),
		P2PPort:     getOpenPortForServer(),
	}

	if callback != nil {
		callback(config)
	}

	if config.DataDir == "" {
		dataDir, err := os.MkdirTemp("/tmp", "edge-e2e-")
		require.NoError(t, err)

		config.DataDir = dataDir
	}

	secretsManager, err := polybftsecrets.GetSecretsManager(config.DataDir, "", true)
	require.NoError(t, err)

	key, err := wallet.GetEcdsaFromSecret(secretsManager)
	require.NoError(t, err)

	srv := &TestServer{
		t:             t,
		clusterConfig: clusterConfig,
		address:       types.Address(key.Address()),
		config:        config,
	}
	srv.Start()

	return srv
}

func (t *TestServer) isRunning() bool {
	return t.node != nil
}

func (t *TestServer) Start() {
	config := t.config

	// Build arguments
	args := []string{
		"server",
		// add data dir
		"--data-dir", config.DataDir,
		// add custom chain
		"--chain", config.Chain,
		// enable p2p port
		"--libp2p", fmt.Sprintf(":%d", config.P2PPort),
		// grpc port
		"--grpc-address", fmt.Sprintf("localhost:%d", config.GRPCPort),
		// enable jsonrpc
		"--jsonrpc", fmt.Sprintf(":%d", config.JSONRPCPort),
		// minimal number of child blocks required for the parent block to be considered final
		"--num-block-confirmations", strconv.FormatUint(config.NumBlockConfirmations, 10),
		// TLS certificate file
		"--tls-cert-file", config.TLSCertFile,
		// TLS key file
		"--tls-key-file", config.TLSKeyFile,
	}

	if len(config.LogLevel) > 0 {
		args = append(args, "--log-level", config.LogLevel)
	} else {
		args = append(args, "--log-level", "DEBUG")
	}

	if config.UseTLS {
		args = append(args, "--use-tls")
	}

	// Start the server
	stdout := t.clusterConfig.GetStdout(t.config.Name)

	node, err := newNode(t.clusterConfig.Binary, args, stdout)
	if err != nil {
		t.t.Fatal(err)
	}

	t.node = node

	// Wait some time for network to be initialized in order to avoid 'parallel' start issue in tests
	time.Sleep(250 * time.Millisecond)
}

func (t *TestServer) Stop() {
	if err := t.node.Stop(); err != nil {
		t.t.Fatal(err)
	}

	t.node = nil
}

func (t *TestServer) WaitForNonZeroBalance(address types.Address, dur time.Duration) (*big.Int, error) {
	timer := time.NewTimer(dur)
	defer timer.Stop()

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	rpcClient := t.JSONRPC()

	for {
		select {
		case <-timer.C:
			return nil, fmt.Errorf("timeout occurred while waiting for balance ")
		case <-ticker.C:
			balance, err := rpcClient.GetBalance(address, jsonrpc.LatestBlockNumberOrHash)
			if err != nil {
				return nil, fmt.Errorf("error getting balance")
			}

			if balance.Cmp(big.NewInt(0)) == 1 {
				return balance, nil
			}
		}
	}
}
