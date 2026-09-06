package jsonrpc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/versioning"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/go-hclog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// JSONRPC is an API consensus
type JSONRPC struct {
	logger     hclog.Logger
	config     *Config
	dispatcher dispatcher
	wsLimiter  *wsLimiter
}

type dispatcher interface {
	RemoveFilterByWs(conn wsConn)
	RefreshFilterTimeouts(conn wsConn)
	HandleWs(reqBody []byte, conn wsConn) ([]byte, error)
	Handle(ctx context.Context, reqBody []byte) ([]byte, error)
}

// JSONRPCStore defines all the methods required
// by all the JSON RPC endpoints
type JSONRPCStore interface {
	ethStore
	networkStore
	txPoolStore
	filterManagerStore
	bridgeStore
	debugStore
}

type Config struct {
	Store                    JSONRPCStore
	Addr                     *net.TCPAddr
	ChainID                  uint64
	ChainName                string
	AccessControlAllowOrigin []string
	PriceLimit               uint64
	BatchLengthLimit         uint64
	BlockRangeLimit          uint64
	MaxRequestBodySize       int64
	JSONRPCTimeout           time.Duration

	ConcurrentRequestsDebug uint64
	WebSocketReadLimit      uint64
	FilterLimit             uint64
	FilterLimitPerConn      uint64
	WSMaxConnections        uint64
	WSMaxInFlight           uint64
	WSMaxInFlightPerConn    uint64
	UseTLS                  bool
	TLSCertFile             string
	TLSKeyFile              string
	SecretsManager          secrets.SecretsManager

	BlockCacheTTL      time.Duration
	BlockCacheCapacity uint64

	EnableTxPoolEndpoints   bool
	EnableAllDebugEndpoints bool

	RPCGasCap       uint64
	BatchCostLimit  uint64
	MaxResponseSize uint64
}

// NewJSONRPC returns the JSONRPC http server
func NewJSONRPC(logger hclog.Logger, config *Config) (*JSONRPC, error) {
	d, err := newDispatcher(
		logger,
		config.Store,
		&dispatcherParams{
			chainID:                 config.ChainID,
			chainName:               config.ChainName,
			priceLimit:              config.PriceLimit,
			jsonRPCBatchLengthLimit: config.BatchLengthLimit,
			blockRangeLimit:         config.BlockRangeLimit,
			filterLimits: FilterLimits{
				PerConnection: config.FilterLimitPerConn,
				Global:        config.FilterLimit,
			},
			concurrentRequestsDebug: config.ConcurrentRequestsDebug,
			blockCacheTTL:           config.BlockCacheTTL,
			blockCacheCapacity:      config.BlockCacheCapacity,
			enableTxPoolEndpoints:   config.EnableTxPoolEndpoints,
			enableAllDebugEndpoints: config.EnableAllDebugEndpoints,
			requestTimeout:          config.JSONRPCTimeout,
			rpcGasCap:               config.RPCGasCap,
			batchCostLimit:          config.BatchCostLimit,
			maxResponseSize:         config.MaxResponseSize,
		},
	)
	if err != nil {
		return nil, err
	}

	srv := &JSONRPC{
		logger:     logger.Named("jsonrpc"),
		config:     config,
		dispatcher: d,
		wsLimiter: newWSLimiter(
			config.WSMaxConnections,
			config.WSMaxInFlight,
			config.WSMaxInFlightPerConn,
		),
	}

	// start http server
	if err := srv.setupHTTP(); err != nil {
		return nil, err
	}

	return srv, nil
}

// writeTimeout is a grace period on top of the execution deadline so the
// timeout error response can still be written after a handler is cancelled.
func writeTimeout(timeout time.Duration) time.Duration {
	if timeout == 0 {
		return 0
	}

	return timeout + 5*time.Second
}

