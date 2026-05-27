package ibft

import (
	"google.golang.org/protobuf/proto"

	protoIBFT "github.com/0xPolygon/go-ibft/messages/proto"
)

func (i *backendIBFT) signMessage(msg *protoIBFT.IbftMessage) *protoIBFT.IbftMessage {
	view := msg.GetView()

	raw, err := proto.Marshal(msg)
	if err != nil {
		i.logger.Error(
			"failed to marshal IBFT message for signing",
			"type", msg.GetType().String(),
			"height", view.GetHeight(),
			"round", view.GetRound(),
			"err", err,
		)

		return nil
	}

	pending := i.blockchain.Header().Number + 1

	signer, err := i.forkManager.GetSigner(pending)
	if err != nil {
		i.logger.Error("cannot get signer from fork manager for", "block number", pending, "err", err)

		return nil
	}

	if msg.Signature, err = signer.SignIBFTMessage(raw); err != nil {
		i.logger.Error(
			"failed to sign IBFT message",
			"type", msg.GetType().String(),
			"height", view.GetHeight(),
			"round", view.GetRound(),
			"err", err,
		)

		return nil
	}

	return msg
}

func (i *backendIBFT) BuildPrePrepareMessage(
	rawProposal []byte,
	certificate *protoIBFT.RoundChangeCertificate,
	view *protoIBFT.View,
) *protoIBFT.IbftMessage {
	proposedBlock := &protoIBFT.Proposal{
		RawProposal: rawProposal,
		Round:       view.Round,
	}

	// hash calculation begins
	proposalHash, err := i.calculateProposalHashFromBlockBytes(rawProposal, &view.Round)
	if err != nil {
		i.logger.Error(
			"failed to calculate proposal hash for PREPREPARE",
			"height", view.GetHeight(),
			"round", view.GetRound(),
			"err", err,
		)

		return nil
	}

	msg := &protoIBFT.IbftMessage{
		View: view,
		From: i.ID(),
		Type: protoIBFT.MessageType_PREPREPARE,
		Payload: &protoIBFT.IbftMessage_PreprepareData{
			PreprepareData: &protoIBFT.PrePrepareMessage{
				Proposal:     proposedBlock,
				ProposalHash: proposalHash.Bytes(),
				Certificate:  certificate,
			},
		},
	}

	retVal := i.signMessage(msg)
	i.logger.Debug("PrePrepareMsg", "signature length", len(msg.Signature))
	i.logger.Debug("PrePrepareMsg", "rawProposal length", len(rawProposal))
	return retVal
	//return i.signMessage(msg)
}

func (i *backendIBFT) BuildPrepareMessage(proposalHash []byte, view *protoIBFT.View) *protoIBFT.IbftMessage {
	msg := &protoIBFT.IbftMessage{
		View: view,
		From: i.ID(),
		Type: protoIBFT.MessageType_PREPARE,
		Payload: &protoIBFT.IbftMessage_PrepareData{
			PrepareData: &protoIBFT.PrepareMessage{
				ProposalHash: proposalHash,
			},
		},
	}

	return i.signMessage(msg)
}

func (i *backendIBFT) BuildCommitMessage(proposalHash []byte, view *protoIBFT.View) *protoIBFT.IbftMessage {
	pending := i.blockchain.Header().Number + 1

	signer, err := i.forkManager.GetSigner(pending)
	if err != nil {
		i.logger.Error("cannot get signer from fork manager for", "block number", pending, "err", err)

		return nil
	}

	committedSeal, err := signer.CreateCommittedSeal(proposalHash)
	if err != nil {
		i.logger.Error("Unable to build commit message, %v", err)

		return nil
	}

	msg := &protoIBFT.IbftMessage{
		View: view,
		From: i.ID(),
		Type: protoIBFT.MessageType_COMMIT,
		Payload: &protoIBFT.IbftMessage_CommitData{
			CommitData: &protoIBFT.CommitMessage{
				ProposalHash:  proposalHash,
				CommittedSeal: committedSeal,
			},
		},
	}

	return i.signMessage(msg)
}

func (i *backendIBFT) BuildRoundChangeMessage(
	proposal *protoIBFT.Proposal,
	certificate *protoIBFT.PreparedCertificate,
	view *protoIBFT.View,
) *protoIBFT.IbftMessage {
	msg := &protoIBFT.IbftMessage{
		View: view,
		From: i.ID(),
		Type: protoIBFT.MessageType_ROUND_CHANGE,
		Payload: &protoIBFT.IbftMessage_RoundChangeData{RoundChangeData: &protoIBFT.RoundChangeMessage{
			LastPreparedProposal:      proposal,
			LatestPreparedCertificate: certificate,
		}},
	}

	return i.signMessage(msg)
}
