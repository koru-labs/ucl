package jsonrpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/0xPolygon/polygon-edge/observability"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-metrics"
	jsonIter "github.com/json-iterator/go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var (
	jsonIt     = jsonIter.ConfigCompatibleWithStandardLibrary
	fastJSONIt = jsonIter.ConfigFastest
)

type serviceData struct {
	sv      reflect.Value
	funcMap map[string]*funcData
}

type funcData struct {
	inNum  int
	reqt   []reflect.Type
	fv     reflect.Value
	isDyn  bool
	hasCtx bool
}

func (f *funcData) numParams() int {
	n := f.inNum - 1
	if f.hasCtx {
		n--
	}

	return n
}

type endpoints struct {
	Eth    *Eth
	Web3   *Web3
	Net    *Net
	TxPool *TxPool
	Bridge *Bridge
	Debug  *Debug
}

// Dispatcher handles all json rpc requests by delegating
// the execution flow to the corresponding service
type Dispatcher struct {
	logger        hclog.Logger
	serviceMap    map[string]*serviceData
	filterManager *FilterManager
	endpoints     endpoints

	params *dispatcherParams
}

type dispatcherParams struct {
	chainID   uint64
	chainName string

	priceLimit              uint64
	jsonRPCBatchLengthLimit uint64
	blockRangeLimit         uint64
	filterLimits            FilterLimits

	concurrentRequestsDebug uint64

	blockCacheTTL      time.Duration
	blockCacheCapacity uint64

	enableTxPoolEndpoints   bool
	enableAllDebugEndpoints bool

	requestTimeout  time.Duration
	rpcGasCap       uint64
	batchCostLimit  uint64
	maxResponseSize uint64
}

func (dp dispatcherParams) isExceedingBatchLengthLimit(value uint64) bool {
	return dp.jsonRPCBatchLengthLimit != 0 && value > dp.jsonRPCBatchLengthLimit
}

func (dp dispatcherParams) isExceedingBatchCostLimit(value uint64) bool {
	return dp.batchCostLimit != 0 && value > dp.batchCostLimit
}

func newDispatcher(
	logger hclog.Logger,
	store JSONRPCStore,
	params *dispatcherParams,
) (*Dispatcher, error) {
	d := &Dispatcher{
		logger: logger.Named("dispatcher"),
		params: params,
	}

	if store != nil {
		d.filterManager = NewFilterManager(logger, store, params.blockRangeLimit, params.filterLimits)
		go d.filterManager.Run()
	}

	if err := d.registerEndpoints(store); err != nil {
		return nil, err
	}

	return d, nil
}

func (d *Dispatcher) registerEndpoints(store JSONRPCStore) error {
	d.endpoints.Eth = &Eth{
		logger:        d.logger,
		store:         store,
		chainID:       d.params.chainID,
		filterManager: d.filterManager,
		priceLimit:    d.params.priceLimit,
		cache:         initRPCCache(store, d.logger, d.params.blockCacheTTL, d.params.blockCacheCapacity),
		rpcGasCap:     d.params.rpcGasCap,
	}
	d.endpoints.Net = &Net{
		store,
		d.params.chainID,
	}
	d.endpoints.Web3 = &Web3{
		d.params.chainName,
	}
	d.endpoints.TxPool = &TxPool{
		store,
	}
	d.endpoints.Bridge = &Bridge{
		store,
	}
	d.endpoints.Debug = NewDebug(
		store,
		d.params.concurrentRequestsDebug,
		d.params.enableAllDebugEndpoints,
		d.params.rpcGasCap,
	)

	var err error

	if err = d.registerService("eth", d.endpoints.Eth); err != nil {
		return err
	}

	if err = d.registerService("net", d.endpoints.Net); err != nil {
		return err
	}

	if err = d.registerService("web3", d.endpoints.Web3); err != nil {
		return err
	}

	if d.params.enableTxPoolEndpoints {
		if err = d.registerService("txpool", d.endpoints.TxPool); err != nil {
			return err
		}
	}

	if err = d.registerService("bridge", d.endpoints.Bridge); err != nil {
		return err
	}

	return d.registerService("debug", d.endpoints.Debug)
}

