package server

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/command/helper"
	"github.com/0xPolygon/polygon-edge/command/server/config"
	"github.com/0xPolygon/polygon-edge/command/server/export"
	"github.com/0xPolygon/polygon-edge/server"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/spf13/cobra"
)

func GetCommand() *cobra.Command {
	serverCmd := &cobra.Command{
		Use:     "server",
		Short:   "The default command that starts the Polygon Edge client, by bootstrapping all modules together",
		PreRunE: runPreRun,
		Run:     runCommand,
	}

	helper.RegisterGRPCAddressFlag(serverCmd)
	helper.RegisterLegacyGRPCAddressFlag(serverCmd)
	helper.RegisterJSONRPCFlag(serverCmd)

	registerSubcommands(serverCmd)
	setFlags(serverCmd)

	return serverCmd
}

func registerSubcommands(baseCmd *cobra.Command) {
	baseCmd.AddCommand(
		// server export
		export.GetCommand(),
	)
}

func setFlags(cmd *cobra.Command) {
	defaultConfig := config.DefaultConfig()

	// Deployed nodes ship logs to an aggregator, so JSON is the default here. This is
	// separate from --json, which selects the format of a command's output.
	helper.RegisterJSONLogsFlag(cmd)

	cmd.Flags().StringVar(
		&params.rawConfig.LogLevel,
		command.LogLevelFlag,
		defaultConfig.LogLevel,
		"the log level for console output",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.GenesisPath,
		genesisPathFlag,
		defaultConfig.GenesisPath,
		"the genesis file used for starting the chain",
	)

	cmd.Flags().StringVar(
		&params.configPath,
		configFlag,
		"",
		"the path to the CLI config. Supports .json, .hcl, .yaml, .yml",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.DataDir,
		dataDirFlag,
		defaultConfig.DataDir,
		"the data directory used for storing Polygon Edge client data",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.Network.Libp2pAddr,
		libp2pAddressFlag,
		defaultConfig.Network.Libp2pAddr,
		"the address and port for the libp2p service",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.Telemetry.PrometheusAddr,
		prometheusAddressFlag,
		"",
		"the address and port for the prometheus instrumentation service (address:port). "+
			"If only port is defined (:port) it will bind to 0.0.0.0:port",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.Network.NatAddr,
		natFlag,
		"",
		"the external IP address without port, as can be seen by peers",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.Network.DNSAddr,
		dnsFlag,
		"",
		"the host DNS address which can be used by a remote peer for connection",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.BlockGasTarget,
		blockGasTargetFlag,
		defaultConfig.BlockGasTarget,
		"the target block gas limit for the chain. If omitted, the value of the parent block is used",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.SecretsConfigPath,
		secretsConfigFlag,
		"",
		"the path to the SecretsManager config file. Used for Hashicorp Vault. "+
			"If omitted, the local FS secrets manager is used",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.SignerConfigPath,
		signerConfigFlag,
		"",
		"the path to the Signer config file",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.RestoreFile,
		restoreFlag,
		"",
		"the path to the archive blockchain data to restore on initialization",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.ShouldSeal,
		sealFlag,
		defaultConfig.ShouldSeal,
		"the flag indicating that the client should seal blocks",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.Network.NoDiscover,
		command.NoDiscoverFlag,
		defaultConfig.Network.NoDiscover,
		"prevent the client from discovering other peers",
	)

	cmd.Flags().Int64Var(
		&params.rawConfig.Network.MaxPeers,
		maxPeersFlag,
		-1,
		"the client's max number of peers allowed",
	)
	// override default usage value
	cmd.Flag(maxPeersFlag).DefValue = fmt.Sprintf("%d", defaultConfig.Network.MaxPeers)

	cmd.Flags().Int64Var(
		&params.rawConfig.Network.MaxInboundPeers,
		maxInboundPeersFlag,
		-1,
		"the client's max number of inbound peers allowed",
	)
	// override default usage value
	cmd.Flag(maxInboundPeersFlag).DefValue = fmt.Sprintf("%d", defaultConfig.Network.MaxInboundPeers)
	cmd.MarkFlagsMutuallyExclusive(maxPeersFlag, maxInboundPeersFlag)

	cmd.Flags().Int64Var(
		&params.rawConfig.Network.MaxOutboundPeers,
		maxOutboundPeersFlag,
		-1,
		"the client's max number of outbound peers allowed",
	)
	// override default usage value
	cmd.Flag(maxOutboundPeersFlag).DefValue = fmt.Sprintf("%d", defaultConfig.Network.MaxOutboundPeers)
	cmd.MarkFlagsMutuallyExclusive(maxPeersFlag, maxOutboundPeersFlag)

	cmd.Flags().IntVar(
		&params.rawConfig.Network.GossipMessageSize,
		gossipMessageSizeFlag,
		pubsub.DefaultMaxMessageSize,
		"the maximum size of a gossip message",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.TxPool.PriceLimit,
		priceLimitFlag,
		defaultConfig.TxPool.PriceLimit,
		fmt.Sprintf(
			"the minimum gas price limit to enforce for acceptance into the pool (default %d)",
			defaultConfig.TxPool.PriceLimit,
		),
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.TxPool.MaxSlots,
		maxSlotsFlag,
		defaultConfig.TxPool.MaxSlots,
		"maximum slots in the pool",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.TxPool.MaxAccountEnqueued,
		maxEnqueuedFlag,
		defaultConfig.TxPool.MaxAccountEnqueued,
		"maximum number of enqueued transactions per account",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.TxPool.MaxAccountPromoted,
		maxPromotedFlag,
		defaultConfig.TxPool.MaxAccountPromoted,
		"maximum number of promoted transactions per account",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.TxPool.TxGossipBatchSize,
		txGossipBatchSizeFlag,
		defaultConfig.TxPool.TxGossipBatchSize,
		"maximum number of transactions in gossip message",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.TxPool.JournalRotateSize,
		journalRotateSizeFlag,
		defaultConfig.TxPool.JournalRotateSize,
		"number of local transactions in journal when rotate will be executed",
	)

	cmd.Flags().StringArrayVar(
		&params.rawConfig.CorsAllowedOrigins,
		corsOriginFlag,
		defaultConfig.Headers.AccessControlAllowOrigins,
		"the CORS header indicating whether any JSON-RPC response can be shared with the specified origin",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCBatchRequestLimit,
		jsonRPCBatchRequestLimitFlag,
		defaultConfig.JSONRPCBatchRequestLimit,
		"max length to be considered when handling json-rpc batch requests, value of 0 disables it",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCBlockRangeLimit,
		jsonRPCBlockRangeLimitFlag,
		defaultConfig.JSONRPCBlockRangeLimit,
		"max block range to be considered when executing json-rpc requests "+
			"that consider fromBlock/toBlock values (e.g. eth_getLogs), value of 0 disables it",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.LogFilePath,
		logFileLocationFlag,
		defaultConfig.LogFilePath,
		"write all logs to the file at specified location instead of writing them to console",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.Relayer,
		relayerFlag,
		defaultConfig.Relayer,
		"start the state sync relayer service (PolyBFT only)",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.NumBlockConfirmations,
		numBlockConfirmationsFlag,
		defaultConfig.NumBlockConfirmations,
		"minimal number of child blocks required for the parent block to be considered final",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.ConcurrentRequestsDebug,
		concurrentRequestsDebugFlag,
		defaultConfig.ConcurrentRequestsDebug,
		"maximal number of concurrent requests for debug endpoints",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.WebSocketReadLimit,
		webSocketReadLimitFlag,
		defaultConfig.WebSocketReadLimit,
		"maximum size in bytes for a message read from the peer by websocket",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCFilterLimit,
		jsonRPCFilterLimitFlag,
		defaultConfig.JSONRPCFilterLimit,
		"maximum number of active filters and subscriptions on the node, zero means no limit",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCFilterLimitPerConn,
		jsonRPCFilterLimitPerConnFlag,
		defaultConfig.JSONRPCFilterLimitPerConn,
		"maximum number of active subscriptions per websocket connection, zero means no limit",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCWSMaxConnections,
		jsonRPCWSMaxConnectionsFlag,
		defaultConfig.JSONRPCWSMaxConnections,
		"maximum number of active websocket connections, zero means no limit",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCWSMaxInFlight,
		jsonRPCWSMaxInFlightFlag,
		defaultConfig.JSONRPCWSMaxInFlight,
		"maximum number of in-flight websocket JSON-RPC requests across all connections, zero means no limit",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCWSMaxInFlightPerConn,
		jsonRPCWSMaxInFlightPerConnFlag,
		defaultConfig.JSONRPCWSMaxInFlightPerConn,
		"maximum number of in-flight websocket JSON-RPC requests per connection, zero means no limit",
	)

	cmd.Flags().DurationVar(
		&params.rawConfig.MetricsInterval,
		metricsIntervalFlag,
		defaultConfig.MetricsInterval,
		"the interval (in seconds) at which special metrics are generated. a value of zero means the metrics are disabled",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.UseTLS,
		useTLSFlag,
		defaultConfig.UseTLS,
		"start json rpc endpoint with tls enabled",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.TLSCertFile,
		tlsCertFileLocationFlag,
		defaultConfig.TLSCertFile,
		"path to TLS cert file, if no file is provided then cert file is loaded from secrets manager",
	)

	cmd.Flags().StringVar(
		&params.rawConfig.TLSKeyFile,
		tlsKeyFileLocationFlag,
		defaultConfig.TLSKeyFile,
		"path to TLS key file, if no file is provided then key file is loaded from secrets manager",
	)

	cmd.Flags().DurationVar(
		&params.rawConfig.BlockCacheTTL,
		blockCacheTTLFlag,
		defaultConfig.BlockCacheTTL,
		"time for block cache item to be kept in the cache since last touch",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.BlockCacheCapacity,
		blockCacheCapacityFlag,
		defaultConfig.BlockCacheCapacity,
		"maximum number of block cache items to be kept in the cache",
	)

	cmd.Flags().Int64Var(
		&params.rawConfig.MaxRequestBodySize,
		MaxRequestBodySizeFlag,
		defaultConfig.MaxRequestBodySize,
		"the maximum size of the JSON-RPC HTTP request body in bytes (default 5MB)",
	)

	cmd.Flags().DurationVar(
		&params.rawConfig.JSONRPCTimeout,
		JSONRPCTimeoutFlag,
		defaultConfig.JSONRPCTimeout,
		"the timeout for JSON-RPC HTTP request processing (e.g. 30s, 1m, 1m30s)",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.RPCGasCap,
		rpcGasCapFlag,
		defaultConfig.RPCGasCap,
		"maximum gas eth_call/eth_estimateGas/debug_traceCall may use, 0 disables the cap",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCBatchCostLimit,
		jsonRPCBatchCostLimitFlag,
		defaultConfig.JSONRPCBatchCostLimit,
		"max total method-cost weight of a JSON-RPC batch request, value of 0 disables it",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JSONRPCMaxResponseSize,
		jsonRPCMaxResponseSizeFlag,
		defaultConfig.JSONRPCMaxResponseSize,
		"maximum JSON-RPC response size in bytes, value of 0 disables it",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.EnableTxPoolEndpoints,
		enableTxPoolEndpointsFlag,
		defaultConfig.EnableTxPoolEndpoints,
		"enable all txpool JSON-RPC endpoints",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.EnableAllDebugEndpoints,
		enableAllDebugEndpointsFlag,
		defaultConfig.EnableAllDebugEndpoints,
		"enable all debug JSON-RPC endpoints",
	)

	cmd.Flags().Uint64Var(
		&params.rawConfig.JumpdestCacheSize,
		jumpdestCacheSizeFlag,
		defaultConfig.JumpdestCacheSize,
		"number of contract codes (keyed by code hash) for which the EVM keeps a "+
			"precomputed JUMPDEST bitmap in memory; set to 0 to disable",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.WithTrieCaching,
		withTrieCachingFlag,
		defaultConfig.WithTrieCaching,
		"enable caching of tries in the state; this can improve performance but will increase memory usage",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.WithBaseFeeFixed,
		withBaseFeeFixedFlag,
		defaultConfig.WithBaseFeeFixed,
		"keep base fee constant through the blocks",
	)

	cmd.Flags().BoolVar(
		&params.rawConfig.Telemetry.SettlementMetrics,
		settlementMetricsFlag,
		false,
		"enable settlement metrics",
	)

	setLegacyFlags(cmd)

	setDevFlags(cmd)
}

