package server

import (
	"errors"
	"fmt"
	"net"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/command/server/config"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/network"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/server"
	"github.com/hashicorp/go-hclog"
	"github.com/multiformats/go-multiaddr"
)

const (
	configFlag                   = "config"
	genesisPathFlag              = "chain"
	dataDirFlag                  = "data-dir"
	libp2pAddressFlag            = "libp2p"
	prometheusAddressFlag        = "prometheus"
	natFlag                      = "nat"
	dnsFlag                      = "dns"
	sealFlag                     = "seal"
	maxPeersFlag                 = "max-peers"
	maxInboundPeersFlag          = "max-inbound-peers"
	maxOutboundPeersFlag         = "max-outbound-peers"
	priceLimitFlag               = "price-limit"
	jsonRPCBatchRequestLimitFlag = "json-rpc-batch-request-limit"
	jsonRPCBlockRangeLimitFlag   = "json-rpc-block-range-limit"
	maxSlotsFlag                 = "max-slots"
	maxEnqueuedFlag              = "max-enqueued"
	maxPromotedFlag              = "max-promoted"
	blockGasTargetFlag           = "block-gas-target"
	secretsConfigFlag            = "secrets-config"
	signerConfigFlag             = "signer-config"
	restoreFlag                  = "restore"
	devIntervalFlag              = "dev-interval"
	devFlag                      = "dev"
	corsOriginFlag               = "access-control-allow-origins"
	logFileLocationFlag          = "log-to"
	gossipMessageSizeFlag        = "gossip-msg-size"
	txGossipBatchSizeFlag        = "tx-gossip-batch-size"
	journalRotateSizeFlag        = "journal-rotate-size"

	useTLSFlag                 = "use-tls"
	tlsCertFileLocationFlag    = "tls-cert-file"
	tlsKeyFileLocationFlag     = "tls-key-file"
	blockCacheTTLFlag          = "block-cache-ttl"
	blockCacheCapacityFlag     = "block-cache-capacity"
	MaxRequestBodySizeFlag     = "max-request-body-size"
	JSONRPCTimeoutFlag         = "json-rpc-timeout"
	rpcGasCapFlag              = "rpc-gas-cap"
	jsonRPCBatchCostLimitFlag  = "json-rpc-batch-cost-limit"
	jsonRPCMaxResponseSizeFlag = "json-rpc-max-response-size"

	relayerFlag               = "relayer"
	numBlockConfirmationsFlag = "num-block-confirmations"

	concurrentRequestsDebugFlag     = "concurrent-requests-debug"
	webSocketReadLimitFlag          = "websocket-read-limit"
	jsonRPCFilterLimitFlag          = "json-rpc-filter-limit"
	jsonRPCFilterLimitPerConnFlag   = "json-rpc-filter-limit-per-connection"
	jsonRPCWSMaxConnectionsFlag     = "json-rpc-ws-max-connections"
	jsonRPCWSMaxInFlightFlag        = "json-rpc-ws-max-in-flight"
	jsonRPCWSMaxInFlightPerConnFlag = "json-rpc-ws-max-in-flight-per-connection"

	metricsIntervalFlag = "metrics-interval"

	enableTxPoolEndpointsFlag   = "enable-tx-pool-endpoints"
	enableAllDebugEndpointsFlag = "enable-all-debug-endpoints"

	// Deprecated: use enable-tx-pool-endpoints (inverted semantics).
	disableTxPoolEndpointsFlagLEGACY = "disable-tx-pool-endpoints"

	jumpdestCacheSizeFlag = "jumpdest-cache-size"

	settlementMetricsFlag = "settlement-metrics"
	withTrieCachingFlag   = "with-trie-caching"
	withBaseFeeFixedFlag  = "with-base-fee-fixed"
)

// Flags that are deprecated, but need to be preserved for
// backwards compatibility with existing scripts
const (
	ibftBaseTimeoutFlagLEGACY = "ibft-base-timeout"
)