func (d *Dispatcher) getFnHandler(req Request) (*serviceData, *funcData, Error) {
	callName := strings.SplitN(req.Method, "_", 2)
	if len(callName) != 2 {
		return nil, nil, NewMethodNotFoundError(req.Method)
	}

	serviceName, funcName := callName[0], callName[1]

	service, ok := d.serviceMap[serviceName]
	if !ok {
		return nil, nil, NewMethodNotFoundError(req.Method)
	}

	fd, ok := service.funcMap[funcName]

	if !ok {
		return nil, nil, NewMethodNotFoundError(req.Method)
	}

	return service, fd, nil
}

type wsConn interface {
	WriteMessage(messageType int, data []byte) error
	AddFilterID(id string)
	RemoveFilterID(id string)
	GetFilterIDs() []string
	Close() error
}

// as per https://www.jsonrpc.org/specification, the `id` in JSON-RPC 2.0
// can only be a string or a non-decimal integer
func formatID(id interface{}) (interface{}, Error) {
	switch t := id.(type) {
	case string:
		return t, nil
	case float64:
		if t == math.Trunc(t) {
			return int(t), nil
		} else {
			return "", NewInvalidRequestError("Invalid json request")
		}
	case nil:
		return nil, nil
	default:
		return "", NewInvalidRequestError("Invalid json request")
	}
}

func (d *Dispatcher) handleSubscribe(req Request, conn wsConn) (string, Error) {
	var params []interface{}
	if err := jsonIt.Unmarshal(req.Params, &params); err != nil {
		return "", NewInvalidRequestError("Invalid json request")
	}

	if len(params) == 0 {
		return "", NewInvalidParamsError("Invalid params")
	}

	subscribeMethod, ok := params[0].(string)
	if !ok {
		return "", NewSubscriptionNotFoundError(subscribeMethod)
	}

	var (
		filterID string
		filterEr error
	)

	switch {
	case subscribeMethod == "newHeads":
		filterID, filterEr = d.filterManager.NewBlockFilter(conn)
	case subscribeMethod == "logs":
		logQuery, err := decodeLogQueryFromInterface(params[1])
		if err != nil {
			return "", NewInternalError(err.Error())
		}

		filterID, filterEr = d.filterManager.NewLogFilter(logQuery, conn)
	case subscribeMethod == "newPendingTransactions":
		filterID, filterEr = d.filterManager.NewPendingTxFilter(conn)
	default:
		return "", NewSubscriptionNotFoundError(subscribeMethod)
	}

	if filterEr != nil {
		return "", NewLimitExceededError(filterEr.Error())
	}

	return filterID, nil
}

func (d *Dispatcher) handleUnsubscribe(req Request) (bool, Error) {
	var params []interface{}
	if err := jsonIt.Unmarshal(req.Params, &params); err != nil {
		return false, NewInvalidRequestError("Invalid json request")
	}

	if len(params) != 1 {
		return false, NewInvalidParamsError("Invalid params")
	}

	filterID, ok := params[0].(string)
	if !ok {
		return false, NewSubscriptionNotFoundError(filterID)
	}

	return d.filterManager.Uninstall(filterID), nil
}

func (d *Dispatcher) RemoveFilterByWs(conn wsConn) {
	d.filterManager.RemoveFilterByWs(conn)
}

// RefreshFilterTimeouts pushes back the expiry of the connection's filters, called whenever
// the peer proves it is still alive
func (d *Dispatcher) RefreshFilterTimeouts(conn wsConn) {
	d.filterManager.RefreshFilterTimeouts(conn)
}

// newRequestContext bounds one HTTP body / WS frame (single or batch) by the configured timeout.
func (d *Dispatcher) newRequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}

	if d.params.requestTimeout <= 0 {
		return context.WithCancel(parent)
	}

	return context.WithTimeout(parent, d.params.requestTimeout)
}