func (j *JSONRPC) setupHTTP() error {
	lis, err := net.Listen("tcp", j.config.Addr.String())
	if err != nil {
		return err
	}

	// NewServeMux must be used, as it disables all debug features.
	// For some strange reason, with DefaultServeMux debug/vars is always enabled (but not debug/pprof).
	// If pprof need to be enabled, this should be DefaultServeMux
	mux := http.NewServeMux()

	// The middleware factory returns a handler, so we need to wrap the handler function properly.
	jsonRPCHandler := http.HandlerFunc(j.handle)
	mux.Handle("/", middlewareFactory(j.config)(jsonRPCHandler))

	mux.HandleFunc("/ws", j.handleWs)

	srv := http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 60 * time.Second,
		ReadTimeout:       j.config.JSONRPCTimeout,
		WriteTimeout:      writeTimeout(j.config.JSONRPCTimeout),
	}

	if j.config.UseTLS {
		j.logger.Info("configuring http server with tls...")

		if j.config.TLSCertFile != "" && j.config.TLSKeyFile != "" {
			j.logger.Info("TLS", "cert file", j.config.TLSCertFile)
			j.logger.Info("TLS", "key file", j.config.TLSKeyFile)

			go func() {
				if err := srv.ServeTLS(lis, j.config.TLSCertFile, j.config.TLSKeyFile); err != nil {
					j.logger.Error("closed https connection", "err", err)
				}
			}()
		} else {
			j.logger.Info("loading tls certificate from secrets manager...")

			cert, err := loadTLSCertificate(j.config.SecretsManager)
			if err != nil {
				j.logger.Error("loading tls certificate", "err", err)

				return err
			}

			srv.TLSConfig = &tls.Config{
				Certificates: []tls.Certificate{*cert},
				MinVersion:   tls.VersionTLS12,
			}

			go func() {
				if err := srv.ServeTLS(lis, "", ""); err != nil {
					j.logger.Error("closed https connection", "err", err)
				}
			}()
		}
	} else {
		go func() {
			if err := srv.Serve(lis); err != nil {
				j.logger.Error("closed http connection", "err", err)
			}
		}()
	}

	j.logger.Info("http server started", "addr", j.config.Addr.String())

	return nil
}

