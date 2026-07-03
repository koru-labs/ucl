package state

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/types"
)

func newDepTx(id byte) *types.Transaction {
	return &types.Transaction{Hash: types.Hash{id}}
}

func TestTxDependancyPool_Len(t *testing.T) {
	t.Parallel()

	txs := []*types.Transaction{newDepTx(1), newDepTx(2), newDepTx(3)}
	pool := NewTxDependancyPool(txs, [][]uint64{{}, {}, {}})

	require.Equal(t, len(txs), pool.Len())
}

func TestTxDependancyPool_NoDependencies_ReturnsAllTxsImmediately(t *testing.T) {
	t.Parallel()

	txs := []*types.Transaction{newDepTx(1), newDepTx(2), newDepTx(3)}
	pool := NewTxDependancyPool(txs, [][]uint64{{}, {}, {}})

	// with no dependencies, every tx should already be queued in insertion order
	for i := range txs {
		tx, alive := pool.GetTx()
		require.True(t, alive)
		require.Equal(t, uint64(i), tx.Indx)
		require.Equal(t, txs[i], tx.Tx)
	}
}

func TestTxDependancyPool_FinishTx_UnlocksDependentTx(t *testing.T) {
	t.Parallel()

	txs := []*types.Transaction{newDepTx(1), newDepTx(2)}
	// tx1 depends on tx0
	pool := NewTxDependancyPool(txs, [][]uint64{{}, {0}})

	tx0, alive := pool.GetTx()
	require.True(t, alive)
	require.Equal(t, uint64(0), tx0.Indx)

	// tx1 must not be ready yet - GetTx should block until tx0 finishes
	done := make(chan TxWithIndex, 1)

	go func() {
		tx, alive := pool.GetTx()
		require.True(t, alive)

		done <- tx
	}()

	select {
	case <-done:
		t.Fatal("tx1 became available before its dependency finished")
	case <-time.After(50 * time.Millisecond):
	}

	pool.FinishTx(tx0)

	select {
	case tx := <-done:
		require.Equal(t, uint64(1), tx.Indx)
	case <-time.After(time.Second):
		t.Fatal("tx1 was not unblocked after its dependency finished")
	}
}

func TestTxDependancyPool_FinishTx_DiamondDependency(t *testing.T) {
	t.Parallel()

	// tx0 has no deps; tx1 and tx2 depend on tx0; tx3 depends on both tx1 and tx2
	txs := []*types.Transaction{newDepTx(1), newDepTx(2), newDepTx(3), newDepTx(4)}
	pool := NewTxDependancyPool(txs, [][]uint64{{}, {0}, {0}, {1, 2}})

	tx0, alive := pool.GetTx()
	require.True(t, alive)
	require.Equal(t, uint64(0), tx0.Indx)

	pool.FinishTx(tx0)

	// tx1 and tx2 should now both be ready, in some order
	got := map[uint64]bool{}

	for range 2 {
		tx, alive := pool.GetTx()
		require.True(t, alive)

		got[tx.Indx] = true
	}

	require.True(t, got[1])
	require.True(t, got[2])

	pool.FinishTx(TxWithIndex{Tx: txs[1], Indx: 1})

	// tx3 must still be blocked - only one of its two deps is done
	done := make(chan TxWithIndex, 1)

	go func() {
		tx, alive := pool.GetTx()
		require.True(t, alive)

		done <- tx
	}()

	select {
	case <-done:
		t.Fatal("tx3 became available before all its dependencies finished")
	case <-time.After(50 * time.Millisecond):
	}

	pool.FinishTx(TxWithIndex{Tx: txs[2], Indx: 2})

	select {
	case tx := <-done:
		require.Equal(t, uint64(3), tx.Indx)
	case <-time.After(time.Second):
		t.Fatal("tx3 was not unblocked after all its dependencies finished")
	}
}

func TestTxDependancyPool_ClosesAutomaticallyWhenAllProcessed(t *testing.T) {
	t.Parallel()

	txs := []*types.Transaction{newDepTx(1)}
	pool := NewTxDependancyPool(txs, [][]uint64{{}})

	tx, alive := pool.GetTx()
	require.True(t, alive)

	pool.FinishTx(tx)

	// pool must be closed now that every tx was processed
	_, alive = pool.GetTx()
	require.False(t, alive)
}