func (d *Dispatcher) HandleWs(reqBody []byte, conn wsConn) ([]byte, error) {
	ctx, cancel := d.newRequestContext(context.Background())
	defer cancel()

	reqBody = bytes.TrimLeft(reqBody, " \t\r\n")

	// if body begins with [ consider it as a batch request
	if len(reqBody) > 0 && reqBody[0] == '[' {
		var batchReq BatchRequest

		err := jsonIt.Unmarshal(reqBody, &batchReq)
		if err != nil {
			return NewRPCResponse(nil, "2.0", nil,
				NewInvalidRequestError("Invalid json batch request")).Bytes()
		}

		return d.handleBatch(ctx, batchReq, func(ctx context.Context, req Request) Response {
			return d.handleSingleWs(ctx, req, conn)
		})
	}

	var req Request
	if err := jsonIt.Unmarshal(reqBody, &req); err != nil {
		return NewRPCResponse(req.ID, "2.0", nil, NewInvalidRequestError("Invalid json request")).Bytes()
	}

	resp, err := d.handleSingleWs(ctx, req, conn).Bytes()
	if err != nil {
		return nil, err
	}

	return d.capSingleResponse(req.ID, resp)
}

func (d *Dispatcher) handleSingleWs(ctx context.Context, req Request, conn wsConn) Response {
	id, err := formatID(req.ID)
	if err != nil {
		return NewRPCResponse(nil, "2.0", nil, err)
	}

	var response []byte

	switch req.Method {
	case "eth_subscribe":
		var filterID string

		// if the request method is eth_subscribe we need to create a new filter with ws connection
		if filterID, err = d.handleSubscribe(req, conn); err == nil {
			response = []byte(fmt.Sprintf("\"%s\"", filterID))
		}
	case "eth_unsubscribe":
		var ok bool

		if ok, err = d.handleUnsubscribe(req); err == nil {
			response = []byte(strconv.FormatBool(ok))
		}
	default:
		// its a normal query that we handle with the dispatcher. WS connections
		// are long-lived with no per-message traceparent, so start a fresh trace.
		response, err = d.handleReq(ctx, req)
	}

	return NewRPCResponse(id, "2.0", response, err)
}

func (d *Dispatcher) Handle(ctx context.Context, reqBody []byte) ([]byte, error) {
	ctx, cancel := d.newRequestContext(ctx)
	defer cancel()

	x := bytes.TrimLeft(reqBody, " \t\r\n")
	if len(x) == 0 {
		return NewRPCResponse(nil, "2.0", nil, NewInvalidRequestError("Invalid json request")).Bytes()
	}

	if x[0] == '{' {
		var req Request
		if err := jsonIt.Unmarshal(reqBody, &req); err != nil {
			return NewRPCResponse(nil, "2.0", nil, NewInvalidRequestError("Invalid json request")).Bytes()
		}

		if req.Method == "" {
			return NewRPCResponse(req.ID, "2.0", nil, NewInvalidRequestError("Invalid json request")).Bytes()
		}

		resp, err := d.handleReq(ctx, req)

		out, marshalErr := NewRPCResponse(req.ID, "2.0", resp, err).Bytes()
		if marshalErr != nil {
			return nil, marshalErr
		}

		return d.capSingleResponse(req.ID, out)
	}

	// handle batch requests
	var requests BatchRequest
	if err := jsonIt.Unmarshal(reqBody, &requests); err != nil {
		return NewRPCResponse(
			nil,
			"2.0",
			nil,
			NewInvalidRequestError("Invalid json request"),
		).Bytes()
	}

	return d.handleBatch(ctx, requests, func(ctx context.Context, req Request) Response {
		data, err := d.handleReq(ctx, req)

		return NewRPCResponse(req.ID, "2.0", data, err)
	})
}

