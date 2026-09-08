package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/0xPolygon/polygon-edge/network"
	"github.com/0xPolygon/polygon-edge/state/runtime/evm"
	"github.com/hashicorp/hcl"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"gopkg.in/yaml.v3"
)

// Config defines the server configuration params
type Config struct {
	GenesisPath              string        `json:"chain_config" yaml:"chain_config"`
	SecretsConfigPath        string        `json:"secrets_config" yaml:"secrets_config"`
	SignerConfigPath         string        `json:"signer_config" yaml:"signer_config"`
	DataDir                  string        `json:"data_dir" yaml:"data_dir"`
	BlockGasTarget           string        `json:"block_gas_target" yaml:"block_gas_target"`
	GRPCAddr                 string        `json:"grpc_addr" yaml:"grpc_addr"`
	JSONRPCAddr              string        `json:"jsonrpc_addr" yaml:"jsonrpc_addr"`
	Telemetry                *Telemetry    `json:"telemetry" yaml:"telemetry"`
	Network                  *Network      `json:"network" yaml:"network"`
	ShouldSeal               bool          `json:"seal" yaml:"seal"`
	TxPool                   *TxPool       `json:"tx_pool" yaml:"tx_pool"`
	LogLevel                 string        `json:"log_level" yaml:"log_level"`
	RestoreFile              string        `json:"restore_file" yaml:"restore_file"`
	Headers                  *Headers      `json:"headers" yaml:"headers"`
	LogFilePath              string        `json:"log_to" yaml:"log_to"`
	JSONRPCBatchRequestLimit uint64        `json:"json_rpc_batch_request_limit" yaml:"json_rpc_batch_request_limit"`
	JSONRPCBlockRangeLimit   uint64        `json:"json_rpc_block_range_limit" yaml:"json_rpc_block_range_limit"`
	JSONLogFormat            bool          `json:"json_log_format" yaml:"json_log_format"`
	CorsAllowedOrigins       []string      `json:"cors_allowed_origins" yaml:"cors_allowed_origins"`
	UseTLS                   bool          `json:"use_tls" yaml:"use_tls"`
	TLSCertFile              string        `json:"tls_cert_file" yaml:"tls_cert_file"`
	TLSKeyFile               string        `json:"tls_key_file" yaml:"tls_key_file"`
	BlockCacheTTL            time.Duration `json:"block_cache_ttl" yaml:"block_cache_ttl"`
	BlockCacheCapacity       uint64        `json:"block_cache_capacity" yaml:"block_cache_capacity"`
	MaxRequestBodySize       int64         `json:"max_request_body_size" yaml:"max_request_body_size"`
	JSONRPCTimeout           time.Duration `json:"json_rpc_timeout" yaml:"json_rpc_timeout"`
	RPCGasCap                uint64        `json:"rpc_gas_cap" yaml:"rpc_gas_cap"`
	JSONRPCBatchCostLimit    uint64        `json:"json_rpc_batch_cost_limit" yaml:"json_rpc_batch_cost_limit"`
	JSONRPCMaxResponseSize   uint64        `json:"json_rpc_max_response_size" yaml:"json_rpc_max_response_size"`

	Relayer               bool   `json:"relayer" yaml:"relayer"`
	NumBlockConfirmations uint64 `json:"num_block_confirmations" yaml:"num_block_confirmations"`

	ConcurrentRequestsDebug uint64 `json:"concurrent_requests_debug" yaml:"concurrent_requests_debug"`
	WebSocketReadLimit      uint64 `json:"web_socket_read_limit" yaml:"web_socket_read_limit"`

	// JSONRPCFilterLimit caps the filters and subscriptions active on the node, and
	// JSONRPCFilterLimitPerConn caps those held by a single web socket connection. Zero
	// disables the respective limit.
	JSONRPCFilterLimit        uint64 `json:"json_rpc_filter_limit" yaml:"json_rpc_filter_limit"`
	JSONRPCFilterLimitPerConn uint64 `json:"json_rpc_filter_limit_per_connection" yaml:"json_rpc_filter_limit_per_connection"` //nolint:lll

	// Websocket request and connection ceilings. Zero disables the respective limit.
	JSONRPCWSMaxConnections     uint64 `json:"json_rpc_ws_max_connections" yaml:"json_rpc_ws_max_connections"`
	JSONRPCWSMaxInFlight        uint64 `json:"json_rpc_ws_max_in_flight" yaml:"json_rpc_ws_max_in_flight"`
	JSONRPCWSMaxInFlightPerConn uint64 `json:"json_rpc_ws_max_in_flight_per_connection" yaml:"json_rpc_ws_max_in_flight_per_connection"` //nolint:lll

	MetricsInterval time.Duration `json:"metrics_interval" yaml:"metrics_interval"`

	EnableTxPoolEndpoints   bool `json:"enable_tx_pool_endpoints" yaml:"enable_tx_pool_endpoints"`
	EnableAllDebugEndpoints bool `json:"enable_all_debug_endpoints" yaml:"enable_all_debug_endpoints"`

	// Deprecated: use enable_tx_pool_endpoints (inverted). Honored when true.
	//nolint:lll
	DisableTxPoolEndpointsDeprecated bool `json:"disable_tx_pool_endpoints,omitempty" yaml:"disable_tx_pool_endpoints,omitempty"`

	// JumpdestCacheSize is the number of distinct contract codes (keyed by
	// code hash) for which the EVM keeps a precomputed JUMPDEST bitmap in
	// memory. Setting this to 0 disables the cache. See
	// `state/runtime/evm/jumpdest_cache.go`
	JumpdestCacheSize uint64 `json:"jumpdest_cache_size" yaml:"jumpdest_cache_size"`

	// Used to enable caching of tries in the state.
	// This can improve performance but will increase memory usage.
	WithTrieCaching bool `json:"with_trie_caching" yaml:"with_trie_caching"`

	// Used to make base fee value constant through the blocks
	WithBaseFeeFixed bool `json:"with_base_fee_fixed" yaml:"with_base_fee_fixed"`
}

