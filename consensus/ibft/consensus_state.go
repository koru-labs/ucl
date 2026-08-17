package ibft

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/0xPolygon/go-ibft/core"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/types"
)

var (
	errConsensusNotInitialized = errors.New("ibft consensus is not initialized")

	_ consensus.ConsensusStateProvider      = (*backendIBFT)(nil)
	_ consensus.ConsensusStateEventProvider = (*backendIBFT)(nil)
)

// GetConsensusState returns this node's live IBFT consensus snapshot.
func (i *backendIBFT) GetConsensusState() (*consensus.ConsensusState, error) {
	if i.consensus == nil || i.consensus.IBFT == nil {
		return nil, errConsensusNotInitialized
	}

	snap := i.consensus.TryGetConsensusState()
	if snap == nil {
		return nil, errors.New("failed to capture consensus state")
	}

	return convertConsensusState(snap), nil
}

// ConsensusStateEvents exposes go-ibft's capacity-one diagnostics wakeup
// channel. Reading the snapshot and performing I/O remain outside consensus.
func (i *backendIBFT) ConsensusStateEvents() <-chan struct{} {
	if i.consensus == nil || i.consensus.IBFT == nil {
		return nil
	}

	return i.consensus.DiagnosticsEvents()
}

func convertConsensusState(snap *core.ConsensusState) *consensus.ConsensusState {
	out := &consensus.ConsensusState{
		CapturedAt:          formatTime(snap.CapturedAt),
		Complete:            snap.Complete,
		UnavailableSections: append([]string(nil), snap.UnavailableSections...),
		NodeID:              encodeHex(snap.NodeID),
		Current:             convertHeightState(snap.Current, snap.CapturedAt, true),
		LastFinalized:       convertHeightArchive(snap.LastFinalized),
	}

	return out
}

func convertHeightState(h *core.HeightState, capturedAt time.Time, includeLiveTiming bool) *consensus.HeightState {
	if h == nil {
		return nil
	}

	out := &consensus.HeightState{
		Status:                    string(h.Status),
		Height:                    h.Height,
		Round:                     h.Round,
		Phase:                     h.Phase,
		RoundStarted:              h.RoundStarted,
		LastRoundEndReason:        string(h.LastRoundEndReason),
		SequenceStartedAt:         formatTime(h.SequenceStartedAt),
		RoundStartedAt:            formatTime(h.RoundStartedAt),
		PhaseStartedAt:            formatTime(h.PhaseStartedAt),
		CompletedPhaseDurationsMs: h.CompletedPhaseDurationsMs,
		IsProposer:                h.IsProposer,
		Proposer:                  encodeHex(h.Proposer),
		TotalVotingPower:          h.TotalVotingPower,
		QuorumSize:                h.QuorumSize,
		Proposal:                  convertProposal(h.Proposal),
	}

	if includeLiveTiming {
		out.RoundTimeoutMs = h.RoundTimeout.Milliseconds()
		out.RoundDeadline = formatTime(h.RoundDeadline)
		out.RoundRemainingMs = roundRemainingMs(capturedAt, h.RoundDeadline)
		out.PhaseElapsedMs = h.PhaseElapsedMs
		out.SequenceElapsedMs = h.SequenceElapsedMs
		out.RoundElapsedMs = h.RoundElapsedMs
	}

	out.RoundHistory = convertRoundHistory(h.RoundHistory)
	out.PhaseSnapshots = convertPhaseSnapshots(h.PhaseSnapshots)
	out.Validators = convertValidators(h.Validators)
	out.Quorum = convertQuorum(h.Quorum)
	out.Messages = convertMessages(h.Messages)
	out.LatestPC = convertPreparedCertificate(h.LatestPC)
	out.CommittedSeals = convertCommittedSeals(h.CommittedSeals)

	return out
}

func convertHeightArchive(h *core.HeightArchive) *consensus.HeightState {
	if h == nil {
		return nil
	}

	out := &consensus.HeightState{
		Status:             string(h.Status),
		Height:             h.Height,
		Round:              h.Round,
		Phase:              h.Phase,
		RoundStarted:       h.RoundStarted,
		LastRoundEndReason: string(h.LastRoundEndReason),
		SequenceStartedAt:  formatTime(h.SequenceStartedAt),
		SequenceEndedAt:    formatTime(h.SequenceEndedAt),
		RoundStartedAt:     formatTime(h.RoundStartedAt),
		PhaseStartedAt:     formatTime(h.PhaseStartedAt),
		IsProposer:         h.IsProposer,
		Proposer:           encodeHex(h.Proposer),
		TotalVotingPower:   h.TotalVotingPower,
		QuorumSize:         h.QuorumSize,
		Proposal:           convertProposal(h.Proposal),
		RoundHistory:       convertRoundHistory(h.RoundHistory),
		PhaseSnapshots:     convertPhaseSnapshots(h.PhaseSnapshots),
		Validators:         convertValidators(h.Validators),
		LatestPC:           convertPreparedCertificate(h.LatestPC),
		CommittedSeals:     convertCommittedSeals(h.CommittedSeals),
	}

	return out
}