// handleBatch runs a batch under the shared cost budget, deadline and response-size cap.
func (d *Dispatcher) handleBatch(
	ctx context.Context,
	requests BatchRequest,
	handle func(context.Context, Request) Response,
) ([]byte, error) {
	if d.params.isExceedingBatchLengthLimit(uint64(len(requests))) {
		return NewRPCResponse(
			nil,
			"2.0",
			nil,
			NewInvalidRequestError("Batch request length too long"),
		).Bytes()
	}

	var total uint64
	for _, req := range requests {
		total += MethodCost(req.Method)
	}

	if d.params.isExceedingBatchCostLimit(total) {
		return NewRPCResponse(
			nil,
			"2.0",
			nil,
			NewInvalidRequestError("Batch request cost too high"),
		).Bytes()
	}

	responses := make([][]byte, 0, len(requests))

	var size uint64

	for i, req := range requests {
		var (
			rpcErr Error
			b      []byte
			err    error
		)

		switch {
		case ctx.Err() != nil:
			rpcErr = NewTimeoutError("batch deadline exceeded")
			for _, remaining := range requests[i:] {
				b, err = NewRPCResponse(remaining.ID, "2.0", nil, rpcErr).Bytes()
				if err != nil {
					return nil, err
				}

				responses = append(responses, b)
			}

			return joinBatch(responses), nil
		default:
			resp := handle(ctx, req)

			b, err = resp.Bytes()
			if err != nil {
				return nil, err
			}

			size += uint64(len(b))
			if d.params.maxResponseSize != 0 && size > d.params.maxResponseSize {
				rpcErr = NewLimitExceededError("batch response too large")
				for _, remaining := range requests[i:] {
					b, err = NewRPCResponse(remaining.ID, "2.0", nil, rpcErr).Bytes()
					if err != nil {
						return nil, err
					}

					responses = append(responses, b)
				}

				return joinBatch(responses), nil
			}

			responses = append(responses, b)
		}
	}

	return joinBatch(responses), nil
}

func joinBatch(responses [][]byte) []byte {
	var buf bytes.Buffer

	buf.WriteByte('[')
	buf.Write(bytes.Join(responses, []byte{','}))
	buf.WriteByte(']')

	return buf.Bytes()
}

func (d *Dispatcher) capSingleResponse(id interface{}, resp []byte) ([]byte, error) {
	if d.params.maxResponseSize != 0 && uint64(len(resp)) > d.params.maxResponseSize {
		return NewRPCResponse(id, "2.0", nil, NewLimitExceededError("response too large")).Bytes()
	}

	return resp, nil
}

func (d *Dispatcher) handleReq(ctx context.Context, req Request) ([]byte, Error) {
	// req.Method is untrusted input, so keep it out of the span name and store it
	// as an attribute instead. The name is promoted to the method once validated.
	spanCtx, span := observability.Tracer().Start(ctx, "rpc.request",
		trace.WithAttributes(attribute.String("rpc.method", req.Method)))
	defer span.End()

	d.logger.Debug("request", append([]interface{}{"method", req.Method, "id", req.ID},
		observability.LogFields(spanCtx)...)...)

	service, fd, ferr := d.getFnHandler(req)
	if ferr != nil {
		span.SetStatus(codes.Error, ferr.Error())

		return nil, ferr
	}

	// req.Method is validated now, so it is safe to use as the span name.
	span.SetName(req.Method)

	inArgs := make([]reflect.Value, fd.inNum)
	inArgs[0] = service.sv

	offset := 1

	if fd.hasCtx {
		inArgs[1] = reflect.ValueOf(ctx)
		offset = 2
	}

	inputs := make([]interface{}, fd.numParams())

	for i := 0; i < fd.numParams(); i++ {
		val := reflect.New(fd.reqt[i+offset])
		inputs[i] = val.Interface()
		inArgs[i+offset] = val.Elem()
	}

	if fd.numParams() > 0 {
		if err := jsonIt.Unmarshal(req.Params, &inputs); err != nil {
			return nil, NewInvalidParamsError("Invalid Params")
		}
	}

	var (
		data []byte
		err  error
		ok   bool
	)

	start := time.Now().UTC()
	output := fd.fv.Call(inArgs) // call rpc endpoint function
	// measure execution time of rpc endpoint function
	metrics.SetGauge([]string{jsonRPCMetric, req.Method + "_time"}, float32(time.Now().UTC().Sub(start).Seconds()))

	if err := getError(output[1]); err != nil {
		// measure error on the rpc endpoint function
		metrics.IncrCounter([]string{jsonRPCMetric, req.Method + "_errors"}, 1)
		span.SetStatus(codes.Error, err.Error())
		d.logInternalError(req.Method, err)

		if res := output[0].Interface(); res != nil {
			data, ok = res.([]byte)

			if !ok {
				return nil, NewInternalError(err.Error())
			}
		}

		var rpcErr Error
		if errors.As(err, &rpcErr) {
			return data, rpcErr
		}

		return data, NewInvalidRequestError(err.Error())
	}

	if res := output[0].Interface(); res != nil {
		data, err = fastJSONIt.Marshal(res)
		if err != nil {
			d.logInternalError(req.Method, err)

			return nil, NewInternalError("Internal error")
		}
	}

	return data, nil
}

