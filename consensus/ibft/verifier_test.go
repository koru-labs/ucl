package ibft

import (
	"testing"

	"github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestBackendIBFT_IsValidValidator(t *testing.T) {
	t.Parallel()

	validators := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(types.StringToAddress("1")),
		validators.NewECDSAValidator(types.StringToAddress("2")),
	)

	signer := &signerMock{}

	forkManagerMock := &forkManagerMock{}
	forkManagerMock.On("GetValidators", mock.Anything).Return(validators)
	forkManagerMock.On("GetSigner", mock.Anything).Return(signer)

	i := &backendIBFT{
		forkManager: forkManagerMock,
		logger:      hclog.NewNullLogger(),
	}

	cases := []struct {
		name          string
		signerAddress types.Address
		senderAddress types.Address
		isValidSender bool
	}{
		{
			name:          "Valid sender",
			signerAddress: types.StringToAddress("1"),
			senderAddress: types.StringToAddress("1"),
			isValidSender: true,
		},
		{
			name:          "Sender not amongst current validators",
			signerAddress: types.StringToAddress("3"),
			senderAddress: types.StringToAddress("3"),
			isValidSender: false,
		},
		{
			name:          "Sender and signer accounts mismatch",
			signerAddress: types.StringToAddress("1"),
			senderAddress: types.StringToAddress("2"),
			isValidSender: false,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			signer.On("EcrecoverFromIBFTMessage", mock.Anything, mock.Anything).Return(c.signerAddress, nil).Once()
			msg := &proto.IbftMessage{From: c.senderAddress.Bytes(), View: &proto.View{Height: 1, Round: 0}}
			require.Equal(t, c.isValidSender, i.IsValidValidator(msg))
		})
	}
}

func TestBackendIBFT_IsValidProposalHash(t *testing.T) {
	t.Parallel()

	expectedProposalHash := types.StringToHash("0x527afa4dbd31ac76d013a0d46897ab109ab020a78b5c30ee6a05e5f1032f75ad")

	validators := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(types.StringToAddress("1")),
		validators.NewECDSAValidator(types.StringToAddress("2")),
	)

	round := uint64(0)
	extra := &signer.IstanbulExtra{
		Validators:     validators,
		CommittedSeals: &signer.AggregatedSeal{},
		RoundNumber:    &round,
		TxDependency:   [][]uint64{{1, 2, 5}, {}, {4}, {}, {3}},
	}

	block := &types.Block{
		Header: &types.Header{
			Number:    10,
			ExtraData: extra.MarshalRLPTo(nil),
		},
	}
	block.Header.ComputeHash()

	signer := &signerMock{}

	forkManagerMock := &forkManagerMock{}
	forkManagerMock.On("GetSigner", mock.Anything).Return(signer)

	i := &backendIBFT{
		forkManager: forkManagerMock,
	}

	cases := []struct {
		name     string
		expected bool
		round    uint64
	}{
		{
			name:     "Valid",
			expected: true,
			round:    0,
		},
		{
			name:     "Invalid",
			expected: false,
			round:    1,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.expected,
				i.IsValidProposalHash(
					&proto.Proposal{RawProposal: block.MarshalRLP(), Round: c.round},
					expectedProposalHash.Bytes()))
		})
	}
}
