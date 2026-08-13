package syncer

import (
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/umbracle/fastrlp"
)

type mockForkManager struct{}

func (m *mockForkManager) GetSigner(blockNumber uint64) (signer.Signer, error) {
	return &mockSigner{}, nil
}

type mockValidator struct{}

func (m *mockForkManager) GetValidators(blockNumber uint64) (validators.Validators, error) {
	return &mockValidators{}, nil
}

type mockValidators struct{}

func (*mockValidators) Type() validators.ValidatorType                         { return "mock" }
func (*mockValidators) Len() int                                               { return 0 }
func (*mockValidators) Equal(validators.Validators) bool                       { return false }
func (*mockValidators) Copy() validators.Validators                            { return nil }
func (*mockValidators) At(uint64) validators.Validator                         { return nil }
func (*mockValidators) Index(types.Address) int64                              { return 0 }
func (*mockValidators) Includes(types.Address) bool                            { return false }
func (*mockValidators) Add(validators.Validator) error                         { return nil }
func (*mockValidators) Del(validators.Validator) error                         { return nil }
func (*mockValidators) Merge(validators.Validators) error                      { return nil }
func (*mockValidators) MarshalRLPWith(*fastrlp.Arena) *fastrlp.Value           { return nil }
func (*mockValidators) UnmarshalRLPFrom(*fastrlp.Parser, *fastrlp.Value) error { return nil }

type mockSigner struct{}

func (*mockSigner) Type() validators.ValidatorType                                   { return "mock" }
func (*mockSigner) Address() types.Address                                           { return types.Address{} }
func (*mockSigner) InitIBFTExtra(*types.Header, validators.Validators, signer.Seals) {}
func (*mockSigner) GetIBFTExtra(*types.Header) (*signer.IstanbulExtra, error)        { return nil, nil }
func (*mockSigner) GetValidators(*types.Header) (validators.Validators, error)       { return nil, nil }
func (*mockSigner) WriteProposerSeal(h *types.Header) (*types.Header, error)         { return h, nil }
func (*mockSigner) EcrecoverFromHeader(*types.Header) (types.Address, error) {
	return types.Address{}, nil
}
func (*mockSigner) CreateCommittedSeal([]byte) ([]byte, error) { return nil, nil }
func (*mockSigner) VerifyCommittedSeal(validators.Validators, types.Address, []byte, []byte) error {
	return nil
}
func (*mockSigner) WriteCommittedSeals(h *types.Header, _ uint64, _ map[types.Address][]byte) (*types.Header, error) {
	return h, nil
}
func (*mockSigner) VerifyCommittedSeals(_ types.Hash, _ signer.Seals, _ validators.Validators, _ int) error {
	return nil
}
func (*mockSigner) VerifyParentCommittedSeals(_ types.Hash, _ *types.Header, _ validators.Validators, _ int, _ bool) error {
	return nil
}

func (*mockSigner) SignIBFTMessage([]byte) ([]byte, error) { return nil, nil }
func (*mockSigner) EcrecoverFromIBFTMessage([]byte, []byte) (types.Address, error) {
	return types.Address{}, nil
}
func (*mockSigner) CalculateHeaderHash(*types.Header) (types.Hash, error)      { return types.Hash{}, nil }
func (*mockSigner) FilterHeaderForHash(h *types.Header) (*types.Header, error) { return h, nil }
