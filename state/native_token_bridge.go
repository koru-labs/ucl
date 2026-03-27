package state

import (
	"errors"
	"log"

	"github.com/0xPolygon/polygon-edge/types"
)

// NativeTokenBridge provides Native Token support for EVM calls
// This enables Solidity contracts to call Native Token functions directly

// NativeTokenChecker interface to check if an address is a Native Token
// This avoids import cycle with hamsa package
type NativeTokenChecker interface {
	IsNativeToken(addr types.Address) bool
}

// NativeTokenExecutor interface to execute Native Token operations
// This avoids import cycle with hamsa package
type NativeTokenExecutor interface {
	ExecuteFromSolidity(caller, to types.Address, input []byte, gas uint64, transition interface{}) ([]byte, uint64, error)
}

var (
	// Global token checker (set by hamsa package during initialization)
	globalTokenChecker NativeTokenChecker

	// Global native token executor (set by hamsa package during initialization)
	globalNativeExecutor NativeTokenExecutor
)

// SetNativeTokenChecker sets the global native token checker
// Called by hamsa package during initialization
func SetNativeTokenChecker(checker NativeTokenChecker) {
	globalTokenChecker = checker
}

// SetNativeTokenExecutor sets the global native token executor
// Called by hamsa package during initialization
func SetNativeTokenExecutor(executor NativeTokenExecutor) {
	globalNativeExecutor = executor
}

// IsNativeToken checks if the given address is a registered Native Token contract
func (t *Transition) IsNativeToken(addr types.Address) bool {
	if globalTokenChecker == nil {
		return false
	}
	return globalTokenChecker.IsNativeToken(addr)
}

// CallNativeToken handles a call to a Native Token contract from within the EVM
// This is called when the EVM executes a CALL opcode to a Native Token address
func (t *Transition) CallNativeToken(
	caller types.Address,
	to types.Address,
	input []byte,
	gas uint64,
) ([]byte, uint64, error) {
	log.Printf("[NativeTokenBridge] CallNativeToken: caller=%s, to=%s, inputLen=%d, gas=%d", caller.String(), to.String(), len(input), gas)
	if globalNativeExecutor == nil {
		log.Printf("[NativeTokenBridge] CallNativeToken: globalNativeExecutor is nil!")
		return nil, gas, errors.New("native token bridge not initialized")
	}

	// Delegate to the native token executor
	ret, gasLeft, err := globalNativeExecutor.ExecuteFromSolidity(caller, to, input, gas, t)
	log.Printf("[NativeTokenBridge] CallNativeToken result: retLen=%d, gasLeft=%d, err=%v", len(ret), gasLeft, err)
	return ret, gasLeft, err
}
