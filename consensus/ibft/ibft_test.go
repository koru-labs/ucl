package ibft

import (
	"testing"

	"github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestConsensusRuntime_StartRound(t *testing.T) {
	cases := []struct {
		funcName string
		round    uint64
	}{
		{
			funcName: "ClearProposed",
			round:    0,
		},
		{
			funcName: "ReinsertProposed",
			round:    1,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.funcName, func(t *testing.T) {
			txPool := new(txPoolMock)
			txPool.On(c.funcName).Once()

			runtime := &backendIBFT{
				txpool: txPool,
				logger: hclog.NewNullLogger(),
			}

			view := &proto.View{Round: c.round}
			require.NoError(t, runtime.RoundStarts(view))
			txPool.AssertExpectations(t)
		})
	}
}

func TestConsensusRuntime_SequenceCancelled(t *testing.T) {
	txPool := new(txPoolMock)
	txPool.On("ReinsertProposed").Once()

	runtime := &backendIBFT{
		txpool: txPool,
		logger: hclog.NewNullLogger(),
	}

	view := &proto.View{}
	require.NoError(t, runtime.SequenceCancelled(view))
	txPool.AssertExpectations(t)
}
