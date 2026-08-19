package stm

import (
	"context"
	"runtime"
	"sync"

	"github.com/hashicorp/go-hclog"

	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
)

// Engine runs candidate batches to convergence with a fixed-size worker pool.
type Engine struct {
	workers int
	logger  hclog.Logger
}

func NewEngine(cfg EngineConfig, logger hclog.Logger) *Engine {
	workers := cfg.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	if workers < 1 {
		workers = 1
	}

	return &Engine{workers: workers, logger: logger}
}

// Workers returns the worker pool size this Engine runs batches with, useful for callers
// sizing their own candidate-batch pulls (e.g. a multiple of it).
func (e *Engine) Workers() int {
	return e.workers
}

// phase is a slot's coarse execution state. A slot cycles pending -> executing -> executed,
// possibly bouncing back to pending (same incarnation, if blocked on a dependency) or blocked
// (registered as a waiter, woken once that dependency finishes) before eventually settling on
// executed with validated == true.
type phase uint8

const (
	phasePending phase = iota
	phaseExecuting
	phaseExecuted
	phaseBlocked
)

type slot struct {
	incarnation int
	phase       phase
	validating  bool
	validated   bool

	txn     *state.TxnMVCC
	tran    *state.Transition
	receipt *types.Receipt
	execErr error

	// installedKeys are the keys this slot currently has installed in mv (from its last
	// successful execution), used to compute the delete-diff on re-execution and to mark
	// estimates on invalidation.
	installedKeys []state.Key
}

// env bundles everything a worker needs to actually do execute/validate work; it never
// changes once a batch starts, so it needs no locking of its own.
type env struct {
	executor   *state.Executor
	header     *types.Header
	coinbase   types.Address
	dst        *state.TxnVerifier
	mv         *MVMemory
	candidates []*types.Transaction
}

// run holds one batch's mutable scheduler state, protected by mu. A blockedOn chain always
// points to strictly lower indices (mv.Read only ever returns lower-indexed versions), so
// waiting on it can never cycle or deadlock.
type run struct {
	mu   sync.Mutex
	cond *sync.Cond

	n       int
	slots   []slot
	waiters map[int][]int

	// epoch counts invalidateAboveLocked calls: every time a slot's completed execution
	// installs/retracts writes, or a validation fails, anything validating concurrently against
	// the pre-change state may now be stale. doValidate captures the epoch at claim time and
	// discards (rather than commits) a passing result if the epoch moved before it can commit -
	// see doValidate for why only the passing case needs this (a failing result is always safe
	// to act on immediately).
	epoch int

	stopped bool
	fatal   error
}

// RunBatch executes one fixed-order candidate batch to convergence and finalizes it.
// remainingBlockGas is how much gas the block still has left for this batch; it is used only by
// the deterministic finalize pass, never by speculative execution (see Executor.BeginTxnSTM).
func (e *Engine) RunBatch(
	ctx context.Context,
	executor *state.Executor,
	header *types.Header,
	coinbase types.Address,
	dst *state.TxnVerifier,
	remainingBlockGas uint64,
	candidates []*types.Transaction,
) (*BatchOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	n := len(candidates)
	if n == 0 {
		return &BatchOutcome{}, nil
	}

	r := &run{
		n:       n,
		slots:   make([]slot, n),
		waiters: map[int][]int{},
	}
	r.cond = sync.NewCond(&r.mu)

	batchEnv := &env{
		executor:   executor,
		header:     header,
		coinbase:   coinbase,
		dst:        dst,
		mv:         NewMVMemory(),
		candidates: candidates,
	}

	workers := e.workers
	if workers > n {
		workers = n
	}

	var wg sync.WaitGroup

	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()

			r.worker(batchEnv)
		}()
	}

	wg.Wait()

	if r.fatal != nil {
		return nil, r.fatal
	}

	outcome, winners := finalize(candidates, r.slots, remainingBlockGas)

	if err := state.FlushBatchInto(dst, winners); err != nil {
		return nil, err
	}

	return outcome, nil
}

func (r *run) worker(e *env) {
outer:
	for {
		r.mu.Lock()

		for {
			if r.stopped {
				r.mu.Unlock()

				return
			}

			if idx, ok := r.claimExecute(); ok {
				r.mu.Unlock()
				r.doExecute(e, idx)

				continue outer
			}

			if idx, txn, epoch, ok := r.claimValidate(); ok {
				r.mu.Unlock()
				r.doValidate(e, idx, txn, epoch)

				continue outer
			}

			if r.isConverged() {
				r.stopped = true
				r.cond.Broadcast()
				r.mu.Unlock()

				return
			}

			r.cond.Wait()
		}
	}
}

func (r *run) claimExecute() (int, bool) {
	for i := 0; i < r.n; i++ {
		if r.slots[i].phase == phasePending {
			r.slots[i].phase = phaseExecuting

			return i, true
		}
	}

	return 0, false
}

// claimValidate claims the next unvalidated executed slot for validation, returning its
// current incarnation's TxnMVCC and the epoch at claim time (see run.epoch).
func (r *run) claimValidate() (int, *state.TxnMVCC, int, bool) {
	for i := 0; i < r.n; i++ {
		s := &r.slots[i]
		if s.phase == phaseExecuted && !s.validating && !s.validated {
			s.validating = true

			return i, s.txn, r.epoch, true
		}
	}

	return 0, nil, 0, false
}