// setLegacyFlags sets the legacy flags to preserve backwards compatibility
// with running partners
func setLegacyFlags(cmd *cobra.Command) {
	// Legacy IBFT base timeout flag
	cmd.Flags().Uint64Var(
		&params.ibftBaseTimeoutLegacy,
		ibftBaseTimeoutFlagLEGACY,
		0,
		"",
	)

	_ = cmd.Flags().MarkHidden(ibftBaseTimeoutFlagLEGACY)

	// Legacy txpool endpoints flag (inverted semantics; use enable-tx-pool-endpoints).
	cmd.Flags().BoolVar(
		&params.disableTxPoolEndpointsLegacy,
		disableTxPoolEndpointsFlagLEGACY,
		false,
		"",
	)

	_ = cmd.Flags().MarkHidden(disableTxPoolEndpointsFlagLEGACY)
}

func setDevFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(
		&params.isDevMode,
		devFlag,
		false,
		"should the client start in dev mode (default false)",
	)

	_ = cmd.Flags().MarkHidden(devFlag)

	cmd.Flags().Uint64Var(
		&params.devInterval,
		devIntervalFlag,
		0,
		"the client's dev notification interval in seconds (default 1)",
	)

	_ = cmd.Flags().MarkHidden(devIntervalFlag)
}

