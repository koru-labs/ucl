package state

import "github.com/0xPolygon/polygon-edge/consensus/ibft/blockstm"

type ITxAccessTracker interface {
	Clear()
	AddWrite(key Key, value any)
	AddRead(key Key)
	GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet
}

type txAccessTrackerMap struct {
	txReadAccessMap  map[Key]struct{}
	txWriteAccessMap map[Key]struct{}
}

var _ ITxAccessTracker = (*txAccessTrackerMap)(nil)

// Clear resets the access maps
func (a *txAccessTrackerMap) Clear() {
	a.txReadAccessMap = make(map[Key]struct{})
	a.txWriteAccessMap = make(map[Key]struct{})
}

// AddWrite records a write access for a given key. The value is ignored here.
func (a *txAccessTrackerMap) AddWrite(key Key, _ any) {
	a.txWriteAccessMap[key] = struct{}{}
}

// AddRead records a read access for a given key.
func (a *txAccessTrackerMap) AddRead(key Key) {
	a.txReadAccessMap[key] = struct{}{}
}

// GetReadWriteSet returns a TxReadWriteSet for the requested transaction index.
func (a *txAccessTrackerMap) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	readDescs := make([]blockstm.ReadDescriptor, 0, len(a.txReadAccessMap))
	writeDescs := make([]blockstm.WriteDescriptor, 0, len(a.txWriteAccessMap))

	for k := range a.txReadAccessMap {
		readDescs = append(readDescs, blockstm.ReadDescriptor{
			Path: blockstm.Key(k),
		})
	}

	for k := range a.txWriteAccessMap {
		writeDescs = append(writeDescs, blockstm.WriteDescriptor{
			Path: blockstm.Key(k),
		})
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

func (e *txAccessTrackerNoOp) Clear() {}

func (e *txAccessTrackerNoOp) AddWrite(_ Key, _ any) {}

func (e *txAccessTrackerNoOp) AddRead(_ Key) {}

func (e *txAccessTrackerNoOp) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	return blockstm.TxReadWriteSet{Index: txIndx}
}

func TxAccessTrackerFactory(isNoOp bool) ITxAccessTracker {
	if isNoOp {
		return &txAccessTrackerNoOp{}
	}

	return &txAccessTrackerMap{}
}
