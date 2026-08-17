package ibft

import (
	"testing"
	"time"

	"github.com/0xPolygon/go-ibft/core"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertProposal_DecodesBlock(t *testing.T) {
	t.Parallel()

	header := &types.Header{
		Number:    42,
		GasLimit:  1000,
		GasUsed:   250,
		Timestamp: 123456,
	}
	header.ComputeHash()

	block := &types.Block{Header: header}
	raw := block.MarshalRLP()
	info := convertProposal(&core.ProposalSnapshot{
		Available:   true,
		Hash:        []byte{0x01, 0x02},
		Round:       3,
		RawSize:     len(raw),
		RawProposal: raw,
	})

	require.NotNil(t, info)
	assert.True(t, info.Available)
	assert.Equal(t, "0x0102", info.Hash)
	assert.Equal(t, uint64(3), info.Round)
	assert.Equal(t, len(raw), info.RawSize)
	assert.Equal(t, uint64(42), info.BlockNumber)
	assert.Equal(t, 0, info.TxCount)
	assert.Equal(t, uint64(1000), info.GasLimit)
	assert.Equal(t, uint64(250), info.GasUsed)
	assert.Equal(t, uint64(123456), info.Timestamp)
	assert.NotEmpty(t, info.BlockHash)
	assert.Empty(t, info.DecodeError)
}

func TestConvertProposal_Malformed(t *testing.T) {
	t.Parallel()

	info := convertProposal(&core.ProposalSnapshot{
		Available:   true,
		RawProposal: []byte("not-rlp"),
		RawSize:     7,
	})
	require.NotNil(t, info)
	assert.True(t, info.Available)
	assert.NotEmpty(t, info.DecodeError)
	assert.Zero(t, info.BlockNumber)
}

func TestGetConsensusState_NotInitialized(t *testing.T) {
	t.Parallel()

	b := &backendIBFT{}
	_, err := b.GetConsensusState()
	require.Error(t, err)
	assert.Nil(t, b.ConsensusStateEvents())
}

func TestRoundRemainingMs(t *testing.T) {
	t.Parallel()

	captured := time.Date(2026, 8, 4, 11, 43, 36, 492465000, time.UTC)
	deadline := captured.Add(11 * time.Second)

	assert.Equal(t, int64(11000), roundRemainingMs(captured, deadline))
	assert.Equal(t, int64(0), roundRemainingMs(deadline.Add(time.Second), deadline))
	assert.Equal(t, int64(0), roundRemainingMs(time.Time{}, deadline))
}
