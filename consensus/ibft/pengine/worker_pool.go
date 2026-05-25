package pengine

import (
	"runtime"
	"sync"
)

const initialWorkerPoolCapacity = 16

type IWorkerPool interface {
	Start()
	Submit(task func())
	Stop(force bool)
}

// WorkerPool maintains a pool of workers that drain a shared, unbounded task queue.
// Submit never blocks: tasks are enqueued and the first idle worker picks them up.
type WorkerPool struct {
	mu           sync.Mutex
	cond         *sync.Cond
	wg           sync.WaitGroup
	queue        *RingBuffer[func()]
	workersCount int
	stopped      bool
}

// NewWorkerPool creates a worker pool with a number of workers equal to the number of CPU cores * 2.
func NewWorkerPool() *WorkerPool {
	return NewWorkerPoolWithWorkersCount(runtime.NumCPU() * 2)
}

// NewWorkerPoolWithWorkersCount creates a worker pool with a specified number of workers.
func NewWorkerPoolWithWorkersCount(workerCount int) *WorkerPool {
	wp := &WorkerPool{
		queue:        NewRingBuffer[func()](initialWorkerPoolCapacity),
		workersCount: workerCount,
	}

	wp.cond = sync.NewCond(&wp.mu)

	return wp
}

// Start is a no-op since workers are started in the constructor.
func (wp *WorkerPool) Start() {
	wp.wg.Add(wp.workersCount)

	for range wp.workersCount {
		go wp.worker()
	}
}

// Submit enqueues a task. The first available worker will execute it.
func (wp *WorkerPool) Submit(task func()) {
	wp.mu.Lock()
	wp.queue.Push(task)
	wp.cond.Signal()
	wp.mu.Unlock()
}

// Stop signals all workers to finish after draining the queue and waits for them.
func (wp *WorkerPool) Stop(force bool) {
	wp.mu.Lock()

	wp.stopped = true

	if force {
		wp.queue = NewRingBuffer[func()](initialWorkerPoolCapacity) // discard pending tasks
	}

	wp.cond.Broadcast()
	wp.mu.Unlock()

	wp.wg.Wait()
}

func (wp *WorkerPool) worker() {
	defer wp.wg.Done()

	for {
		wp.mu.Lock()

		for wp.queue.Len() == 0 && !wp.stopped {
			wp.cond.Wait()
		}

		if wp.stopped && wp.queue.Len() == 0 {
			wp.mu.Unlock()

			return
		}

		task := wp.queue.Pop()

		wp.mu.Unlock()

		task()
	}
}
