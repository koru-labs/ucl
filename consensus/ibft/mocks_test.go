package ibft

import (
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/mock"
)

var _ txPoolInterface = (*txPoolMock)(nil)

type txPoolMock struct {
	mock.Mock
}

func (tp *txPoolMock) Prepare() {
	tp.Called()
}

func (tp *txPoolMock) Length() uint64 {
	args := tp.Called()

	return args[0].(uint64)
}

func (tp *txPoolMock) Peek() *types.Transaction {
	args := tp.Called()

	return args[0].(*types.Transaction)
}

func (tp *txPoolMock) Pop(tx *types.Transaction) {
	tp.Called(tx)
}

func (tp *txPoolMock) Drop(tx *types.Transaction) {
	tp.Called(tx)
}

func (tp *txPoolMock) Demote(tx *types.Transaction) {
	tp.Called(tx)
}

func (tp *txPoolMock) SetSealing(v bool) {
	tp.Called(v)
}

func (tp *txPoolMock) ResetWithBlock(fullBlock *types.Block) {
	tp.Called(fullBlock)
}

func (tp *txPoolMock) ReinsertProposed() {
	tp.Called()
}

func (tp *txPoolMock) ClearProposed() {
	tp.Called()
}
