package state

import (
	"math/big"

	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/types/bal"
)

// concreteBALRecorder wraps a ConstructionAccountAccess-backed list and
// the access index for the current execution phase (0, 1..n, or n+1).
type concreteBALRecorder struct {
	bal   *bal.ConstructionBlockAccessList
	index uint32
}

func NewBALRecorder(b *bal.ConstructionBlockAccessList, index uint32) runtime.BALRecorder {
	return &concreteBALRecorder{bal: b, index: index}
}

func (r *concreteBALRecorder) getOrCreate(addr types.Address) *bal.ConstructionAccountAccess {
	return r.bal.GetOrCreate(addr)
}

func (r *concreteBALRecorder) AccountRead(addr types.Address) {
	r.getOrCreate(addr) // touch only, no-op if already present
}

func (r *concreteBALRecorder) StorageRead(addr types.Address, slot types.Hash) {
	r.getOrCreate(addr).RecordStorageRead(slot)
}

func (r *concreteBALRecorder) StorageWrite(addr types.Address, slot, val types.Hash) {
	r.getOrCreate(addr).RecordStorageWrite(r.index, slot, val)
}

func (r *concreteBALRecorder) BalanceChange(addr types.Address, balance *big.Int) {
	r.getOrCreate(addr).RecordBalanceChange(r.index, balance)
}

func (r *concreteBALRecorder) NonceChange(addr types.Address, nonce uint64) {
	r.getOrCreate(addr).RecordNonceChange(r.index, nonce)
}

func (r *concreteBALRecorder) CodeChange(addr types.Address, code []byte) {
	r.getOrCreate(addr).RecordCodeChange(r.index, code)
}

var _ runtime.BALRecorder = &concreteBALRecorder{}
