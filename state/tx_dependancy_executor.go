package state

import (
	"errors"
	"sync"

	"github.com/0xPolygon/polygon-edge/types"
)

type TransitionFactory func(id int) *Transition

type TxDependancyExecutor struct {
	pool       *TxDependancyPool
	workersCnt int
}

func NewTxDependancyExecutorWithWorkers(
	pool *TxDependancyPool, workersCnt int,
) *TxDependancyExecutor {
	return &TxDependancyExecutor{
		pool:       pool,
		workersCnt: workersCnt,
	}
}

func (t *TxDependancyExecutor) Execute(
	transitionFactory TransitionFactory,
) (*Transition, []*types.Receipt, error) {
	wg := sync.WaitGroup{}
	trans := make([]*Transition, t.workersCnt)
	receipts := make([]*types.Receipt, t.pool.Len())
	errs := make([]error, t.workersCnt)

	for i := range trans {
		trans[i] = transitionFactory(i)
	}

	wg.Add(t.workersCnt)

	for i := range t.workersCnt {
		go func(id int, tran *Transition) {
			defer wg.Done()

			for {
				tx, alive := t.pool.GetTx()
				if !alive {
					return
				}

				receipt, err := tran.Write(tx.Tx)
				if err != nil {
					errs[id] = err

					t.pool.Close() // failed to process one tx -> quit

					return
				}

				receipts[tx.Indx] = receipt
				t.pool.FinishTx(tx)
			}
		}(i, trans[i])
	}

	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}

	// TODO: merge all transitions into one

	return trans[0], receipts, nil
}
