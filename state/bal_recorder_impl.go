package state

import (
	"math/big"

	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/types/bal"
)

type BlockAccessListRecorder struct {
	bal   *bal.BlockAccessListRecord
	index uint64
}

func NewBlockAccessListRecorder(b *bal.BlockAccessListRecord, index uint64) runtime.BlockAccessListRecorder {
	return &BlockAccessListRecorder{bal: b, index: index}
}

func (r *BlockAccessListRecorder) getOrCreate(addr types.Address) *bal.AccountAccessRecord {
	return r.bal.GetOrCreate(addr)
}

func (r *BlockAccessListRecorder) AccountRead(addr types.Address) {
	r.getOrCreate(addr) // touch only, no-op if already present
}

func (r *BlockAccessListRecorder) StorageWrite(addr types.Address, slot, val types.Hash) {
	r.getOrCreate(addr).RecordStorageWrite(r.index, slot, val)
}

func (r *BlockAccessListRecorder) BalanceChange(addr types.Address, balance *big.Int) {
	r.getOrCreate(addr).RecordBalanceChange(r.index, balance)
}

func (r *BlockAccessListRecorder) NonceChange(addr types.Address, nonce uint64) {
	r.getOrCreate(addr).RecordNonceChange(r.index, nonce)
}

func (r *BlockAccessListRecorder) CodeChange(addr types.Address, code []byte) {
	r.getOrCreate(addr).RecordCodeChange(r.index, code)
}

func (r *BlockAccessListRecorder) Merge(balRecorder runtime.BlockAccessListRecorder) {
	switch b := balRecorder.(type) {
	case *BlockAccessListRecorder:
		r.bal.Merge(b.bal)
	default:
	}
}

func (r *BlockAccessListRecorder) GetIndex() uint64 {
	return r.index
}

func (r *BlockAccessListRecorder) GetBlockAccessListRecord() *bal.BlockAccessListRecord {
	return r.bal
}

var _ runtime.BlockAccessListRecorder = &BlockAccessListRecorder{}
