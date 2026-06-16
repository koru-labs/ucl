package state

import (
	"github.com/0xPolygon/polygon-edge/consensus/ibft/blockstm"
	"github.com/0xPolygon/polygon-edge/types"
)

type ITxAccessTracker interface {
	Clear(writesOnly bool)
	AddWrite(addr types.Address, hash types.Hash, subpath byte, value any)
	AddRead(addr types.Address, hash types.Hash, subpath byte)
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

// AddWrite records a write access for a given key. The value is ignored here.
func (a *txAccessTrackerMap) AddWrite(addr types.Address, hash types.Hash, subpath byte, _ any) {
	key := NewGenericKey(addr, hash, subpath)

	a.txWriteAccessMap[key] = struct{}{}
}

// AddRead records a read access for a given key.
func (a *txAccessTrackerMap) AddRead(addr types.Address, hash types.Hash, subpath byte) {
	key := NewGenericKey(addr, hash, subpath)

	a.txReadAccessMap[key] = struct{}{}
}

// GetReadWriteSet returns a TxReadWriteSet for the requested transaction index.
func (a *txAccessTrackerMap) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	readDescs := ([]blockstm.ReadDescriptor)(nil)
	writeDescs := ([]blockstm.WriteDescriptor)(nil)

	if len(a.txReadAccessMap) > 0 {
		readDescs = make([]blockstm.ReadDescriptor, 0, len(a.txReadAccessMap))

		for k := range a.txReadAccessMap {
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

func (a *txAccessTrackerNoOp) AddWrite(addr types.Address, hash types.Hash, subpath byte, _ any) {
}

func (a *txAccessTrackerNoOp) AddRead(addr types.Address, hash types.Hash, subpath byte) {
}

func (e *txAccessTrackerNoOp) GetReadWriteSet(txIndx int) blockstm.TxReadWriteSet {
	return blockstm.TxReadWriteSet{Index: txIndx}
}

func TxAccessTrackerFactory(isNoOp bool) ITxAccessTracker {
	if isNoOp {
		return &txAccessTrackerNoOp{}
	}

	return &txAccessTrackerMap{}
}
