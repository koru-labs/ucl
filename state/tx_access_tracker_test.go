package state

import (
	"testing"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/require"
)

// trackerCase pairs a concrete ITxAccessTracker implementation with the assertions that differ
// between implementations, so every test below runs against both trackers.
type trackerCase struct {
	name    string
	factory func() ITxAccessTracker
	// shadowedReadSurvivesWritesOnlyClear reports whether a read that was later shadowed by a
	// write of the same key survives a Clear(writesOnly).
	shadowedReadSurvivesWritesOnlyClear bool
}

func trackerCases() []trackerCase {
	return []trackerCase{
		{
			name: "map",
			factory: func() ITxAccessTracker {
				t := &txAccessTrackerMap{}
				t.Clear(false)

				return t
			},
			shadowedReadSurvivesWritesOnlyClear: true,
		},
	}
}

// trackerSets flattens GetReadWriteSet into key sets for easy assertions.
func trackerSets(t *testing.T, tracker ITxAccessTracker, txIndx int) (reads, writes map[Key]struct{}) {
	t.Helper()

	rw := tracker.GetReadWriteSet(txIndx)
	require.Equal(t, txIndx, rw.Index)

	reads = map[Key]struct{}{}
	writes = map[Key]struct{}{}

	for _, d := range rw.ReadList {
		reads[d.Path] = struct{}{}
	}

	for _, d := range rw.WriteList {
		writes[d.Path] = struct{}{}
	}

	return reads, writes
}

func TestTxAccessTracker_Empty(t *testing.T) {
	t.Parallel()

	for _, tc := range trackerCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rw := tc.factory().GetReadWriteSet(7)
			require.Equal(t, 7, rw.Index)
			require.Empty(t, rw.ReadList)
			require.Empty(t, rw.WriteList)
		})
	}
}

func TestTxAccessTracker_WriteShadowsRead(t *testing.T) {
	t.Parallel()

	for _, tc := range trackerCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			addrA, addrB := types.StringToAddress("0xA"), types.StringToAddress("0xB")
			slot := types.StringToHash("0x1")

			tracker := tc.factory()

			// read then write of the same slot: must be reported only as a write, in either order
			tracker.AddStorageRead(addrA, slot)
			tracker.AddStorageWrite(addrA, slot, nil)

			// write then read of the same account: the later read must not downgrade the write
			tracker.AddWrite(addrA, FullPath, nil)
			tracker.AddRead(addrA, FullPath)

			// pure read stays a read
			tracker.AddRead(addrB, FullPath)

			reads, writes := trackerSets(t, tracker, 3)

			slotKey := NewStateKey(addrA, slot)
			accAKey := NewSubpathKey(addrA, FullPath)
			accBKey := NewSubpathKey(addrB, FullPath)

			require.Equal(t, map[Key]struct{}{slotKey: {}, accAKey: {}}, writes)
			// a key reported as a write must never also appear as a read: it would clobber the
			// DAG builder's last-reader record and lose write-after-read dependency edges
			require.Equal(t, map[Key]struct{}{accBKey: {}}, reads)
		})
	}
}

func TestTxAccessTracker_Dedup(t *testing.T) {
	t.Parallel()

	for _, tc := range trackerCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			addr := types.StringToAddress("0xA")
			slot := types.StringToHash("0x1")

			tracker := tc.factory()

			// repeated accesses of the same key must collapse to a single descriptor
			tracker.AddRead(addr, FullPath)
			tracker.AddRead(addr, FullPath)
			tracker.AddStorageRead(addr, slot)
			tracker.AddStorageRead(addr, slot)
			tracker.AddStorageWrite(addr, slot, nil)
			tracker.AddStorageWrite(addr, slot, nil)

			rw := tracker.GetReadWriteSet(0)
			require.Len(t, rw.ReadList, 1, "the account read must appear exactly once")
			require.Len(t, rw.WriteList, 1, "the storage write must appear exactly once")

			reads, writes := trackerSets(t, tracker, 0)
			require.Equal(t, map[Key]struct{}{NewSubpathKey(addr, FullPath): {}}, reads)
			require.Equal(t, map[Key]struct{}{NewStateKey(addr, slot): {}}, writes)
		})
	}
}

func TestTxAccessTracker_Clear(t *testing.T) {
	t.Parallel()

	addrR, addrW, addrRW := types.StringToAddress("0x1"), types.StringToAddress("0x2"), types.StringToAddress("0x3")

	setup := func(tc trackerCase) ITxAccessTracker {
		tracker := tc.factory()
		tracker.AddRead(addrR, FullPath)        // pure read
		tracker.AddWrite(addrW, FullPath, nil)  // pure write
		tracker.AddRead(addrRW, FullPath)       // read...
		tracker.AddWrite(addrRW, FullPath, nil) // ...then written: stored as a write

		return tracker
	}

	for _, tc := range trackerCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			t.Run("writes only", func(t *testing.T) {
				t.Parallel()

				tracker := setup(tc)
				tracker.Clear(true)

				reads, writes := trackerSets(t, tracker, 0)
				require.Empty(t, writes, "Clear(writesOnly) must drop every write")

				expectedReads := map[Key]struct{}{
					NewSubpathKey(addrR, FullPath): {},
				}
				if tc.shadowedReadSurvivesWritesOnlyClear {
					// the read of addrRW lives in a separate map from its write, so clearing the
					// writes leaves the read behind
					expectedReads[NewSubpathKey(addrRW, FullPath)] = struct{}{}
				}

				require.Equal(t, expectedReads, reads,
					"pure reads must survive Clear(writesOnly)")
			})

			t.Run("everything", func(t *testing.T) {
				t.Parallel()

				tracker := setup(tc)
				tracker.Clear(false)

				reads, writes := trackerSets(t, tracker, 0)
				require.Empty(t, reads)
				require.Empty(t, writes)
			})
		})
	}
}
