// Package halt is ucl's side of the ucl to pitau cutover (pitau's docs/plans/ucl-migration.md §7.4):
// the chain halts after the first block h for which the migration's attestation contract
// (migration/contracts, MigrationAttestations) answers shouldHalt(h) on the state of block h-1.
// Every node evaluates the same call on the same state, so IBFT needs no coordination: validators
// stop proposing and every node refuses any block after h.
package halt

import (
	"errors"
	"math/big"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
)

// ErrHalted is returned for a block after the cutover block: ucl has handed over to pitau.
var ErrHalted = errors.New("ucl has halted for the migration to pitau: no block after the cutover block")

var shouldHaltSelector = crypto.Keccak256([]byte("shouldHalt(uint256)"))[:4]

// TxnBeginner opens a transition on a block's state, as ucl's executor does.
type TxnBeginner interface {
	BeginTxn(parentRoot types.Hash, header *types.Header, coinbase types.Address) (*state.Transition, error)
}

// ShouldHalt evaluates `contract.shouldHalt(h)` on the state of `parent`, block h-1. A contract that
// is not deployed at that block, reverts or answers anything but true means no halt.
func ShouldHalt(exec TxnBeginner, contract types.Address, parent *types.Header, h uint64) (bool, error) {
	txn, err := exec.BeginTxn(parent.StateRoot, parent, types.ZeroAddress)
	if err != nil {
		return false, err
	}

	txn.SetNonPayable(true)

	input := append(append([]byte{}, shouldHaltSelector...), types.BytesToHash(new(big.Int).SetUint64(h).Bytes()).Bytes()...)

	result, err := txn.Apply(&types.Transaction{
		From:     types.ZeroAddress,
		To:       &contract,
		Input:    input,
		Nonce:    txn.GetNonce(types.ZeroAddress),
		Gas:      parent.GasLimit,
		GasPrice: big.NewInt(0),
		Value:    big.NewInt(0),
	})
	if err != nil {
		return false, err
	}

	if result.Failed() || len(result.ReturnValue) != types.HashLength {
		return false, nil
	}

	return types.BytesToHash(result.ReturnValue) == types.BytesToHash([]byte{1}), nil
}
