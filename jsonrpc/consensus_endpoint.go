package jsonrpc

import "errors"

// consensusStore provides access to live consensus diagnostics.
type consensusStore interface {
	// GetConsensusState returns the current node's live consensus snapshot, if supported.
	GetConsensusState() (interface{}, error)
}

// Consensus is the consensus JSON-RPC endpoint namespace.
type Consensus struct {
	store consensusStore
}

// State returns this node's live view of consensus (height, round, phase,
// votes, quorum progress, proposal metadata, and timing).
//
//nolint:stylecheck
func (c *Consensus) State() (interface{}, error) {
	if c.store == nil {
		return nil, errors.New("consensus state store is unavailable")
	}

	return c.store.GetConsensusState()
}
