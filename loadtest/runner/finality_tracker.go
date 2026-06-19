package runner

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/types"
)

// finalityTrackerBuffer is the size of the buffered submission channel. When it
// fills up (workers cannot keep up), record() drops the sample rather than
// blocking the send path, so the load profile is never distorted.
const finalityTrackerBuffer = 16384

// finalityTrackerWorkers is the number of concurrent receipt-polling workers.
const finalityTrackerWorkers = 128

// submittedTx pairs a sent transaction hash with the wall-clock time it was submitted.
type submittedTx struct {
	hash types.Hash
	at   time.Time
}

// finalityResult holds the computed submit->finalized latency distribution.
type finalityResult struct {
	p50      time.Duration
	p95      time.Duration
	p99      time.Duration
	measured int
	dropped  uint64
}

// finalityTracker measures submit->finalized latency for sampled transactions.
//
// It polls for each submitted transaction's receipt concurrently with sending,
// so the first time a receipt is observed approximates when the transaction was
// committed (a single client clock is used, so no NTP/clock-sync is required).
//
// Because receipts in this codebase exist only for committed/written blocks,
// submit->first-seen-receipt is an honest submit->finalized latency. Under heavy
// load the worker pool may not keep up; in that case record() drops samples
// (counted in dropped) rather than back-pressuring the sender, so the result is
// a sample of the finality distribution, not necessarily every transaction.
type finalityTracker struct {
	clients ethClientList
	timeout time.Duration
	in      chan submittedTx

	wg sync.WaitGroup

	// closeMu guards stopped and the close of in. record takes it for reading
	// (many concurrent senders run in parallel), stopAndCompute takes it for
	// writing, so the channel can never be closed while a record is mid-send.
	closeMu sync.RWMutex
	stopped bool

	dropped atomic.Uint64

	mu        sync.Mutex
	latencies []time.Duration
	seen      map[types.Hash]struct{}
}

func newFinalityTracker(clients ethClientList, timeout time.Duration) *finalityTracker {
	return &finalityTracker{
		clients: clients,
		timeout: timeout,
		in:      make(chan submittedTx, finalityTrackerBuffer),
		seen:    make(map[types.Hash]struct{}),
	}
}

// start launches the worker pool that polls for receipts.
func (f *finalityTracker) start(workers int) {
	if workers <= 0 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		f.wg.Add(1)

		go f.worker()
	}
}

func (f *finalityTracker) worker() {
	defer f.wg.Done()

	client := f.clients.getClient()

	for tx := range f.in {
		f.mu.Lock()
		if _, ok := f.seen[tx.hash]; ok {
			f.mu.Unlock()

			continue
		}

		f.seen[tx.hash] = struct{}{}
		f.mu.Unlock()

		latency, ok := f.poll(client, tx)
		if !ok {
			continue
		}

		f.mu.Lock()
		f.latencies = append(f.latencies, latency)
		f.mu.Unlock()
	}
}

// poll waits for the receipt of a single transaction, returning the observed
// submit->finalized latency. It returns ok=false on timeout or a hard RPC error.
func (f *finalityTracker) poll(client *jsonrpc.EthClient, tx submittedTx) (time.Duration, bool) {
	timer := time.NewTimer(f.timeout)
	defer timer.Stop()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	// Immediate first check so very fast finality isn't rounded up to a tick.
	if receipt, err := client.GetTransactionReceipt(tx.hash); err == nil && receipt != nil {
		return time.Since(tx.at), true
	}

	for {
		select {
		case <-ticker.C:
			receipt, err := client.GetTransactionReceipt(tx.hash)
			if err != nil {
				if err.Error() != "not found" {
					return 0, false
				}
			}

			if receipt != nil {
				return time.Since(tx.at), true
			}
		case <-timer.C:
			return 0, false
		}
	}
}

// record enqueues a submitted transaction for finality tracking. It never blocks:
// if the worker pool is saturated the sample is dropped and counted. Calls after
// stopAndCompute are ignored so they can never send on a closed channel.
func (f *finalityTracker) record(hash types.Hash, at time.Time) {
	// Hold the read lock for the whole check-and-send so stopAndCompute (which
	// takes the write lock before closing in) can never close the channel
	// between the stopped check and the send. The send is non-blocking, so the
	// lock is held only briefly.
	f.closeMu.RLock()
	defer f.closeMu.RUnlock()

	if f.stopped {
		return
	}

	select {
	case f.in <- submittedTx{hash: hash, at: at}:
	default:
		f.dropped.Add(1)
	}
}

// stopAndCompute stops accepting new submissions, waits for in-flight polling to
// finish, and returns the p50/p95/p99 of the measured finality latencies along
// with the number of measured and dropped samples. It must be called only after
// all senders have stopped calling record.
func (f *finalityTracker) stopAndCompute() finalityResult {
	// Take the write lock so no record is mid-send, then mark stopped and close
	// the channel. Guard against a double call so a second invocation can never
	// close an already-closed channel.
	f.closeMu.Lock()
	alreadyStopped := f.stopped
	f.stopped = true
	if !alreadyStopped {
		close(f.in)
	}
	f.closeMu.Unlock()

	if alreadyStopped {
		return finalityResult{}
	}

	f.wg.Wait()

	f.mu.Lock()
	defer f.mu.Unlock()

	result := finalityResult{
		measured: len(f.latencies),
		dropped:  f.dropped.Load(),
	}

	if result.measured == 0 {
		return result
	}

	sort.Slice(f.latencies, func(i, j int) bool {
		return f.latencies[i] < f.latencies[j]
	})

	result.p50 = percentile(f.latencies, 50)
	result.p95 = percentile(f.latencies, 95)
	result.p99 = percentile(f.latencies, 99)

	return result
}

// percentile returns the nearest-rank percentile of a sorted slice.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}

	rank := int(math.Ceil(float64(p) / 100.0 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}

	if rank > len(sorted) {
		rank = len(sorted)
	}

	return sorted[rank-1]
}