const (
	unsetPeersValue = -1
)

var (
	params = &serverParams{
		rawConfig: &config.Config{
			Telemetry: &config.Telemetry{},
			Network:   &config.Network{},
			TxPool:    &config.TxPool{},
		},
	}
)

var (
	errInvalidNATAddress = errors.New("could not parse NAT IP address")
)

type serverParams struct {
	rawConfig  *config.Config
	configPath string

	libp2pAddress     *net.TCPAddr
	prometheusAddress *net.TCPAddr
	natAddress        net.IP
	dnsAddress        multiaddr.Multiaddr
	grpcAddress       *net.TCPAddr
	jsonRPCAddress    *net.TCPAddr

	blockGasTarget uint64
	devInterval    uint64
	isDevMode      bool

	ibftBaseTimeoutLegacy        uint64
	disableTxPoolEndpointsLegacy bool

	genesisConfig *chain.Chain
	secretsConfig *secrets.SecretsManagerConfig
	signerConfig  *signer.SignerConfig

	logFileLocation string

	relayer bool
}

func (p *serverParams) isMaxPeersSet() bool {
	return p.rawConfig.Network.MaxPeers != unsetPeersValue
}

func (p *serverParams) isPeerRangeSet() bool {
	return p.rawConfig.Network.MaxInboundPeers != unsetPeersValue ||
		p.rawConfig.Network.MaxOutboundPeers != unsetPeersValue
}

func (p *serverParams) isSecretsConfigPathSet() bool {
	return p.rawConfig.SecretsConfigPath != ""
}

func (p *serverParams) isSignerConfigPathSet() bool {
	return p.rawConfig.SignerConfigPath != ""
}

func (p *serverParams) isPrometheusAddressSet() bool {
	return p.rawConfig.Telemetry.PrometheusAddr != ""
}

func (p *serverParams) isNATAddressSet() bool {
	return p.rawConfig.Network.NatAddr != ""
}

func (p *serverParams) isDNSAddressSet() bool {
	return p.rawConfig.Network.DNSAddr != ""
}

func (p *serverParams) isLogFileLocationSet() bool {
	return p.rawConfig.LogFilePath != ""
}

func (p *serverParams) isDevConsensus() bool {
	return server.ConsensusType(p.genesisConfig.Params.GetEngine()) == server.DevConsensus
}

func (p *serverParams) getRestoreFilePath() *string {
	if p.rawConfig.RestoreFile != "" {
		return &p.rawConfig.RestoreFile
	}

	return nil
}

func (p *serverParams) setRawGRPCAddress(grpcAddress string) {
	p.rawConfig.GRPCAddr = grpcAddress
}

func (p *serverParams) setRawJSONRPCAddress(jsonRPCAddress string) {
	p.rawConfig.JSONRPCAddr = jsonRPCAddress
}

func (p *serverParams) setJSONLogFormat(jsonLogFormat bool) {
	p.rawConfig.JSONLogFormat = jsonLogFormat
}

func (p *serverParams) validateFlags() error {
	if p.rawConfig.MaxRequestBodySize <= 0 {
		return fmt.Errorf("max-request-body-size must be greater than zero")
	}

	return nil
}

