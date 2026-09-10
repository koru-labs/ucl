package jsonrpc

import (
	"fmt"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

type consensusEndpointMockStore struct {
	getConsensusStateFn func() (interface{}, error)
}

func (s *consensusEndpointMockStore) GetConsensusState() (interface{}, error) {
	if s.getConsensusStateFn != nil {
		return s.getConsensusStateFn()
	}

	return nil, fmt.Errorf("consensus state is not supported by the active consensus engine")
}

func TestConsensus_State(t *testing.T) {
	t.Parallel()

	t.Run("unsupported engine returns error", func(t *testing.T) {
		t.Parallel()

		endpoint := &Consensus{store: &consensusEndpointMockStore{}}
		res, err := endpoint.State()
		require.Nil(t, res)
		require.ErrorContains(t, err, "not supported")
	})

	t.Run("returns provider snapshot", func(t *testing.T) {
		t.Parallel()

		expected := map[string]interface{}{
			"status": "running",
			"height": uint64(10),
			"round":  uint64(1),
			"phase":  "prepare",
		}
		endpoint := &Consensus{
			store: &consensusEndpointMockStore{
				getConsensusStateFn: func() (interface{}, error) {
					return expected, nil
				},
			},
		}

		res, err := endpoint.State()
		require.NoError(t, err)
		require.Equal(t, expected, res)
	})
}

func TestDispatcher_ConsensusEndpointsGated(t *testing.T) {
	t.Parallel()

	t.Run("disabled by default", func(t *testing.T) {
		t.Parallel()

		d, err := newDispatcher(hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})
		require.NoError(t, err)

		_, _, rpcErr := d.getFnHandler(Request{Method: "consensus_state"})
		require.NotNil(t, rpcErr)
	})

	t.Run("enabled registers consensus_state", func(t *testing.T) {
		t.Parallel()

		d, err := newDispatcher(hclog.NewNullLogger(), newMockStore(), &dispatcherParams{
			enableConsensusEndpoints: true,
		})
		require.NoError(t, err)

		_, fn, rpcErr := d.getFnHandler(Request{Method: "consensus_state"})
		require.Nil(t, rpcErr)
		require.NotNil(t, fn)
	})
}
