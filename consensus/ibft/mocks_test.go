package ibft

import (
	"time"

	"github.com/0xPolygon/polygon-edge/consensus/ibft/fork"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/helper/progress"
	"github.com/0xPolygon/polygon-edge/syncer"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
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

var _ forkManagerInterface = (*forkManagerMock)(nil)

type forkManagerMock struct {
	mock.Mock
}

func (fm *forkManagerMock) Initialize() error {
	return nil
}

func (fm *forkManagerMock) Close() error {
	return nil
}

func (fm *forkManagerMock) GetSigner(height uint64) (signer.Signer, error) {
	args := fm.Called(height)

	return args.Get(0).(signer.Signer), nil
}

func (fm *forkManagerMock) GetValidatorStore(height uint64) (fork.ValidatorStore, error) {
	args := fm.Called(height)

	return args.Get(0).(fork.ValidatorStore), nil
}

func (fm *forkManagerMock) GetValidators(height uint64) (validators.Validators, error) {
	args := fm.Called(height)

	return args.Get(0).(validators.Validators), nil
}

func (fm *forkManagerMock) GetHooks(height uint64) fork.HooksInterface {
	args := fm.Called(height)

	return args.Get(0).(fork.HooksInterface)
}

var _ syncer.Syncer = (*syncerMock)(nil)

type syncerMock struct {
	mock.Mock
}

func (tp *syncerMock) Start() error {
	args := tp.Called()

	return args.Error(0)
}

func (tp *syncerMock) Close() error {
	args := tp.Called()

	return args.Error(0)
}

func (tp *syncerMock) GetSyncProgression() *progress.Progression {
	args := tp.Called()

	return args[0].(*progress.Progression)
}

func (tp *syncerMock) HasSyncPeer() bool {
	args := tp.Called()

	return args[0].(bool)
}

func (tp *syncerMock) Sync(func(*types.FullBlock) bool) error {
	args := tp.Called()

	return args.Error(0)
}

func (tp *syncerMock) UpdateBlockTimeout(time.Duration) {
	tp.Called()
}

func (tp *syncerMock) SyncTxPool() error {
	args := tp.Called()

	return args.Error(0)
}