// Telemetry holds the config details for metric services.
type Telemetry struct {
	PrometheusAddr    string `json:"prometheus_addr" yaml:"prometheus_addr"`
	SettlementMetrics bool   `json:"settlement_metrics" yaml:"settlement_metrics"`
}

// Network defines the network configuration params
type Network struct {
	NoDiscover        bool   `json:"no_discover" yaml:"no_discover"`
	Libp2pAddr        string `json:"libp2p_addr" yaml:"libp2p_addr"`
	NatAddr           string `json:"nat_addr" yaml:"nat_addr"`
	DNSAddr           string `json:"dns_addr" yaml:"dns_addr"`
	MaxPeers          int64  `json:"max_peers,omitempty" yaml:"max_peers,omitempty"`
	MaxOutboundPeers  int64  `json:"max_outbound_peers,omitempty" yaml:"max_outbound_peers,omitempty"`
	MaxInboundPeers   int64  `json:"max_inbound_peers,omitempty" yaml:"max_inbound_peers,omitempty"`
	GossipMessageSize int    `json:"gossip_msg_size" yaml:"gossip_msg_size"`
}

// TxPool defines the TxPool configuration params
type TxPool struct {
	PriceLimit         uint64 `json:"price_limit" yaml:"price_limit"`
	MaxSlots           uint64 `json:"max_slots" yaml:"max_slots"`
	MaxAccountEnqueued uint64 `json:"max_account_enqueued" yaml:"max_account_enqueued"`
	MaxAccountPromoted uint64 `json:"max_account_promoted" yaml:"max_account_promoted"`
	TxGossipBatchSize  uint64 `json:"tx_gossip_batch_size" yaml:"tx_gossip_batch_size"`
	JournalRotateSize  uint64 `json:"journal_rotate_size" yaml:"journal_rotate_size"`
}

// Headers defines the HTTP response headers required to enable CORS.
type Headers struct {
	AccessControlAllowOrigins []string `json:"access_control_allow_origins" yaml:"access_control_allow_origins"`
}