func (r *run) isConverged() bool {
	for i := 0; i < r.n; i++ {
		if r.slots[i].phase != phaseExecuted || !r.slots[i].validated {
			return false
		}
	}

	return true
}

// wakeWaitersLocked moves every slot blocked on idx back to pending now that idx has reached
// phaseExecuted. Must be called with r.mu held.
func (r *run) wakeWaitersLocked(idx int) {
	for _, w := range r.waiters[idx] {
		if r.slots[w].phase == phaseBlocked {
			r.slots[w].phase = phasePending
		}
	}

	delete(r.waiters, idx)
}

// invalidateAboveLocked marks every slot above idx as needing re-validation, since idx just
// (re)installed or retracted writes that any of them may have depended on, and bumps the batch
// epoch so any validation already in flight against the pre-change state gets discarded rather
// than committed. Must be called with r.mu held.
func (r *run) invalidateAboveLocked(idx int) {
	r.epoch++

	for j := idx + 1; j < r.n; j++ {
		r.slots[j].validated = false
	}
}

func (r *run) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.fatal == nil {
		r.fatal = err
	}

	r.stopped = true
	r.cond.Broadcast()
}

// doExecute runs one incarnation of candidate idx to completion (or to an ESTIMATE-triggered
// abort) and records the outcome. See state.EstimateAbort for why a panic/recover is used
// here: an mv dependency can only be discovered arbitrarily deep inside EVM execution, and
// this codebase's EVM interpreter has no other abort channel for that.
func (r *run) doExecute(e *env, idx int) {
	r.mu.Lock()
	incarnation := r.slots[idx].incarnation
	r.mu.Unlock()

	tran, txn, err := e.executor.BeginTxnSTM(e.header, e.coinbase, e.dst, e.mv, idx, incarnation)
	if err != nil {
		r.fail(err)

		return
	}

	var (
		receipt   *types.Receipt
		execErr   error
		blockedOn = -1
	)

	func() {
		defer func() {
			if p := recover(); p != nil {
				abort, ok := p.(*state.EstimateAbortError)
				if !ok {
					panic(p) //nolint:gocritic
				}

				blockedOn = abort.BlockedOn
			}
		}()

		receipt, execErr = tran.Write(e.candidates[idx])
	}()

	r.mu.Lock()
	defer r.mu.Unlock()

	if blockedOn >= 0 {
		if blockedOn < r.n && r.slots[blockedOn].phase != phaseExecuted {
			r.slots[idx].phase = phaseBlocked
			r.waiters[blockedOn] = append(r.waiters[blockedOn], idx)
		} else {
			// the tx we were blocked on already finished between our read and this lock -
			// retry immediately rather than register for a wakeup that will never come
			r.slots[idx].phase = phasePending
		}

		r.cond.Broadcast()

		return
	}

	prevKeys := r.slots[idx].installedKeys

	var newKeys []state.Key

	if execErr == nil {
		for _, kv := range txn.WriteSet() {
			e.mv.Write(kv.Key, idx, incarnation, kv.Value)
			newKeys = append(newKeys, kv.Key)
		}
	}

	stillWritten := make(map[state.Key]struct{}, len(newKeys))
	for _, k := range newKeys {
		stillWritten[k] = struct{}{}
	}

	for _, k := range prevKeys {
		if _, ok := stillWritten[k]; !ok {
			e.mv.Delete(k, idx)
		}
	}

	s := &r.slots[idx]
	s.phase = phaseExecuted
	s.txn = txn
	s.tran = tran
	s.receipt = receipt
	s.execErr = execErr
	s.installedKeys = newKeys
	s.validated = false
	s.validating = false

	r.invalidateAboveLocked(idx)
	r.wakeWaitersLocked(idx)
	r.cond.Broadcast()
}

// doValidate re-checks txn's (incarnation's) recorded read-set against mv. txn and epoch are
// exactly what claimValidate observed at claim time - Validate() itself runs outside the lock,
// so both must be re-checked against the slot's current state before committing a result.
func (r *run) doValidate(e *env, idx int, txn *state.TxnMVCC, epochAtClaim int) {
	ok := txn.Validate()

	r.mu.Lock()
	defer r.mu.Unlock()

	s := &r.slots[idx]
	s.validating = false

	if s.phase != phaseExecuted || s.txn != txn {
		// re-executed (one or more times) concurrently while this validation was in flight
		// against a now-superseded incarnation; the result no longer applies to anything
		return
	}

	if !ok {
		// a definite mismatch is always safe to act on immediately, whether or not anything else
		// changed concurrently: it reflects a real inconsistency observed at some point during
		// the check, so re-executing is correct (at worst redundant, never wrong)
		e.mv.MarkEstimate(idx, s.installedKeys)

		s.incarnation++
		s.phase = phasePending
		s.validated = false
		s.txn = nil
		s.tran = nil
		s.receipt = nil
		s.execErr = nil

		r.invalidateAboveLocked(idx)
		r.cond.Broadcast()

		return
	}

	if r.epoch != epochAtClaim {
		// some lower slot completed (or another validation failed) while this one was in
		// flight, which may have reset s.validated to false to force a fresh check - a stale
		// "still consistent" result must not clobber that; leave it alone and let claimValidate
		// pick this slot up again against the now-current state
		r.cond.Broadcast()

		return
	}

	s.validated = true

	r.cond.Broadcast()
}