func convertPhaseSnapshots(in []core.PhaseSnapshot) []consensus.PhaseSnapshot {
	if len(in) == 0 {
		return nil
	}

	out := make([]consensus.PhaseSnapshot, 0, len(in))
	for _, p := range in {
		out = append(out, consensus.PhaseSnapshot{
			Phase:      p.Phase,
			Status:     p.Status,
			Height:     p.Height,
			Round:      p.Round,
			StartedAt:  formatTime(p.StartedAt),
			EndedAt:    formatTime(p.EndedAt),
			DurationMs: p.DurationMs,
			Proposer:   encodeHex(p.Proposer),
			Proposal:   convertProposal(p.Proposal),
			Quorum:     convertQuorum(p.Quorum),
			Messages:   convertMessages(p.Messages),
			LatestPC:   convertPreparedCertificate(p.LatestPC),
		})
	}

	return out
}

func convertRoundHistory(in []core.RoundSummary) []consensus.RoundSummary {
	if len(in) == 0 {
		return nil
	}

	out := make([]consensus.RoundSummary, 0, len(in))
	for _, r := range in {
		out = append(out, consensus.RoundSummary{
			Round:            r.Round,
			EndReason:        string(r.EndReason),
			StartedAt:        formatTime(r.StartedAt),
			EndedAt:          formatTime(r.EndedAt),
			DurationMs:       r.DurationMs,
			PhaseDurationsMs: r.PhaseDurationsMs,
		})
	}

	return out
}

func convertValidators(in []core.ValidatorSnapshot) []consensus.ValidatorInfo {
	if len(in) == 0 {
		return nil
	}

	out := make([]consensus.ValidatorInfo, 0, len(in))
	for _, v := range in {
		out = append(out, consensus.ValidatorInfo{
			ID:          encodeHex(v.ID),
			VotingPower: v.VotingPower,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})

	return out
}

func convertQuorum(in map[string]core.QuorumProgress) map[string]consensus.QuorumInfo {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]consensus.QuorumInfo, len(in))
	for k, q := range in {
		out[k] = consensus.QuorumInfo{
			Available:     q.Available,
			Count:         q.Count,
			ReceivedPower: q.ReceivedPower,
			RequiredPower: q.RequiredPower,
			HasQuorum:     q.HasQuorum,
		}
	}

	return out
}

func convertMessages(in map[string]core.MessageTypeSnapshot) map[string]consensus.MessageGroup {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]consensus.MessageGroup, len(in))
	for k, g := range in {
		group := consensus.MessageGroup{
			Available:  g.Available,
			Truncated:  g.Truncated,
			ViewHeight: g.ViewHeight,
			ViewRound:  g.ViewRound,
			Count:      len(g.Messages),
		}
		if len(g.Messages) > 0 {
			group.Messages = make([]consensus.MessageInfo, 0, len(g.Messages))
			for _, m := range g.Messages {
				group.Messages = append(group.Messages, consensus.MessageInfo{
					From:          encodeHex(m.From),
					Type:          m.Type,
					Height:        m.Height,
					Round:         m.Round,
					ProposalHash:  encodeHex(m.ProposalHash),
					Signature:     encodeHex(m.Signature),
					CommittedSeal: encodeHex(m.CommittedSeal),
				})
			}
		}

		out[k] = group
	}

	return out
}

func convertPreparedCertificate(in *core.PreparedCertificateSnapshot) *consensus.PreparedCertificateInfo {
	if in == nil {
		return nil
	}

	senders := make([]string, 0, len(in.PrepareSenders))
	for _, s := range in.PrepareSenders {
		senders = append(senders, encodeHex(s))
	}

	return &consensus.PreparedCertificateInfo{
		Available:      in.Available,
		ProposalHash:   encodeHex(in.ProposalHash),
		PrepareCount:   in.PrepareCount,
		ProposalFrom:   encodeHex(in.ProposalFrom),
		PrepareSenders: senders,
	}
}

func convertCommittedSeals(in []core.CommittedSealSnapshot) []consensus.CommittedSealInfo {
	if len(in) == 0 {
		return nil
	}

	out := make([]consensus.CommittedSealInfo, 0, len(in))
	for _, s := range in {
		out = append(out, consensus.CommittedSealInfo{
			Signer:    encodeHex(s.Signer),
			Signature: encodeHex(s.Signature),
		})
	}

	return out
}

func convertProposal(p *core.ProposalSnapshot) *consensus.ProposalInfo {
	if p == nil {
		return &consensus.ProposalInfo{Available: false}
	}

	out := &consensus.ProposalInfo{
		Available: p.Available,
		Hash:      encodeHex(p.Hash),
		Round:     p.Round,
		RawSize:   p.RawSize,
	}
	if !p.Available || len(p.RawProposal) == 0 {
		return out
	}

	block := &types.Block{}
	if err := block.UnmarshalRLP(p.RawProposal); err != nil {
		out.DecodeError = fmt.Sprintf("failed to decode proposal: %v", err)

		return out
	}

	if block.Header != nil {
		if block.Header.Hash == types.ZeroHash {
			block.Header.ComputeHash()
		}

		out.BlockHash = block.Hash().String()
		out.BlockNumber = block.Number()
		out.GasLimit = block.Header.GasLimit
		out.GasUsed = block.Header.GasUsed
		out.Timestamp = block.Header.Timestamp
	}

	out.TxCount = len(block.Transactions)

	return out
}

func encodeHex(b []byte) string {
	if len(b) == 0 {
		return ""
	}

	return "0x" + hex.EncodeToString(b)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339Nano)
}

func roundRemainingMs(capturedAt, deadline time.Time) int64 {
	if deadline.IsZero() || capturedAt.IsZero() {
		return 0
	}

	remaining := deadline.Sub(capturedAt).Milliseconds()
	if remaining < 0 {
		return 0
	}

	return remaining
}
