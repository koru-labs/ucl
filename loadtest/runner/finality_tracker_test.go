package runner

import (
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/types"
)

// TestFinalityTrackerRecordStopRace hammers record from many goroutines while
// stopAndCompute closes the channel, and keeps calling record after the stop.
// It must never panic with "send on closed channel" or trip the race detector.
// Run with: go test -race -run TestFinalityTrackerRecordStopRace ./loadtest/runner/...
//
// No workers are started, so stopAndCompute's wg.Wait returns immediately and
// the test exercises the record()-vs-close() synchronization directly.
func TestFinalityTrackerRecordStopRace(t *testing.T) {
	t.Parallel()

	f := newFinalityTracker(ethClientList{}, time.Second)

	const senders = 64

	var (
		wg   sync.WaitGroup
		stop = make(chan struct{})
	)

	for i := 0; i < senders; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for {
				select {
				case <-stop:
					// Keep calling after the stop signal to prove record stays
					// safe once the channel has been closed.
					for j := 0; j < 200; j++ {
						f.record(types.Hash{}, time.Now())
					}

					return
				default:
					f.record(types.Hash{}, time.Now())
				}
			}
		}()
	}

	// Let the senders ramp up so the close lands while records are in flight.
	time.Sleep(10 * time.Millisecond)

	_ = f.stopAndCompute()

	// A second call must not panic on an already-closed channel.
	_ = f.stopAndCompute()

	// Release the senders for their post-close burst, then wait for them.
	close(stop)
	wg.Wait()
}

// TestFinalityTrackerStopIdempotent verifies stopAndCompute can be called more
// than once without panicking and that the first call returns a zero result
// when nothing was measured.
func TestFinalityTrackerStopIdempotent(t *testing.T) {
	t.Parallel()

	f := newFinalityTracker(ethClientList{}, time.Second)

	first := f.stopAndCompute()
	if first.measured != 0 {
		t.Fatalf("expected 0 measured samples, got %d", first.measured)
	}

	// Second call returns the zero result and must not double-close.
	second := f.stopAndCompute()
	if second.measured != 0 || second.p50 != 0 {
		t.Fatalf("expected zero result on repeat stop, got %+v", second)
	}
}

// TestFinalityTrackerRecordAfterStopDrops confirms that records arriving after
// stop are silently ignored (neither enqueued nor counted as dropped) rather
// than panicking.
func TestFinalityTrackerRecordAfterStopDrops(t *testing.T) {
	t.Parallel()

	f := newFinalityTracker(ethClientList{}, time.Second)

	_ = f.stopAndCompute()

	// Should be a no-op; in particular must not send on the closed channel.
	for i := 0; i < 1000; i++ {
		f.record(types.Hash{}, time.Now())
	}

	if got := f.dropped.Load(); got != 0 {
		t.Fatalf("post-stop records must not count as dropped, got %d", got)
	}
}

// TestPercentile checks the nearest-rank percentile helper.
func TestPercentile(t *testing.T) {
	t.Parallel()

	d := func(n int) time.Duration { return time.Duration(n) }

	oneToTen := []time.Duration{d(1), d(2), d(3), d(4), d(5), d(6), d(7), d(8), d(9), d(10)}

	tests := []struct {
		name   string
		sorted []time.Duration
		p      int
		want   time.Duration
	}{
		{"empty", nil, 50, 0},
		{"single high p", []time.Duration{d(5)}, 99, d(5)},
		{"p50 of 1..10", oneToTen, 50, d(5)},
		{"p95 of 1..10", oneToTen, 95, d(10)},
		{"p99 of 1..10", oneToTen, 99, d(10)},
		{"p100 of 1..10", oneToTen, 100, d(10)},
		{"p10 of 1..10", oneToTen, 10, d(1)},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := percentile(tt.sorted, tt.p); got != tt.want {
				t.Fatalf("percentile(%v, %d) = %v, want %v", tt.sorted, tt.p, got, tt.want)
			}
		})
	}
}
