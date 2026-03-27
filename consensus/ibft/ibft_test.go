package ibft

import (
	"errors"
	"testing"
	"time"

	"github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/0xPolygon/polygon-edge/helper/progress"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestIBFTBackend_StartRound(t *testing.T) {
	t.Parallel()

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

			i := &backendIBFT{
				txpool: txPool,
				logger: hclog.NewNullLogger(),
			}

			view := &proto.View{Round: c.round}
			require.NoError(t, i.RoundStarts(view))
			txPool.AssertExpectations(t)
		})
	}
}

func TestIBFTBackend_SequenceCancelled(t *testing.T) {
	t.Parallel()

	txPool := new(txPoolMock)
	txPool.On("ReinsertProposed").Once()

	i := &backendIBFT{
		txpool: txPool,
		logger: hclog.NewNullLogger(),
	}

	view := &proto.View{}
	require.NoError(t, i.SequenceCancelled(view))
	txPool.AssertExpectations(t)
}

func TestIBFTBackend_Start(t *testing.T) {
	t.Parallel()

	syncer := &syncerMock{}
	syncer.On("Start").Return(nil).Once()

	i := &backendIBFT{
		syncer: syncer,
	}

	require.NoError(t, i.Start())
}

func TestIBFTBackend_Close(t *testing.T) {
	t.Parallel()

	syncer := &syncerMock{}
	syncer.On("Close").Return(error(nil)).Once()

	i := &backendIBFT{
		closeCh: make(chan struct{}),
		syncer:  syncer,
	}

	require.NoError(t, i.Close())

	<-i.closeCh

	syncer.AssertExpectations(t)

	errExpected := errors.New("something")
	syncer.On("Close").Return(errExpected).Once()

	i.closeCh = make(chan struct{})

	require.Error(t, errExpected, i.Close())

	select {
	case <-i.closeCh:
	case <-time.After(time.Millisecond * 100):
		require.Fail(t, "channel closing not invoked")
	}

	syncer.AssertExpectations(t)
}

func TestIBFTBackend_GetSyncProgression(t *testing.T) {
	t.Parallel()

	result := &progress.Progression{}

	syncer := &syncerMock{}
	syncer.On("GetSyncProgression").Return(result).Once()

	i := &backendIBFT{
		syncer: syncer,
	}

	require.Equal(t, result, i.GetSyncProgression())
}

func TestIBFTBackend_GetEpoch(t *testing.T) {
	t.Parallel()

	i := &backendIBFT{
		epochSize: 10,
	}

	t.Run("MiddleOfEpoch", func(t *testing.T) {
		require.Equal(t, uint64(1), i.GetEpoch(i.epochSize/2))
	})

	t.Run("EndOfEpoch", func(t *testing.T) {
		require.Equal(t, uint64(1), i.GetEpoch(i.epochSize))
	})
}

func TestIBFTBackend_IsLastOfEpoch(t *testing.T) {
	t.Parallel()

	i := &backendIBFT{
		epochSize: 10,
	}

	t.Run("MiddleOfEpoch", func(t *testing.T) {
		require.Equal(t, false, i.IsLastOfEpoch(i.epochSize/2))
	})

	t.Run("EndOfEpoch", func(t *testing.T) {
		require.Equal(t, true, i.IsLastOfEpoch(i.epochSize))
	})
}
