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

func (s *syncerMock) Start() error {
	args := s.Called()

	return args.Error(0)
}

func (s *syncerMock) Close() error {
	args := s.Called()

	return args.Error(0)
}

func (s *syncerMock) GetSyncProgression() *progress.Progression {
	args := s.Called()

	return args[0].(*progress.Progression)
}

func (s *syncerMock) HasSyncPeer() bool {
	args := s.Called()

	return args[0].(bool)
}

func (s *syncerMock) Sync(func(*types.FullBlock) bool) error {
	args := s.Called()

	return args.Error(0)
}

func (s *syncerMock) UpdateBlockTimeout(time.Duration) {
	s.Called()
}

func (s *syncerMock) SyncTxPool() error {
	args := s.Called()

	return args.Error(0)
}

var _ signer.Signer = (*signerMock)(nil)

type signerMock struct {
	mock.Mock
}

func (s *signerMock) Type() validators.ValidatorType {
	args := s.Called()

	return args[0].(validators.ValidatorType)
}

func (s *signerMock) Address() types.Address {
	args := s.Called()

	return args[0].(types.Address)
}

func (s *signerMock) InitIBFTExtra(*types.Header, validators.Validators, signer.Seals) {
	s.Called()
}

func (s *signerMock) GetIBFTExtra(*types.Header) (*signer.IstanbulExtra, error) {
	args := s.Called()

	return args[0].(*signer.IstanbulExtra), args.Error(1)
}

func (s *signerMock) GetValidators(*types.Header) (validators.Validators, error) {
	args := s.Called()

	return args[0].(validators.Validators), args.Error(1)
}

func (s *signerMock) WriteProposerSeal(*types.Header) (*types.Header, error) {
	args := s.Called()

	return args[0].(*types.Header), args.Error(1)
}

func (s *signerMock) EcrecoverFromHeader(*types.Header) (types.Address, error) {
	args := s.Called()

	return args[0].(types.Address), args.Error(1)
}

func (s *signerMock) CreateCommittedSeal([]byte) ([]byte, error) {
	args := s.Called()

	return args[0].([]byte), args.Error(1)
}

func (s *signerMock) VerifyCommittedSeal(validators.Validators, types.Address, []byte, []byte) error {
	args := s.Called()

	return args.Error(0)
}

func (s *signerMock) WriteCommittedSeals(
	header *types.Header,
	roundNumber uint64,
	sealMap map[types.Address][]byte,
) (*types.Header, error) {
	args := s.Called()

	return args[0].(*types.Header), args.Error(1)
}

func (s *signerMock) VerifyCommittedSeals(
	hash types.Hash,
	committedSeals signer.Seals,
	validators validators.Validators,
	quorumSize int,
) error {
	args := s.Called()

	return args.Error(0)
}

func (s *signerMock) VerifyParentCommittedSeals(
	parentHash types.Hash,
	header *types.Header,
	parentValidators validators.Validators,
	quorum int,
	mustExist bool,
) error {
	args := s.Called()

	return args.Error(0)
}

func (s *signerMock) SignIBFTMessage([]byte) ([]byte, error) {
	args := s.Called()

	return args[0].([]byte), args.Error(1)
}

func (s *signerMock) EcrecoverFromIBFTMessage([]byte, []byte) (types.Address, error) {
	args := s.Called()

	return args[0].(types.Address), args.Error(1)
}

func (s *signerMock) CalculateHeaderHash(*types.Header) (types.Hash, error) {
	args := s.Called()

	return args[0].(types.Hash), args.Error(1)
}

func (s *signerMock) FilterHeaderForHash(*types.Header) (*types.Header, error) {
	args := s.Called()

	return args[0].(*types.Header), args.Error(1)
}
