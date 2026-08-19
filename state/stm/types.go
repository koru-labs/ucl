// Package stm implements a Block-STM style optimistic parallel transaction executor for the
// block-building (proposer) path: candidate transactions from a fixed-order batch are
// speculatively executed in parallel against a shared multi-version memory, validated, and
// aborted/re-executed on conflict, converging on a result equivalent to running the batch
// strictly sequentially in candidate order.
package stm

import (
	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
)

// EngineConfig configures an Engine.
type EngineConfig struct {
	// Workers is the number of goroutines collaboratively executing/validating one batch.
	// Defaults to runtime.NumCPU() when <= 0.
	Workers int
}

// BatchOutcome is the result of running one candidate batch to convergence and finalizing it.
type BatchOutcome struct {
	// Included is the batch's candidates that made it into the block, in final order.
	Included []*types.Transaction
	// Receipts is parallel to Included, with CumulativeGasUsed already assigned sequentially.
	Receipts []*types.Receipt
	// ReadWriteSets is parallel to Included, 0..k-1 indexed, ready for
	// blockstm.DepsBuilder.AddTransaction.
	ReadWriteSets []state.TxReadWriteSet
	// Pop, Drop and Demote name which of the batch's candidates the caller must apply that
	// txpool bookkeeping call to. Every candidate not named in any of these three, and not in
	// Included, was left completely untouched (e.g. excluded purely by the gas-limit cutoff,
	// or a single-tx gas-pool hiccup) and should simply be left in the pool for a future round.
	Pop    []*types.Transaction
	Drop   []*types.Transaction
	Demote []*types.Transaction
	// GasUsed is the total gas consumed by Included, i.e. what the caller should debit from
	// the block's authoritative remaining-gas counter.
	GasUsed uint64
}
