package consensus

// ConsensusStateProvider is optionally implemented by consensus engines that can
// expose a live consensus snapshot for debugging.
type ConsensusStateProvider interface {
	// GetConsensusState returns this node's current view of consensus.
	// Implementations must be non-blocking with respect to the consensus hot path.
	GetConsensusState() (*ConsensusState, error)
}

// ConsensusStateEventProvider is optionally implemented by consensus engines
// that can wake an out-of-band snapshot consumer when diagnostics state changes.
//
// Implementations must coalesce notifications and must never block consensus.
// A notification is only a wakeup; consumers must call GetConsensusState to
// read the latest immutable view.
type ConsensusStateEventProvider interface {
	ConsensusStateEvents() <-chan struct{}
}

// ConsensusState is the JSON-RPC facing consensus diagnostics snapshot.
type ConsensusState struct {
	CapturedAt          string       `json:"capturedAt"`
	Complete            bool         `json:"complete"`
	UnavailableSections []string     `json:"unavailableSections,omitempty"`
	NodeID              string       `json:"nodeId"`
	Current             *HeightState `json:"current"`
	LastFinalized       *HeightState `json:"lastFinalized,omitempty"`
}

// HeightState is one height's consensus view (live current or retained finalized).
type HeightState struct {
	Status             string `json:"status"`
	Height             uint64 `json:"height"`
	Round              uint64 `json:"round"`
	Phase              string `json:"phase"`
	RoundStarted       bool   `json:"roundStarted"`
	LastRoundEndReason string `json:"lastRoundEndReason,omitempty"`

	SequenceStartedAt string `json:"sequenceStartedAt,omitempty"`
	SequenceEndedAt   string `json:"sequenceEndedAt,omitempty"`
	RoundStartedAt    string `json:"roundStartedAt,omitempty"`
	PhaseStartedAt    string `json:"phaseStartedAt,omitempty"`
	RoundTimeoutMs    int64  `json:"roundTimeoutMs,omitempty"`
	RoundDeadline     string `json:"roundDeadline,omitempty"`
	RoundRemainingMs  int64  `json:"roundRemainingMs,omitempty"`
	PhaseElapsedMs    int64  `json:"phaseElapsedMs,omitempty"`
	SequenceElapsedMs int64  `json:"sequenceElapsedMs,omitempty"`
	RoundElapsedMs    int64  `json:"roundElapsedMs,omitempty"`

	CompletedPhaseDurationsMs map[string]int64 `json:"completedPhaseDurationsMs,omitempty"`
	RoundHistory              []RoundSummary   `json:"roundHistory,omitempty"`
	PhaseSnapshots            []PhaseSnapshot  `json:"phaseSnapshots,omitempty"`

	Proposal   *ProposalInfo `json:"proposal"`
	IsProposer bool          `json:"isProposer"`
	Proposer   string        `json:"proposer,omitempty"`

	Validators       []ValidatorInfo          `json:"validators,omitempty"`
	TotalVotingPower string                   `json:"totalVotingPower,omitempty"`
	QuorumSize       string                   `json:"quorumSize,omitempty"`
	Quorum           map[string]QuorumInfo    `json:"quorum,omitempty"`
	Messages         map[string]MessageGroup  `json:"messages,omitempty"`
	LatestPC         *PreparedCertificateInfo `json:"latestPreparedCertificate,omitempty"`
	CommittedSeals   []CommittedSealInfo      `json:"committedSeals,omitempty"`
}

// PhaseSnapshot is the frozen end-state of one phase (or the in-progress phase).
type PhaseSnapshot struct {
	Phase      string                   `json:"phase"`
	Status     string                   `json:"status"` // completed | in_progress
	Height     uint64                   `json:"height"`
	Round      uint64                   `json:"round"`
	StartedAt  string                   `json:"startedAt,omitempty"`
	EndedAt    string                   `json:"endedAt,omitempty"`
	DurationMs int64                    `json:"durationMs"`
	Proposer   string                   `json:"proposer,omitempty"`
	Proposal   *ProposalInfo            `json:"proposal,omitempty"`
	Quorum     map[string]QuorumInfo    `json:"quorum,omitempty"`
	Messages   map[string]MessageGroup  `json:"messages,omitempty"`
	LatestPC   *PreparedCertificateInfo `json:"latestPreparedCertificate,omitempty"`
}

// RoundSummary is a completed round for the current height.
type RoundSummary struct {
	Round            uint64           `json:"round"`
	EndReason        string           `json:"endReason"`
	StartedAt        string           `json:"startedAt,omitempty"`
	EndedAt          string           `json:"endedAt,omitempty"`
	DurationMs       int64            `json:"durationMs"`
	PhaseDurationsMs map[string]int64 `json:"phaseDurationsMs,omitempty"`
}

// ProposalInfo describes the accepted proposal and decoded block metadata.
type ProposalInfo struct {
	Available bool   `json:"available"`
	Hash      string `json:"hash,omitempty"`
	Round     uint64 `json:"round,omitempty"`
	RawSize   int    `json:"rawSize,omitempty"`

	BlockHash   string `json:"blockHash,omitempty"`
	BlockNumber uint64 `json:"blockNumber,omitempty"`
	TxCount     int    `json:"txCount,omitempty"`
	GasLimit    uint64 `json:"gasLimit,omitempty"`
	GasUsed     uint64 `json:"gasUsed,omitempty"`
	Timestamp   uint64 `json:"timestamp,omitempty"`
	DecodeError string `json:"decodeError,omitempty"`
}

// ValidatorInfo is one validator and its voting power.
type ValidatorInfo struct {
	ID          string `json:"id"`
	VotingPower string `json:"votingPower"`
}

// QuorumInfo is received vs required voting power for a message type.
type QuorumInfo struct {
	Available     bool   `json:"available"`
	Count         int    `json:"count"`
	ReceivedPower string `json:"receivedPower,omitempty"`
	RequiredPower string `json:"requiredPower,omitempty"`
	HasQuorum     bool   `json:"hasQuorum"`
}

// MessageGroup is the accepted messages of one type for a view.
type MessageGroup struct {
	Available  bool          `json:"available"`
	Truncated  bool          `json:"truncated,omitempty"`
	ViewHeight uint64        `json:"viewHeight"`
	ViewRound  uint64        `json:"viewRound"`
	Count      int           `json:"count"`
	Messages   []MessageInfo `json:"messages,omitempty"`
}

// MessageInfo is one accepted consensus message.
type MessageInfo struct {
	From          string `json:"from"`
	Type          string `json:"type"`
	Height        uint64 `json:"height"`
	Round         uint64 `json:"round"`
	ProposalHash  string `json:"proposalHash,omitempty"`
	Signature     string `json:"signature,omitempty"`
	CommittedSeal string `json:"committedSeal,omitempty"`
}

// PreparedCertificateInfo summarizes the latest prepared certificate.
type PreparedCertificateInfo struct {
	Available      bool     `json:"available"`
	ProposalHash   string   `json:"proposalHash,omitempty"`
	PrepareCount   int      `json:"prepareCount"`
	ProposalFrom   string   `json:"proposalFrom,omitempty"`
	PrepareSenders []string `json:"prepareSenders,omitempty"`
}

// CommittedSealInfo is one commit seal.
type CommittedSealInfo struct {
	Signer    string `json:"signer"`
	Signature string `json:"signature"`
}
