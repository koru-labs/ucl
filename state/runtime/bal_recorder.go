package runtime

import (
	"math/big"

	"github.com/0xPolygon/polygon-edge/types"
)

// BALRecorder captures EIP-7928 block access list entries as the EVM
// executes opcodes that read or write account/storage state.
type BALRecorder interface {
	AccountRead(addr types.Address)
	StorageRead(addr types.Address, slot types.Hash)
	StorageWrite(addr types.Address, slot types.Hash, val types.Hash)
	BalanceChange(addr types.Address, balance *big.Int)
	NonceChange(addr types.Address, nonce uint64)
	CodeChange(addr types.Address, code []byte)
}

// NoopBALRecorder is used for blocks prior to the BAL fork activation, so
// opcode handlers never need fork-awareness.
type NoopBALRecorder struct{}

func (NoopBALRecorder) AccountRead(types.Address)                          {}
func (NoopBALRecorder) StorageRead(types.Address, types.Hash)              {}
func (NoopBALRecorder) StorageWrite(types.Address, types.Hash, types.Hash) {}
func (NoopBALRecorder) BalanceChange(types.Address, *big.Int)              {}
func (NoopBALRecorder) NonceChange(types.Address, uint64)                  {}
func (NoopBALRecorder) CodeChange(types.Address, []byte)                   {}

var _ BALRecorder = NoopBALRecorder{}
