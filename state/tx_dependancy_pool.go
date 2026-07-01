package state

import (
	"sync"

	"github.com/0xPolygon/polygon-edge/types"
)

type TxWithIndex struct {
	Tx   *types.Transaction
	Indx uint64
}

type TxDependancyPool struct {
	mu           sync.Mutex
	cond         *sync.Cond
	txs          []*types.Transaction
	deps         []map[uint64]struct{}
	children     []map[uint64]struct{}
	queues       []TxWithIndex // no need for better structure, it will grow to len(txs) size
	processedCnt int
	closed       bool
}

func NewTxDependancyPool(
	txs []*types.Transaction,
	depsMatrice [][]uint64,
) *TxDependancyPool {
	deps := make([]map[uint64]struct{}, len(depsMatrice))
	children := make([]map[uint64]struct{}, len(depsMatrice))
	queues := ([]TxWithIndex)(nil)

	for i, mat := range depsMatrice {
		deps[i] = make(map[uint64]struct{}, len(mat))

		for _, ind := range mat {
			deps[i][ind] = struct{}{}

			if children[ind] == nil {
				children[ind] = map[uint64]struct{}{}
			}

			children[ind][uint64(i)] = struct{}{}
		}

		if len(mat) == 0 {
			queues = append(queues, TxWithIndex{
				Tx: txs[i], Indx: uint64(i),
			})
		}
	}

	t := &TxDependancyPool{
		txs:      txs,
		deps:     deps,
		queues:   queues,
		children: children,
	}

	t.cond = sync.NewCond(&t.mu)

	return t
}

func (t *TxDependancyPool) FinishTx(tx TxWithIndex) {
	t.mu.Lock()
	defer t.mu.Unlock()

	newlyReady := 0

	for cind := range t.children[tx.Indx] {
		delete(t.deps[cind], tx.Indx)

		if len(t.deps[cind]) == 0 {
			newlyReady++

			t.queues = append(t.queues, TxWithIndex{
				Tx:   t.txs[cind],
				Indx: cind,
			})
		}
	}

	t.processedCnt++

	if t.processedCnt == len(t.txs) {
		t.closeUnlocked()
	} else {
		for range newlyReady {
			t.cond.Signal()
		}
	}
}

func (t *TxDependancyPool) GetTx() (TxWithIndex, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for len(t.queues) == 0 && !t.closed {
		t.cond.Wait()
	}

	if t.closed {
		return TxWithIndex{}, false
	}

	tx := t.queues[0]
	t.queues = t.queues[1:]

	return tx, true
}

func (t *TxDependancyPool) Len() int {
	return len(t.txs)
}

func (t *TxDependancyPool) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.closeUnlocked()
}

func (t *TxDependancyPool) closeUnlocked() {
	t.closed = true

	t.cond.Broadcast()
}
