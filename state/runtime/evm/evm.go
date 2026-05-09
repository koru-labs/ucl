package evm

import (
	"errors"
	"time"

	"github.com/hashicorp/go-metrics"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/state/runtime"
)

var _ runtime.Runtime = &EVM{}

// EVM is the ethereum virtual machine
type EVM struct {
}

// NewEVM creates a new EVM
func NewEVM() *EVM {
	return &EVM{}
}

// CanRun implements the runtime interface
func (e *EVM) CanRun(*runtime.Contract, runtime.Host, *chain.ForksInTime) bool {
	return true
}

// Name implements the runtime interface
func (e *EVM) Name() string {
	return "evm"
}

// Run implements the runtime interface
func (e *EVM) Run(c *runtime.Contract, host runtime.Host, config *chain.ForksInTime) *runtime.ExecutionResult {
	start := time.Now()
	defer func() {
		metrics.MeasureSince([]string{"evm", "run"}, start)
		metrics.IncrCounter([]string{"evm", "run", "count"}, 1)
	}()

	contract := acquireState()
	contract.resetReturnData()

	contract.msg = c
	contract.code = c.Code
	contract.evm = e
	contract.gas = c.Gas
	contract.host = host
	contract.config = config

	// JUMPDEST analysis is a pure function of `c.Code`, so we look it up in
	// the process-wide bitmap cache keyed by the contract's code hash. The
	// cache short-circuits the O(N) scan that used to dominate per-call
	// overhead on large contracts (see
	// `.cursor/plans/large_contract_perf_analysis.plan.md`).
	//
	// We pull the code hash from the host (O(1) on the account object) rather
	// than hashing the bytes ourselves — re-hashing 50 KB on every call would
	// cost almost as much as the original scan and defeat the optimization.
	// `setCodeWithCache` itself decides whether the (hash, code) pair is
	// cacheable; init-code runs (CREATE/CREATE2) fall back to the owned-buffer
	// path because the host returns EmptyCodeHash before the constructor has
	// produced the deployed bytecode.
	codeHash := host.GetCodeHash(c.CodeAddress)
	contract.bitmap.setCodeWithCache(codeHash, c.Code)

	ret, err := contract.Run()

	// We are probably doing this append magic to make sure that the slice doesn't have more capacity than it needs
	var returnValue []byte

	returnValue = append(returnValue[:0], ret...)

	gasLeft := contract.gas

	releaseState(contract)

	if err != nil && !errors.Is(err, errRevert) {
		gasLeft = 0
	}

	return &runtime.ExecutionResult{
		ReturnValue: returnValue,
		GasLeft:     gasLeft,
		GasUsed:     c.Gas - gasLeft,
		Err:         err,
	}
}