func (d *Dispatcher) logInternalError(method string, err error) {
	d.logger.Warn("failed to dispatch", "method", method, "err", err)
}

func (d *Dispatcher) registerService(serviceName string, service interface{}) error {
	if d.serviceMap == nil {
		d.serviceMap = map[string]*serviceData{}
	}

	if serviceName == "" {
		return errors.New("jsonrpc: serviceName cannot be empty")
	}

	st := reflect.TypeOf(service)
	if st.Kind() == reflect.Struct {
		return fmt.Errorf("jsonrpc: service '%s' must be a pointer to struct", serviceName)
	}

	funcMap := make(map[string]*funcData)

	for i := 0; i < st.NumMethod(); i++ {
		mv := st.Method(i)
		if mv.PkgPath != "" {
			// skip unexported methods
			continue
		}

		name := lowerCaseFirstRune(mv.Name)
		funcName := serviceName + "_" + name
		fd := &funcData{
			fv: mv.Func,
		}

		var err error

		if fd.inNum, fd.reqt, err = validateFunc(funcName, fd.fv, true); err != nil {
			return fmt.Errorf("jsonrpc: %w", err)
		}

		fd.hasCtx = fd.inNum >= 2 && fd.reqt[1] == contextType

		// check if last item is a pointer
		if fd.numParams() != 0 {
			last := fd.reqt[fd.inNum-1]
			if last.Kind() == reflect.Ptr {
				fd.isDyn = true
			}
		}

		funcMap[name] = fd
	}

	d.serviceMap[serviceName] = &serviceData{
		sv:      reflect.ValueOf(service),
		funcMap: funcMap,
	}

	return nil
}

func validateFunc(funcName string, fv reflect.Value, _ bool) (inNum int, reqt []reflect.Type, err error) {
	if funcName == "" {
		err = fmt.Errorf("funcName cannot be empty")

		return
	}

	ft := fv.Type()
	if ft.Kind() != reflect.Func {
		err = fmt.Errorf("function '%s' must be a function instead of %s", funcName, ft)

		return
	}

	inNum = ft.NumIn()

	if outNum := ft.NumOut(); ft.NumOut() != 2 {
		err = fmt.Errorf("unexpected number of output arguments in the function '%s': %d. Expected 2", funcName, outNum)

		return
	}

	if !isErrorType(ft.Out(1)) {
		err = fmt.Errorf(
			"unexpected type for the second return value of the function '%s': '%s'. Expected '%s'",
			funcName,
			ft.Out(1),
			errt,
		)

		return
	}

	reqt = make([]reflect.Type, inNum)
	for i := 0; i < inNum; i++ {
		reqt[i] = ft.In(i)
	}

	return
}

var (
	errt        = reflect.TypeOf((*error)(nil)).Elem()
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
)

func isErrorType(t reflect.Type) bool {
	return t.Implements(errt)
}

func getError(v reflect.Value) error {
	if v.IsNil() {
		return nil
	}

	extractedErr, ok := v.Interface().(error)
	if !ok {
		return errors.New("invalid type assertion, unable to extract error")
	}

	return extractedErr
}

// lowerCaseFirstRune converts the first character of the string to lowercase.
func lowerCaseFirstRune(str string) string {
	if len(str) == 0 {
		return ""
	}

	return string(unicode.ToLower(rune(str[0]))) + str[1:]
}
