package pengine

import (
	"sync"

	"github.com/0xPolygon/polygon-edge/types"
)

type TxExecutor func(tx *types.Transaction) bool

const initialQueueCapacity = 16

type AddressQueueManager struct {
	mu         sync.Mutex
	cond       *sync.Cond
	stopped    bool
	current    *RingBuffer[pendingTask]
	next       map[types.Address]*RingBuffer[*types.Transaction]
	inCurrent  map[types.Address]bool
	txExecutor TxExecutor
	workerPool IWorkerPool
}

func NewAddressQueueManager(
	workerPool IWorkerPool,
	executor TxExecutor,
) *AddressQueueManager {
	q := &AddressQueueManager{
		current:    NewRingBuffer[pendingTask](initialQueueCapacity),
		next:       make(map[types.Address]*RingBuffer[*types.Transaction]),
		inCurrent:  make(map[types.Address]bool),
		txExecutor: executor,
		workerPool: workerPool,
	}

	q.cond = sync.NewCond(&q.mu)

	return q
}

func (q *AddressQueueManager) Enqueue(addr types.Address, tx *types.Transaction) {
	q.mu.Lock()

	if !q.inCurrent[addr] {
		q.inCurrent[addr] = true
		q.current.Push(pendingTask{addr: addr, tx: tx})
	} else {
		if _, ok := q.next[addr]; !ok {
			q.next[addr] = NewRingBuffer[*types.Transaction](initialQueueCapacity)
		}

		q.next[addr].Push(tx)
	}

	q.cond.Signal()
	q.mu.Unlock()
}

func (q *AddressQueueManager) Stop() {
	q.mu.Lock()
	q.stopped = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *AddressQueueManager) Process() {
	for {
		// wait until there is work or we're stopped
		q.mu.Lock()
		for q.current.IsEmpty() && !q.stopped {
			q.cond.Wait()
		}

		if q.stopped && q.current.IsEmpty() {
			q.mu.Unlock()

			return
		}

		task := q.current.Pop()
		q.inCurrent[task.addr] = false
		q.mu.Unlock()

		// submit work to the pool; the worker will handle promoting the address
		tx, addr := task.tx, task.addr
		q.workerPool.Submit(func() {
			// execute transaction; if it requests shutdown, set stopped under lock
			shouldContinue := q.txExecutor(tx)
			if !shouldContinue {
				q.Stop()
			} else {
				q.promoteAddr(addr)
			}
		})
	}
}

func (q *AddressQueueManager) promoteAddr(addr types.Address) {
	q.mu.Lock()

	rb, ok := q.next[addr]
	if !ok || rb.IsEmpty() || q.stopped {
		q.mu.Unlock()

		return
	}

	tx := rb.Pop()
	q.current.Push(pendingTask{addr: addr, tx: tx})
	q.inCurrent[addr] = true

	q.cond.Signal()
	q.mu.Unlock()
}

// PendingTask pairs an address with its next ready-to-execute task.
type pendingTask struct {
	addr types.Address
	tx   *types.Transaction
}