const (
	// BlockTimeMultiplierForTimeout Multiplier to get IBFT timeout from block time
	// timeout is calculated when IBFT timeout is not specified
	BlockTimeMultiplierForTimeout uint64 = 5

	// DefaultJSONRPCBatchRequestLimit maximum length allowed for json_rpc batch requests
	DefaultJSONRPCBatchRequestLimit uint64 = 20

	// DefaultJSONRPCBlockRangeLimit maximum block range allowed for json_rpc
	// requests with fromBlock/toBlock values (e.g. eth_getLogs)
	DefaultJSONRPCBlockRangeLimit uint64 = 1000

	// DefaultNumBlockConfirmations minimal number of child blocks required for the parent block to be considered final
	// on ethereum epoch lasts for 32 blocks. more details: https://www.alchemy.com/overviews/ethereum-commitment-levels
	DefaultNumBlockConfirmations uint64 = 64

	// DefaultConcurrentRequestsDebug specifies max number of allowed concurrent requests for debug endpoints
	DefaultConcurrentRequestsDebug uint64 = 32

	// DefaultWebSocketReadLimit specifies max size in bytes for a message read from the peer by Gorrila websocket lib.
	// If a message exceeds the limit,
	// the connection sends a close message to the peer and returns ErrReadLimit to the application.
	DefaultWebSocketReadLimit uint64 = 8192

	// DefaultJSONRPCFilterLimit is the maximum number of filters and subscriptions the node
	// keeps active at once. Every log filter is matched against every log of every block, so
	// this bounds per-block work as well as memory.
	DefaultJSONRPCFilterLimit uint64 = 10000

	// DefaultJSONRPCFilterLimitPerConn is the maximum number of subscriptions a single web
	// socket connection may hold. Ordinary clients use a handful; the allowance is generous
	// enough for a backend that watches many contracts, while stopping one socket from
	// installing filters in a loop.
	DefaultJSONRPCFilterLimitPerConn uint64 = 100

	// DefaultJSONRPCWSMaxConnections is how many websocket connections the node
	// accepts at once. Each connection owns a writer goroutine and an outbound
	// queue, so this bounds baseline memory before any request is in flight.
	DefaultJSONRPCWSMaxConnections uint64 = 1024

	// DefaultJSONRPCWSMaxInFlight is how many websocket JSON-RPC handlers may
	// run at once across every connection. This is the ceiling that stops a
	// request flood, from one connection or many, from creating unbounded work.
	DefaultJSONRPCWSMaxInFlight uint64 = 256

	// DefaultJSONRPCWSMaxInFlightPerConn is how many handlers one connection
	// may run at once. Large enough for ordinary pipelining; small enough that
	// a single socket cannot consume the global ceiling by itself.
	DefaultJSONRPCWSMaxInFlightPerConn uint64 = 16

	// DefaultMetricsInterval specifies the time interval after which Prometheus metrics will be generated.
	// A value of 0 means the metrics are disabled.
	DefaultMetricsInterval time.Duration = time.Second * 8

	// JSON RPC max request body size
	DefaultRequestBodySize = 5 << 20 // 5 MiB

	// JSON RPC request timeout
	DefaultJSONRPCTimeout = 30 * time.Second

	// DefaultRPCGasCap is the maximum gas eth_call / eth_estimateGas / debug_traceCall may use.
	// Zero disables the cap.
	DefaultRPCGasCap uint64 = 50_000_000

	// DefaultJSONRPCBatchCostLimit is the maximum total method-cost weight of one batch.
	// Zero disables the cost budget.
	DefaultJSONRPCBatchCostLimit uint64 = 200

	// DefaultJSONRPCMaxResponseSize is the maximum serialized JSON-RPC response size in bytes.
	// Zero disables the cap.
	DefaultJSONRPCMaxResponseSize uint64 = 25 << 20
)

