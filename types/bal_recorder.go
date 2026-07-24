package types

import (
	"math/big"
)

type BlockAccessListRecorderImpl struct {
	bal   BlockAccessRuntime
	index uint64
}

func NewBlockAccessListRecorder(b BlockAccessRuntime, index uint64) *BlockAccessListRecorderImpl {
	return &BlockAccessListRecorderImpl{bal: b, index: index}
}

func (r *BlockAccessListRecorderImpl) getOrCreate(addr Address) *AccountAccessRuntime {
	return r.bal.GetOrCreate(addr)
}

func (r *BlockAccessListRecorderImpl) AccountRead(addr Address) {
	r.getOrCreate(addr) // touch only, no-op if already present
}

func (r *BlockAccessListRecorderImpl) StorageWrite(addr Address, slot, val Hash) {
	r.getOrCreate(addr).RecordStorageChange(r.index, slot, val)
}

func (r *BlockAccessListRecorderImpl) BalanceChange(addr Address, balance *big.Int) {
	r.getOrCreate(addr).RecordBalanceChange(r.index, balance)
}

func (r *BlockAccessListRecorderImpl) NonceChange(addr Address, nonce uint64) {
	r.getOrCreate(addr).RecordNonceChange(r.index, nonce)
}

func (r *BlockAccessListRecorderImpl) CodeChange(addr Address, code []byte) {
	r.getOrCreate(addr).RecordCodeChange(r.index, code)
}

func (r *BlockAccessListRecorderImpl) Merge(balRecorder BlockAccessListRecorder) {
	switch b := balRecorder.(type) {
	case *BlockAccessListRecorderImpl:
		r.bal.Merge(b.bal)
	default:
	}
}

func (r *BlockAccessListRecorderImpl) GetIndex() uint64 {
	return r.index
}

func (r *BlockAccessListRecorderImpl) GetBlockAccessListRecord() BlockAccessRuntime {
	return r.bal
}

// BlockAccessListRecorder captures EIP-7928 block access list entries as the EVM
// executes opcodes that read or write account/storage state.
type BlockAccessListRecorder interface {
	AccountRead(addr Address)
	StorageWrite(addr Address, slot Hash, val Hash)
	BalanceChange(addr Address, balance *big.Int)
	NonceChange(addr Address, nonce uint64)
	CodeChange(addr Address, code []byte)
	Merge(balRecorder BlockAccessListRecorder)
	GetIndex() uint64
	GetBlockAccessListRecord() BlockAccessRuntime
}

// NoopBALRecorder is used for blocks prior to the BAL fork activation, so
// opcode handlers never need fork-awareness.
type NoopBALRecorder struct{}

func (NoopBALRecorder) AccountRead(Address)                          {}
func (NoopBALRecorder) StorageWrite(Address, Hash, Hash)             {}
func (NoopBALRecorder) BalanceChange(Address, *big.Int)              {}
func (NoopBALRecorder) NonceChange(Address, uint64)                  {}
func (NoopBALRecorder) CodeChange(Address, []byte)                   {}
func (NoopBALRecorder) Merge(BlockAccessListRecorderImpl)            {}
func (NoopBALRecorder) GetIndex() uint64                             { return 0 }
func (NoopBALRecorder) GetBlockAccessListRecord() BlockAccessRuntime { return nil }