// The middlewareFactory builds a middleware which enables CORS using the provided config.
func middlewareFactory(config *Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			for _, allowedOrigin := range config.AccessControlAllowOrigin {
				if allowedOrigin == "*" {
					w.Header().Set("Access-Control-Allow-Origin", "*")

					break
				}

				if allowedOrigin == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)

					break
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// wsUpgrader defines upgrade parameters for the WS connection
var wsUpgrader = websocket.Upgrader{
	// Uses the default HTTP buffer sizes for Read / Write buffers.
	// Documentation specifies that they are 4096B in size.
	// There is no need to have them be 4x in size when requests / responses
	// shouldn't exceed 1024B
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,

	// CORS rule - Allow requests from anywhere
	CheckOrigin: func(r *http.Request) bool { return true },
}

const (
	// wsWriteDeadline bounds a single frame write to the peer. Without a deadline a peer
	// that stops reading its socket blocks the writer forever once the TCP window fills.
	wsWriteDeadline = 10 * time.Second

	// wsOutboundQueueSize is how many frames are buffered per connection before the peer is
	// considered too slow to keep up and gets dropped. It needs to absorb the burst a single
	// block can produce, which for a log subscription is one frame per matching log, while
	// staying small enough that many stalled connections cannot amplify into a lot of
	// retained memory.
	wsOutboundQueueSize = 256

	// wsPongWait is how long the connection may go without any inbound traffic, counting the
	// pong replies to our pings, before it is treated as dead. Without this a peer that
	// vanishes without closing its socket (power loss, NAT timeout) leaves the read loop
	// blocked forever and its filters installed until the OS gives up on the TCP connection.
	wsPongWait = 60 * time.Second

	// wsPingPeriod is how often the peer is pinged. It has to stay comfortably below
	// wsPongWait so that a single lost pong does not drop a healthy connection.
	wsPingPeriod = 20 * time.Second
)

// errWSWriteQueueFull signals that the peer is not draining its socket fast enough and
// should be evicted rather than buffered for.
var errWSWriteQueueFull = errors.New("web socket outbound queue is full")

// wsConnection is the subset of *websocket.Conn used by wsWrapper. It exists so the write
// path can be exercised without a real socket.
type wsConnection interface {
	WriteMessage(messageType int, data []byte) error
	WriteControl(messageType int, data []byte, deadline time.Time) error
	SetWriteDeadline(t time.Time) error
	Close() error
}

type wsMessage struct {
	messageType int
	data        []byte
}

// wsWrapper is a wrapping object for the web socket connection and logger
type wsWrapper struct {
	ws     wsConnection // the actual WS connection
	logger hclog.Logger // module logger

	// filterIDs holds every filter created over this connection. It has to be a set rather
	// than a single ID: a client can open any number of subscriptions on one connection, and
	// all of them have to be removed when it goes away.
	filterMux sync.Mutex
	filterIDs map[string]struct{}

	outbound   chan wsMessage // bounded queue drained by the writer goroutine
	closeCh    chan struct{}  // closed when the connection is being torn down
	closeOnce  sync.Once
	pingPeriod time.Duration
}

// newWSWrapper wraps the connection and starts the goroutine that owns all writes to it
func newWSWrapper(ws wsConnection, logger hclog.Logger, pingPeriod time.Duration) *wsWrapper {
	w := &wsWrapper{
		ws:         ws,
		logger:     logger,
		filterIDs:  make(map[string]struct{}),
		outbound:   make(chan wsMessage, wsOutboundQueueSize),
		closeCh:    make(chan struct{}),
		pingPeriod: pingPeriod,
	}

	go w.writeLoop()

	return w
}

// AddFilterID records a filter created over this connection
func (w *wsWrapper) AddFilterID(id string) {
	w.filterMux.Lock()
	defer w.filterMux.Unlock()

	w.filterIDs[id] = struct{}{}
}

// RemoveFilterID forgets a filter that has already been uninstalled, so that a client which
// subscribes and unsubscribes in a loop does not grow this set without bound
func (w *wsWrapper) RemoveFilterID(id string) {
	w.filterMux.Lock()
	defer w.filterMux.Unlock()

	delete(w.filterIDs, id)
}

// GetFilterIDs returns a snapshot of the filters created over this connection, so the caller
// can iterate it while filters are being removed
func (w *wsWrapper) GetFilterIDs() []string {
	w.filterMux.Lock()
	defer w.filterMux.Unlock()

	ids := make([]string, 0, len(w.filterIDs))

	for id := range w.filterIDs {
		ids = append(ids, id)
	}

	return ids
}

// WriteMessage queues the message for delivery to the WS peer. It never blocks: if the
// peer is not keeping up, the queue fills and the message is rejected with
// errWSWriteQueueFull so the caller can evict the connection.
func (w *wsWrapper) WriteMessage(messageType int, data []byte) error {
	select {
	case <-w.closeCh:
		return net.ErrClosed
	default:
	}

	select {
	case w.outbound <- wsMessage{messageType: messageType, data: data}:
		return nil
	default:
		return errWSWriteQueueFull
	}
}

// Close tears down the connection and stops the writer goroutine. Closing the underlying
// connection also unblocks the read loop in handleWs, which removes the connection's filters.
func (w *wsWrapper) Close() error {
	var closeErr error

	w.closeOnce.Do(func() {
		close(w.closeCh)

		closeErr = w.ws.Close()
	})

	return closeErr
}

// writeLoop is the only goroutine that writes to the underlying connection, so the writes
// need no further serialization. It also drives the keepalive pings, whose pongs are what
// keep the peer's filters from expiring.
func (w *wsWrapper) writeLoop() {
	pingTicker := time.NewTicker(w.pingPeriod)
	defer pingTicker.Stop()

	for {
		var err error

		select {
		case <-w.closeCh:
			return
		case msg := <-w.outbound:
			err = w.writeToConn(msg)
		case <-pingTicker.C:
			err = w.ws.WriteControl(
				websocket.PingMessage,
				nil,
				time.Now().UTC().Add(wsWriteDeadline),
			)
		}

		if err != nil {
			w.logger.Error(
				fmt.Sprintf("Unable to write to WS peer, closing connection, %s", err.Error()),
			)

			_ = w.Close()

			return
		}
	}
}

func (w *wsWrapper) writeToConn(msg wsMessage) error {
	if err := w.ws.SetWriteDeadline(time.Now().UTC().Add(wsWriteDeadline)); err != nil {
		return err
	}

	return w.ws.WriteMessage(msg.messageType, msg.data)
}

// isSupportedWSType returns a status indicating if the message type is supported
func isSupportedWSType(messageType int) bool {
	return messageType == websocket.TextMessage ||
		messageType == websocket.BinaryMessage
}

// replyWSLimitExceeded writes a JSON-RPC error from the read loop, so a flood of
// frames past the in-flight ceiling does not spawn a goroutine each.
func (j *JSONRPC) replyWSLimitExceeded(conn *wsWrapper, msgType int, message []byte) {
	var req Request

	_ = jsonIt.Unmarshal(message, &req)

	resp, err := NewRPCResponse(
		req.ID,
		"2.0",
		nil,
		NewLimitExceededError("websocket in-flight request limit reached"),
	).Bytes()
	if err != nil {
		return
	}

	if writeErr := conn.WriteMessage(msgType, resp); errors.Is(writeErr, errWSWriteQueueFull) {
		j.logger.Warn("Closing WS connection, peer is not draining its socket")

		_ = conn.Close()
	}
}

func (j *JSONRPC) handleWs(w http.ResponseWriter, req *http.Request) {
	if !j.wsLimiter.tryAddConn() {
		j.logger.Warn("websocket connection limit reached")
		http.Error(w, "websocket connection limit reached", http.StatusServiceUnavailable)

		return
	}

	defer j.wsLimiter.removeConn()

	// Upgrade the connection to a WS one
	ws, err := wsUpgrader.Upgrade(w, req, nil)
	if err != nil {
		j.logger.Error(fmt.Sprintf("Unable to upgrade to a WS connection, %s", err.Error()))

		return
	}

	// Set a read limit (maximum message size) for this connection
	if j.config.WebSocketReadLimit != 0 {
		ws.SetReadLimit(int64(j.config.WebSocketReadLimit))
	}

	wrapConn := newWSWrapper(ws, j.logger, wsPingPeriod)

	// Defer WS closure, which also stops the writer goroutine
	defer func() {
		if closeErr := wrapConn.Close(); closeErr != nil {
			j.logger.Error(
				fmt.Sprintf("Unable to gracefully close WS connection, %s", closeErr.Error()),
			)
		}
	}()

	extendReadDeadline := func() error {
		return ws.SetReadDeadline(time.Now().UTC().Add(wsPongWait))
	}

	if err := extendReadDeadline(); err != nil {
		j.logger.Error(fmt.Sprintf("Unable to set WS read deadline, %s", err.Error()))

		return
	}

	// A pong is the peer proving it is still there, so it both keeps the connection open and
	// pushes back the expiry of the filters the connection owns. Tying the refresh to pongs
	// rather than to every inbound frame keeps it naturally rate limited: a client cannot
	// turn cheap messages into FilterManager write lock traffic proportional to its filters.
	ws.SetPongHandler(func(string) error {
		j.dispatcher.RefreshFilterTimeouts(wrapConn)

		return extendReadDeadline()
	})

	j.logger.Info("Websocket connection established")

	var perConn uint64
	if j.wsLimiter != nil {
		perConn = j.wsLimiter.perConn
	}

	inFlight := newWSConnSlots(perConn)

	// Run the listen loop
	for {
		// Read the incoming message
		msgType, message, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseAbnormalClosure,
			) {
				// Accepted close codes
				j.logger.Info("Closing WS connection gracefully")
			} else {
				j.logger.Error(fmt.Sprintf("Unable to read WS message, %s", err.Error()))
				j.logger.Info("Closing WS connection with error")
			}

			j.dispatcher.RemoveFilterByWs(wrapConn)

			break
		}

		if deadlineErr := extendReadDeadline(); deadlineErr != nil {
			j.logger.Error(
				fmt.Sprintf("Unable to refresh WS read deadline, %s", deadlineErr.Error()),
			)

			j.dispatcher.RemoveFilterByWs(wrapConn)

			break
		}

		if isSupportedWSType(msgType) {
			if !inFlight.try() {
				j.replyWSLimitExceeded(wrapConn, msgType, message)

				continue
			}

			if !j.wsLimiter.tryAcquireInFlight() {
				inFlight.release()
				j.replyWSLimitExceeded(wrapConn, msgType, message)

				continue
			}

			go func(msgType int, message []byte) {
				defer inFlight.release()
				defer j.wsLimiter.releaseInFlight()

				defer func() {
					if r := recover(); r != nil {
						// Log the panic details
						j.logger.Error(fmt.Sprintf("Recovered from panic: %v", r))
					}
				}()

				resp, handleErr := j.dispatcher.HandleWs(message, wrapConn)
				if handleErr != nil {
					j.logger.Error(fmt.Sprintf("Unable to handle WS request, %s", handleErr.Error()))

					resp = []byte(fmt.Sprintf("WS Handle error: %s", handleErr.Error()))
				}

				if writeErr := wrapConn.WriteMessage(msgType, resp); errors.Is(writeErr, errWSWriteQueueFull) {
					j.logger.Warn("Closing WS connection, peer is not draining its socket")

					_ = wrapConn.Close()
				}
			}(msgType, message)
		}
	}
}