func TestTxDependancyPool_Close_UnblocksWaitingConsumers(t *testing.T) {
	t.Parallel()

	txs := []*types.Transaction{newDepTx(1), newDepTx(2)}
	// tx1 depends on tx0, so it never becomes ready in this test
	pool := NewTxDependancyPool(txs, [][]uint64{{}, {0}})

	_, alive := pool.GetTx()
	require.True(t, alive)

	var wg sync.WaitGroup

	results := make([]bool, 3)

	for i := range results {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			_, alive := pool.GetTx()
			results[i] = alive
		}(i)
	}

	// give the goroutines a chance to start waiting on the condition variable
	time.Sleep(20 * time.Millisecond)

	pool.Close()

	wg.Wait()

	for _, alive := range results {
		require.False(t, alive)
	}

	// closing twice, or getting from an already closed pool, must not block or panic
	_, alive = pool.GetTx()
	require.False(t, alive)
}

func TestTxDependancyPool_ConcurrentProducersConsumers(t *testing.T) {
	t.Parallel()

	const numTxs = 200

	txs := make([]*types.Transaction, numTxs)
	deps := make([][]uint64, numTxs)

	for i := range txs {
		txs[i] = newDepTx(byte(i))

		// each tx (other than the first two) depends on the two preceding ones,
		// forming a wide graph with plenty of concurrency opportunities
		switch i {
		case 0, 1:
			deps[i] = nil
		default:
			deps[i] = []uint64{uint64(i - 1), uint64(i - 2)}
		}
	}

	pool := NewTxDependancyPool(txs, deps)

	var (
		mu        sync.Mutex
		processed = make(map[uint64]int)
	)

	const numWorkers = 8

	var wg sync.WaitGroup

	wg.Add(numWorkers)

	for range numWorkers {
		go func() {
			defer wg.Done()

			for {
				tx, alive := pool.GetTx()
				if !alive {
					return
				}

				mu.Lock()
				processed[tx.Indx]++
				mu.Unlock()

				pool.FinishTx(tx)
			}
		}()
	}

	wg.Wait()

	require.Len(t, processed, numTxs)

	for i := range uint64(numTxs) {
		assert.Equal(t, 1, processed[i], "tx %d processed an unexpected number of times", i)
	}
}

// Regression test: FinishTx used to range over children as a map, so a fan-out's queue order
// (and thus execution order) was randomized per run - a correctness risk when the same graph is
// replayed (e.g. a validator re-executing a proposer's block). Single worker, many iterations,
// since a flaky map-order bug only shows up some of the time.
func TestTxDependancyPool_FinishTx_FanOutQueueOrderIsDeterministic(t *testing.T) {
	t.Parallel()

	const (
		cntDependant = 20
		itersCount   = 50
	)

	for iter := range itersCount {
		txDeps := make([][]uint64, cntDependant+1)
		txs := make([]*types.Transaction, cntDependant+1)

		for i := range txDeps {
			if i == 0 {
				txDeps[i] = []uint64{}
			} else {
				txDeps[i] = []uint64{0}
			}

			txs[i] = newDepTx(byte(i + 1))
		}

		// tx0 has no deps; tx1, tx2, tx3, ..., txn all depend solely on tx0 (a n-way fan-out)
		pool := NewTxDependancyPool(txs, txDeps)

		tx0, alive := pool.GetTx()
		require.True(t, alive)
		require.Equal(t, uint64(0), tx0.Indx)

		pool.FinishTx(tx0)

		order := make([]uint64, cntDependant)
		expected := make([]uint64, cntDependant)

		for i := range len(order) {
			tx, alive := pool.GetTx()
			require.True(t, alive)

			order[i] = tx.Indx
			expected[i] = uint64(i + 1)
		}

		require.Equal(t, expected, order,
			"iteration %d: children must be queued in a fixed, deterministic order", iter)
	}
}
