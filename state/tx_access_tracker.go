package state

import (
	"github.com/0xPolygon/polygon-edge/consensus/ibft/blockstm"
	"github.com/0xPolygon/polygon-edge/types"
)

type ITxAccessTracker interface {
	Clear(writesOnly bool)
	AddWrite(addr types.Address, subpath byte, val any)
	AddRead(addr types.Address, subpath byte)
	AddStorageWrite(addr types.Address, slot types.Hash, val any)
	AddStorageRead(addr types.Address, slot types.Hash)
	GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet
}

type txAccessTrackerMap struct {
	txReadAccessMap  map[Key]struct{}
	txWriteAccessMap map[Key]struct{}
}

var _ ITxAccessTracker = (*txAccessTrackerMap)(nil)

// Clear resets the access maps
func (a *txAccessTrackerMap) Clear(writesOnly bool) {
	if !writesOnly {
		a.txReadAccessMap = make(map[Key]struct{})
	}

	a.txWriteAccessMap = make(map[Key]struct{})
}

// AddWrite records an account-field write access
func (a *txAccessTrackerMap) AddWrite(addr types.Address, _ byte, _ any) {
	a.txWriteAccessMap[NewSubpathKey(addr, FullPath)] = struct{}{}
}

// AddRead records an account-field read access for a given key.
func (a *txAccessTrackerMap) AddRead(addr types.Address, _ byte) {
	a.txReadAccessMap[NewSubpathKey(addr, FullPath)] = struct{}{}
}

// AddStorageWrite records a contract-storage write
func (a *txAccessTrackerMap) AddStorageWrite(addr types.Address, slot types.Hash, _ any) {
	a.txWriteAccessMap[NewStateKey(addr, slot)] = struct{}{}
}

// AddStorageRead records a contract-storage read (see AddStorageWrite for the key rationale).
func (a *txAccessTrackerMap) AddStorageRead(addr types.Address, slot types.Hash) {
	a.txReadAccessMap[NewStateKey(addr, slot)] = struct{}{}
}

// GetReadWriteSet returns a TxReadWriteSet for the requested transaction index.
func (a *txAccessTrackerMap) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	readDescs := ([]blockstm.ReadDescriptor)(nil)
	writeDescs := ([]blockstm.WriteDescriptor)(nil)

	if len(a.txReadAccessMap) > 0 {
		readDescs = make([]blockstm.ReadDescriptor, 0, len(a.txReadAccessMap))

		for k := range a.txReadAccessMap {
			// A key this tx also wrote must only be reported as a write: the write already
			// conflicts with both the key's previous writer and its previous reader, covering
			// everything the read would. Reporting the read too would overwrite the DAG
			// builder's last-reader record with this tx before its own write-after-read check
			// runs, hiding the previous reader and losing that dependency edge (e.g. tx A calls
			// a contract - code read - and tx B selfdestructs it - account write + code read).
			if _, alsoWritten := a.txWriteAccessMap[k]; alsoWritten {
				continue
			}

			readDescs = append(readDescs, blockstm.ReadDescriptor{
				Path: blockstm.Key(k),
			})
		}
	}

	if len(a.txWriteAccessMap) > 0 {
		writeDescs = make([]blockstm.WriteDescriptor, 0, len(a.txWriteAccessMap))

		for k := range a.txWriteAccessMap {
			writeDescs = append(writeDescs, blockstm.WriteDescriptor{
				Path: blockstm.Key(k),
			})
		}
	}

	return blockstm.TxReadWriteSet{
		Index:     txIndx,
		ReadList:  readDescs,
		WriteList: writeDescs,
	}
}

type txAccessTrackerNoOp struct {
}

var _ ITxAccessTracker = (*txAccessTrackerNoOp)(nil)

func (e *txAccessTrackerNoOp) Clear(writesOnly bool) {}

func (a *txAccessTrackerNoOp) AddWrite(addr types.Address, subpath byte, _ any) {
}

func (a *txAccessTrackerNoOp) AddRead(addr types.Address, subpath byte) {
}

func (a *txAccessTrackerNoOp) AddStorageWrite(addr types.Address, slot types.Hash, _ any) {
}

func (a *txAccessTrackerNoOp) AddStorageRead(addr types.Address, slot types.Hash) {
}

func (e *txAccessTrackerNoOp) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	return blockstm.TxReadWriteSet{Index: txIndx}
}

// txAccessTrackerSingleMap collects one tx's read/write set for the blockstm DAG builder. A write
// shadows a read of the same key on purpose: reporting both would clobber the DAG builder's
// last-reader record before its write-after-read check runs, losing that dependency edge; the
// write alone already covers every conflict the read would.
type txAccessTrackerSingleMap struct {
	txAccessMap map[Key]bool // true is write, false is read
}

var _ ITxAccessTracker = (*txAccessTrackerSingleMap)(nil)

// Clear resets the access map; writesOnly keeps pure reads. A read+written key is stored as a
// write, so it drops with the writes (fine: no caller consumes the set after a writesOnly clear).
func (a *txAccessTrackerSingleMap) Clear(writesOnly bool) {
	if !writesOnly {
		a.txAccessMap = map[Key]bool{}

		return
	}

	for k, v := range a.txAccessMap {
		if v {
			delete(a.txAccessMap, k)
		}
	}
}

// AddWrite records an account-field write access
func (a *txAccessTrackerSingleMap) AddWrite(addr types.Address, _ byte, _ any) {
	a.txAccessMap[NewSubpathKey(addr, FullPath)] = true
}

// AddRead records an account-field read access for a given key.
func (a *txAccessTrackerSingleMap) AddRead(addr types.Address, _ byte) {
	key := NewSubpathKey(addr, FullPath)

	// insert as read only if not tracked yet: a write shadows a read (see struct doc)
	if _, exists := a.txAccessMap[key]; !exists {
		a.txAccessMap[key] = false
	}
}

// AddStorageWrite records a contract-storage write
func (a *txAccessTrackerSingleMap) AddStorageWrite(addr types.Address, slot types.Hash, _ any) {
	a.txAccessMap[NewStateKey(addr, slot)] = true
}

// AddStorageRead records a contract-storage read (see AddStorageWrite for the key rationale).
func (a *txAccessTrackerSingleMap) AddStorageRead(addr types.Address, slot types.Hash) {
	key := NewStateKey(addr, slot)

	// insert as read only if not tracked yet: a write shadows a read (see struct doc)
	if _, exists := a.txAccessMap[key]; !exists {
		a.txAccessMap[key] = false
	}
}

// GetReadWriteSet returns a TxReadWriteSet for the requested transaction index.
func (a *txAccessTrackerSingleMap) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	readDescs := ([]blockstm.ReadDescriptor)(nil)
	writeDescs := ([]blockstm.WriteDescriptor)(nil)

	for k, v := range a.txAccessMap {
		if v {
			writeDescs = append(writeDescs, blockstm.WriteDescriptor{
				Path: blockstm.Key(k),
			})
		} else {
			readDescs = append(readDescs, blockstm.ReadDescriptor{
				Path: blockstm.Key(k),
			})
		}
	}

	return blockstm.TxReadWriteSet{
		Index:     txIndx,
		ReadList:  readDescs,
		WriteList: writeDescs,
	}
}

func TxAccessTrackerFactory(isNoOp bool) ITxAccessTracker {
	if isNoOp {
		return &txAccessTrackerNoOp{}
	}

	return &txAccessTrackerMap{}
}