func (p *serverParams) generateConfig() *server.Config {
	return &server.Config{
		Chain: p.genesisConfig,
		JSONRPC: &server.JSONRPC{
			JSONRPCAddr:              p.jsonRPCAddress,
			AccessControlAllowOrigin: p.rawConfig.CorsAllowedOrigins,
			BatchLengthLimit:         p.rawConfig.JSONRPCBatchRequestLimit,
			BlockRangeLimit:          p.rawConfig.JSONRPCBlockRangeLimit,
			RPCGasCap:                p.rawConfig.RPCGasCap,
			BatchCostLimit:           p.rawConfig.JSONRPCBatchCostLimit,
			MaxResponseSize:          p.rawConfig.JSONRPCMaxResponseSize,
			ConcurrentRequestsDebug:  p.rawConfig.ConcurrentRequestsDebug,
			WebSocketReadLimit:       p.rawConfig.WebSocketReadLimit,
			FilterLimit:              p.rawConfig.JSONRPCFilterLimit,
			FilterLimitPerConn:       p.rawConfig.JSONRPCFilterLimitPerConn,
			WSMaxConnections:         p.rawConfig.JSONRPCWSMaxConnections,
			WSMaxInFlight:            p.rawConfig.JSONRPCWSMaxInFlight,
			WSMaxInFlightPerConn:     p.rawConfig.JSONRPCWSMaxInFlightPerConn,
		},
		GRPCAddr:   p.grpcAddress,
		LibP2PAddr: p.libp2pAddress,
		Telemetry: &server.Telemetry{
			PrometheusAddr:    p.prometheusAddress,
			SettlementMetrics: p.rawConfig.Telemetry.SettlementMetrics,
		},
		Network: &network.Config{
			NoDiscover:        p.rawConfig.Network.NoDiscover,
			Addr:              p.libp2pAddress,
			NatAddr:           p.natAddress,
			DNS:               p.dnsAddress,
			DataDir:           p.rawConfig.DataDir,
			MaxPeers:          p.rawConfig.Network.MaxPeers,
			MaxInboundPeers:   p.rawConfig.Network.MaxInboundPeers,
			MaxOutboundPeers:  p.rawConfig.Network.MaxOutboundPeers,
			Chain:             p.genesisConfig,
			GossipMessageSize: p.rawConfig.Network.GossipMessageSize,
		},
		DataDir:            p.rawConfig.DataDir,
		Seal:               p.rawConfig.ShouldSeal,
		PriceLimit:         p.rawConfig.TxPool.PriceLimit,
		MaxSlots:           p.rawConfig.TxPool.MaxSlots,
		MaxAccountEnqueued: p.rawConfig.TxPool.MaxAccountEnqueued,
		MaxAccountPromoted: p.rawConfig.TxPool.MaxAccountPromoted,
		TxGossipBatchSize:  p.rawConfig.TxPool.TxGossipBatchSize,
		JournalRotateSize:  p.rawConfig.TxPool.JournalRotateSize,
		SecretsManager:     p.secretsConfig,
		SignerConfig:       p.signerConfig,
		RestoreFile:        p.getRestoreFilePath(),
		LogLevel:           hclog.LevelFromString(p.rawConfig.LogLevel),
		JSONLogFormat:      p.rawConfig.JSONLogFormat,
		LogFilePath:        p.logFileLocation,

		Relayer:                 p.relayer,
		NumBlockConfirmations:   p.rawConfig.NumBlockConfirmations,
		MetricsInterval:         p.rawConfig.MetricsInterval,
		UseTLS:                  p.rawConfig.UseTLS,
		TLSCertFile:             p.rawConfig.TLSCertFile,
		TLSKeyFile:              p.rawConfig.TLSKeyFile,
		BlockCacheTTL:           p.rawConfig.BlockCacheTTL,
		BlockCacheCapacity:      p.rawConfig.BlockCacheCapacity,
		MaxRequestBodySize:      p.rawConfig.MaxRequestBodySize,
		JSONRPCTimeout:          p.rawConfig.JSONRPCTimeout,
		EnableTxPoolEndpoints:   p.rawConfig.EnableTxPoolEndpoints,
		EnableAllDebugEndpoints: p.rawConfig.EnableAllDebugEndpoints,
		WithTrieCaching:         p.rawConfig.WithTrieCaching,
		WithBaseFeeFixed:        p.rawConfig.WithBaseFeeFixed,

		JumpdestCacheSize: p.rawConfig.JumpdestCacheSize,
	}
}