func (p *serverParams) applyLegacyTxPoolEndpointsFlag(cmd *cobra.Command) {
	if !cmd.Flags().Changed(disableTxPoolEndpointsFlagLEGACY) ||
		cmd.Flags().Changed(enableTxPoolEndpointsFlag) {
		return
	}

	p.rawConfig.EnableTxPoolEndpoints = !p.disableTxPoolEndpointsLegacy
}

func runPreRun(cmd *cobra.Command, _ []string) error {
	// Set the grpc and json ip:port bindings
	// The config file will have precedence over --flag
	params.setRawGRPCAddress(helper.GetGRPCAddress(cmd))
	params.setRawJSONRPCAddress(helper.GetJSONRPCAddress(cmd))
	params.setJSONLogFormat(helper.GetJSONLogFormat(cmd))

	// Check if the config file has been specified
	// Config file settings will override JSON-RPC and GRPC address values
	if isConfigFileSpecified(cmd) {
		if err := params.initConfigFromFile(); err != nil {
			return err
		}

		// initConfigFromFile replaces rawConfig wholesale, discarding the flag values
		// set above. An explicitly-passed --json-logs is re-applied here so it is not
		// silently ignored whenever --config is used.
		if cmd.Flags().Changed(command.JSONLogsFlag) {
			params.setJSONLogFormat(helper.GetJSONLogFormat(cmd))
		}
	}

	params.applyLegacyTxPoolEndpointsFlag(cmd)

	if err := params.initRawParams(); err != nil {
		return err
	}

	if err := params.validateFlags(); err != nil {
		return err
	}

	return nil
}

func isConfigFileSpecified(cmd *cobra.Command) bool {
	return cmd.Flags().Changed(configFlag)
}

func runCommand(cmd *cobra.Command, _ []string) {
	outputter := command.InitializeOutputter(cmd)

	if err := runServerLoop(params.generateConfig(), outputter); err != nil {
		outputter.SetError(err)
		outputter.WriteOutput()

		return
	}
}

func runServerLoop(
	config *server.Config,
	outputter command.OutputFormatter,
) error {
	serverInstance, err := server.NewServer(config)
	if err != nil {
		return err
	}

	return helper.HandleSignals(serverInstance.Close, outputter)
}
