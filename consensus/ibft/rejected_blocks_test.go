package ibft

import (
	"path/filepath"
	"testing"

	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestRejectedBlockStoreKeepsLastN(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), RejectedBlocksFileName)
	store := newRejectedBlockStore(path, 3, hclog.NewNullLogger())

	for i := uint64(1); i <= 5; i++ {
		block := &types.Block{
			Header: &types.Header{Number: i},
		}
		block.Header.ComputeHash()
		store.record(block, "invalid block state root", types.ZeroHash, nil)
	}

	recs, err := LoadRejectedBlocks(path)
	require.NoError(t, err)
	require.Len(t, recs, 3)
	require.Equal(t, uint64(3), recs[0].Block.Number())
	require.Equal(t, uint64(5), recs[2].Block.Number())
	require.Equal(t, "invalid block state root", recs[0].Reason)
}

func TestRejectedBlockStoreRoundtripOutcomes(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), RejectedBlocksFileName)
	store := newRejectedBlockStore(path, 16, hclog.NewNullLogger())

	block := &types.Block{Header: &types.Header{Number: 7, StateRoot: types.StringToHash("0x11")}}
	block.Header.ComputeHash()

	local := types.StringToHash("0x22")
	outcomes := []state.TxExecOutcome{{
		Hash:       types.StringToHash("0xaa"),
		Status:     types.ReceiptSuccess,
		GasUsed:    21015,
		ReturnHash: types.StringToHash("0xbb"),
		DeltaHash:  types.StringToHash("0xcc"),
	}}

	store.record(block, "invalid block state root", local, outcomes)

	recs, err := LoadRejectedBlocks(path)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, local, recs[0].LocalStateRoot)
	require.Equal(t, outcomes, recs[0].Outcomes)
}