// DefaultConfig returns the default server configuration
func DefaultConfig() *Config {
	defaultNetworkConfig := network.DefaultConfig()

	return &Config{
		// Deployed nodes ship logs to an aggregator, which cannot parse hclog's text
		// format. Kept in step with the --json-logs flag default.
		JSONLogFormat:  true,
		GenesisPath:    "./genesis.json",
		DataDir:        "",
		BlockGasTarget: "0x0", // Special value signaling the parent gas limit should be applied
		Network: &Network{
			NoDiscover:       defaultNetworkConfig.NoDiscover,
			MaxPeers:         defaultNetworkConfig.MaxPeers,
			MaxOutboundPeers: defaultNetworkConfig.MaxOutboundPeers,
			MaxInboundPeers:  defaultNetworkConfig.MaxInboundPeers,
			Libp2pAddr: fmt.Sprintf("%s:%d",
				defaultNetworkConfig.Addr.IP,
				defaultNetworkConfig.Addr.Port,
			),
			GossipMessageSize: pubsub.DefaultMaxMessageSize,
		},
		Telemetry:  &Telemetry{},
		ShouldSeal: true,
		TxPool: &TxPool{
			PriceLimit:         0,
			MaxSlots:           4096,
			MaxAccountEnqueued: 128,
			MaxAccountPromoted: 128,
			TxGossipBatchSize:  1,
			JournalRotateSize:  1000,
		},
		LogLevel:    "INFO",
		RestoreFile: "",
		Headers: &Headers{
			AccessControlAllowOrigins: []string{"*"},
		},
		LogFilePath:                 "",
		JSONRPCBatchRequestLimit:    DefaultJSONRPCBatchRequestLimit,
		JSONRPCBlockRangeLimit:      DefaultJSONRPCBlockRangeLimit,
		Relayer:                     false,
		NumBlockConfirmations:       DefaultNumBlockConfirmations,
		ConcurrentRequestsDebug:     DefaultConcurrentRequestsDebug,
		WebSocketReadLimit:          DefaultWebSocketReadLimit,
		JSONRPCFilterLimit:          DefaultJSONRPCFilterLimit,
		JSONRPCFilterLimitPerConn:   DefaultJSONRPCFilterLimitPerConn,
		JSONRPCWSMaxConnections:     DefaultJSONRPCWSMaxConnections,
		JSONRPCWSMaxInFlight:        DefaultJSONRPCWSMaxInFlight,
		JSONRPCWSMaxInFlightPerConn: DefaultJSONRPCWSMaxInFlightPerConn,
		MetricsInterval:             DefaultMetricsInterval,
		UseTLS:                      false,
		TLSCertFile:                 "",
		TLSKeyFile:                  "",
		BlockCacheTTL:               3 * time.Minute,
		BlockCacheCapacity:          50,
		MaxRequestBodySize:          DefaultRequestBodySize,
		JSONRPCTimeout:              DefaultJSONRPCTimeout,
		RPCGasCap:                   DefaultRPCGasCap,
		JSONRPCBatchCostLimit:       DefaultJSONRPCBatchCostLimit,
		JSONRPCMaxResponseSize:      DefaultJSONRPCMaxResponseSize,
		JumpdestCacheSize:           evm.DefaultJumpdestCacheSize,
		EnableTxPoolEndpoints:       false,
		EnableAllDebugEndpoints:     false,
		WithTrieCaching:             true,
		WithBaseFeeFixed:            false,
	}
}

// ApplyDeprecatedEndpointFlags maps legacy disable_tx_pool_endpoints to enable_tx_pool_endpoints.
func (c *Config) ApplyDeprecatedEndpointFlags() {
	if c.DisableTxPoolEndpointsDeprecated {
		c.EnableTxPoolEndpoints = false
	}
}

// ReadConfigFile reads the config file from the specified path, builds a Config object
// and returns it.
//
// Supported file types: .json, .hcl, .yaml, .yml
func ReadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var unmarshalFunc func([]byte, interface{}) error

	switch {
	case strings.HasSuffix(path, ".hcl"):
		unmarshalFunc = hcl.Unmarshal
	case strings.HasSuffix(path, ".json"):
		unmarshalFunc = json.Unmarshal
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		unmarshalFunc = yaml.Unmarshal
	default:
		return nil, fmt.Errorf("suffix of %s is neither hcl, json, yaml nor yml", path)
	}

	config := DefaultConfig()
	if err := unmarshalFunc(data, config); err != nil {
		return nil, err
	}

	config.ApplyDeprecatedEndpointFlags()

	return config, nil
}
