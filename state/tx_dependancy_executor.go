package state

import (
	"errors"
	"fmt"
	"sync"

	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
)

type TxDependancyExecutor struct {
	workersCnt int
	logger     hclog.Logger
}

func NewTxDependancyExecutor(
	workersCnt int, logger hclog.Logger,
) *TxDependancyExecutor {
	return &TxDependancyExecutor{
		workersCnt: workersCnt,
		logger:     logger,
	}
}

func (t *TxDependancyExecutor) Execute(
	pool *TxDependancyPool,
	executor *Executor,
	parentRoot types.Hash,
	blockHeader *types.Header,
	blockCreator types.Address,
) (*Transition, []*types.Receipt, error) {
	workersCnt := min(t.workersCnt, pool.Len())

	wg := sync.WaitGroup{}
	trans := make([]*Transition, workersCnt)
	receipts := make([]*types.Receipt, pool.Len())
	errs := make([]error, workersCnt)
	baseRadix := createBlockRadix()
	baseMutex := &sync.RWMutex{} // all transitions using this mutex for accessing/updating baseRadix

	addError := func(id int, err error) {
		errs[id] = err

		pool.Close() // failed to process one tx -> quit
	}

	for i := range trans {
		tran, err := executor.BeginTxnWithCustomTxn(
			parentRoot, blockHeader, blockCreator, func(s Snapshot) ITransitionTxn {
				return NewTxnVerifier(s, baseRadix, baseMutex)
			})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create transition no %d: %w", i, err)
		}

		trans[i] = tran
	}

	wg.Add(workersCnt)

	t.logger.Debug("Parallel Block Execution has been started",
		"workers", workersCnt, "txs", pool.Len())

	for i := range workersCnt {
		go func(id int, tran *Transition) {
			defer wg.Done()

			for {
				tx, alive := pool.GetTx()
				if !alive {
					return
				}

				if tx.Tx.Gas > blockHeader.GasLimit {
					addError(id, runtime.ErrOutOfGas)

					return
				}

				if tx.Tx.From == emptyFrom && tx.Tx.Type != types.StateTx {
					if poolTx, ok := executor.GetPendingTxHook(tx.Tx.Hash); ok {
						tx.Tx.From = poolTx.From
					}
				}

				receipt, err := tran.Write(tx.Tx)
				if err != nil {
					addError(id, err)

					return
				}

				// write local changes to global baseRadix
				if err := tran.PopulateBlockRadix(); err != nil {
					addError(id, err)

					return
				}

				t.logger.Debug("Parallel Block Execution tx processed",
					"tx", tx.Tx.Hash, "ind", tx.Indx, "workerID", id)

				receipts[tx.Indx] = receipt
				pool.FinishTx(tx)
			}
		}(i, trans[i])
	}

	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.logger.Error("Parallel Block Execution failed", "err", err)

		return nil, nil, err
	}

	totalGasUsed := uint64(0)

	for i, r := range receipts {
		totalGasUsed += r.GasUsed

		receipts[i].CumulativeGasUsed = totalGasUsed
	}

	t.logger.Debug("Parallel Block Execution finished", "receipts", len(receipts), "gasUsed", totalGasUsed)

	trans[0].SetTotalGas(totalGasUsed)

	return trans[0], receipts, nil
}
