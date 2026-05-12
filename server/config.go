package server

import (
	"net"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/network"
	"github.com/0xPolygon/polygon-edge/secrets"
)

const DefaultGRPCPort int = 9632
const DefaultJSONRPCPort int = 8545

// Config is used to parametrize the minimal client
type Config struct {
	Chain *chain.Chain

	JSONRPC    *JSONRPC
	GRPCAddr   *net.TCPAddr
	LibP2PAddr *net.TCPAddr

	PriceLimit         uint64
	MaxAccountEnqueued uint64
	MaxSlots           uint64
	TxGossipBatchSize  uint64
	JournalRotateSize  uint64

	Telemetry *Telemetry
	Network   *network.Config

	DataDir     string
	RestoreFile *string

	Seal bool

	SecretsManager *secrets.SecretsManagerConfig
	SignerConfig   *signer.SignerConfig

	LogLevel hclog.Level

	JSONLogFormat bool

	LogFilePath string

	UseTLS bool

	TLSCertFile string

	TLSKeyFile string

	BlockCacheTTL time.Duration

	BlockCacheCapacity uint64

	MaxRequestBodySize int64

	JSONRPCTimeout time.Duration

	Relayer bool

	NumBlockConfirmations uint64
	MetricsInterval       time.Duration

	DisableTxPoolEndpoints  bool
	EnableAllDebugEndpoints bool

	// JumpdestCacheSize controls the size of the per-process JUMPDEST bitmap
	// cache used by the EVM (see `state/runtime/evm/jumpdest_cache.go`).
	// 0 disables the cache.
	JumpdestCacheSize uint64
}

// Telemetry holds the config details for metric services
type Telemetry struct {
	PrometheusAddr *net.TCPAddr
}

// JSONRPC holds the config details for the JSON-RPC server
type JSONRPC struct {
	JSONRPCAddr              *net.TCPAddr
	AccessControlAllowOrigin []string
	BatchLengthLimit         uint64
	BlockRangeLimit          uint64
	ConcurrentRequestsDebug  uint64
	WebSocketReadLimit       uint64
}
