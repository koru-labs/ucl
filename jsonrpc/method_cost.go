package jsonrpc

import "strings"

// MethodCost weights a method by the work it causes, relative to a cheap lookup (1).
// Shared by batch budgeting and, later, request rate limiting.
func MethodCost(method string) uint64 {
	if c, ok := methodCosts[method]; ok {
		return c
	}

	if strings.HasPrefix(method, "debug_") {
		return 50
	}

	return 1
}

var methodCosts = map[string]uint64{
	"eth_call":                 5,
	"eth_estimateGas":          20,
	"eth_getLogs":              10,
	"eth_getFilterLogs":        10,
	"eth_feeHistory":           5,
	"eth_getBlockReceipts":     3,
	"eth_getBlockByNumber":     2,
	"eth_getBlockByHash":       2,
	"debug_traceCall":          50,
	"debug_traceTransaction":   50,
	"debug_traceBlockByNumber": 100,
	"debug_traceBlockByHash":   100,
	"debug_traceBlock":         100,
	"debug_traceBlockFromFile": 100,
	"debug_traceChain":         200,
}
