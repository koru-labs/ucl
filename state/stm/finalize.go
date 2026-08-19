package stm

import (
	"errors"

	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
)

// classification says what txpool bookkeeping (if any) a failed candidate needs.
type classification int

const (
	classifyDrop classification = iota
	classifyDemote
	// classifyUntouched: leave the candidate in the pool untouched. Used both for a
	// gas-limit-cutoff candidate and, deliberately, for a GasLimitReachedTransitionApplicationError
	// on a single incarnation - under speculative execution that error can fire on a tx that
	// wasn't actually at the true (sequential, cumulative-order) cutoff, purely because several
	// incarnations raced for the batch's shared speculative gas pool; see RunBatch's gas
	// accounting. Penalizing the account (Demote) for a scheduling artifact would be unfair, and
	// finalize's own cumulative-gas walk below is the only authoritative "is the block full" check
	// anyway - so the safe, simple answer is to just retry it untouched.
	classifyUntouched
)

func classifyErr(err error) classification {
	var gasLimitErr *state.GasLimitReachedTransitionApplicationError
	if errors.As(err, &gasLimitErr) {
		return classifyUntouched
	}

	var transitionErr *state.TransitionApplicationError
	if errors.As(err, &transitionErr) && transitionErr.IsRecoverable {
		return classifyDemote
	}

	return classifyDrop
}

// finalize walks a converged batch's slots in candidate (final) order, deciding the true
// gas-limit cutoff, assigning sequential CumulativeGasUsed, and classifying every candidate
// for the caller's txpool bookkeeping. It returns the winning TxnMVCC incarnations for
// Included, in order, for the caller to merge via state.FlushBatchInto.
func finalize(candidates []*types.Transaction, slots []slot, remainingBlockGas uint64) (*BatchOutcome, []*state.TxnMVCC) {
	outcome := &BatchOutcome{}

	var (
		cumGas        uint64
		cutoffReached bool
		winners       []*state.TxnMVCC
	)

	for i, tx := range candidates {
		if cutoffReached {
			continue
		}

		s := &slots[i]

		if s.execErr != nil {
			switch classifyErr(s.execErr) {
			case classifyDrop:
				outcome.Drop = append(outcome.Drop, tx)
			case classifyDemote:
				outcome.Demote = append(outcome.Demote, tx)
			case classifyUntouched:
				// left in the pool, no bookkeeping call at all
			}

			continue
		}

		if cumGas+s.receipt.GasUsed > remainingBlockGas {
			cutoffReached = true

			continue
		}

		cumGas += s.receipt.GasUsed
		s.receipt.CumulativeGasUsed = cumGas

		outcome.Included = append(outcome.Included, tx)
		outcome.Receipts = append(outcome.Receipts, s.receipt)
		outcome.Pop = append(outcome.Pop, tx)
		winners = append(winners, s.txn)
	}

	outcome.GasUsed = cumGas

	outcome.ReadWriteSets = make([]state.TxReadWriteSet, len(winners))
	for i, txn := range winners {
		outcome.ReadWriteSets[i] = txn.GetReadWriteSet(i)
	}

	return outcome, winners
}