func (j *JSONRPC) handle(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set(
		"Access-Control-Allow-Headers",
		"Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization",
	)

	switch req.Method {
	case "POST":
		j.handleJSONRPCRequest(w, req)
	case "GET":
		j.handleGetRequest(w)
	case "OPTIONS":
		// nothing to return
	default:
		_, _ = w.Write([]byte("method " + req.Method + " not allowed"))
	}
}

func (j *JSONRPC) handleJSONRPCRequest(w http.ResponseWriter, req *http.Request) {
	req.Body = http.MaxBytesReader(w, req.Body, j.config.MaxRequestBodySize)

	data, err := io.ReadAll(req.Body)
	if err != nil {
		_, _ = w.Write([]byte(err.Error()))

		return
	}

	// log request
	j.logger.Debug("handle", "request", string(data))

	ctx := otel.GetTextMapPropagator().Extract(req.Context(), propagation.HeaderCarrier(req.Header))

	resp, err := j.dispatcher.Handle(ctx, data)
	if err != nil {
		_, _ = w.Write([]byte(err.Error()))
	} else {
		_, _ = w.Write(resp)
	}

	j.logger.Debug("handle", "response", string(resp))
}

type GetResponse struct {
	Name    string `json:"name"`
	ChainID uint64 `json:"chain_id"`
	Version string `json:"version"`
}

func (j *JSONRPC) handleGetRequest(writer io.Writer) {
	data := &GetResponse{
		Name:    j.config.ChainName,
		ChainID: j.config.ChainID,
		Version: versioning.Version,
	}

	resp, err := json.Marshal(data)
	if err != nil {
		_, _ = writer.Write([]byte(err.Error()))
	}

	if _, err = writer.Write(resp); err != nil {
		_, _ = writer.Write([]byte(err.Error()))
	}
}

func loadTLSCertificate(manager secrets.SecretsManager) (*tls.Certificate, error) {
	if manager.HasSecret(secrets.JSONTLSCert) && manager.HasSecret(secrets.JSONTLSKey) {
		tlsCert, err := manager.GetSecret(secrets.JSONTLSCert)
		if err != nil {
			return nil, fmt.Errorf("unable to get a tls cert file from Secrets Manager, %w", err)
		}

		tlsKey, err := manager.GetSecret(secrets.JSONTLSKey)
		if err != nil {
			return nil, fmt.Errorf("unable to get a tls key file from Secrets Manager, %w", err)
		}

		cert, err := tls.X509KeyPair(tlsCert, tlsKey)
		if err != nil {
			return nil, fmt.Errorf("unable to create a tls certificate, %w", err)
		}

		return &cert, nil
	}

	return nil, secrets.ErrSecretNotFound
}
