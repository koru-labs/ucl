package tracker

import (
	"context"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/testutil"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

type mockEventSubscriber struct {
	lock sync.RWMutex
	logs []*ethgo.Log
}

func (m *mockEventSubscriber) AddLog(log *ethgo.Log) error {
	m.lock.Lock()
	defer m.lock.Unlock()

	if len(m.logs) == 0 {
		m.logs = []*ethgo.Log{}
	}

	m.logs = append(m.logs, log)

	return nil
}

func (m *mockEventSubscriber) len() int {
	m.lock.RLock()
	defer m.lock.RUnlock()

	return len(m.logs)
}

func TestEventTracker_TrackSyncEvents(t *testing.T) {
	const (
		numBlockConfirmations = 6
		eventsPerStep         = 8
	)

	server := testutil.DeployTestServer(t, nil)

	tmpDir, err := os.MkdirTemp("/tmp", "test-event-tracker")
	require.NoError(t, err)

	defer os.RemoveAll(tmpDir)

	cc := &testutil.Contract{}
	cc.AddCallback(func() string {
		return `
			event StateSync(uint256 indexed id, address indexed target, bytes data);

			function emitEvent() public payable {
				emit StateSync(1, msg.sender, bytes(""));
			}
			`
	})

	_, addr, err := server.DeployContract(cc)
	require.NoError(t, err)

	// prefill with eventsPerStep + numBlockConfirmations events
	for i := 0; i < eventsPerStep+numBlockConfirmations; i++ {
		receipt, err := server.TxnTo(addr, "emitEvent")
		require.NoError(t, err)
		require.Equal(t, uint64(types.ReceiptSuccess), receipt.Status)
	}

	sub := &mockEventSubscriber{}

	tracker := &EventTracker{
		logger:                hclog.NewNullLogger(),
		subscriber:            sub,
		dbPath:                path.Join(tmpDir, "test.db"),
		rpcEndpoint:           server.HTTPAddr(),
		contractAddr:          addr,
		numBlockConfirmations: numBlockConfirmations,
		pollInterval:          time.Second,
	}

	err = tracker.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(2 * time.Second)
	require.Equal(t, eventsPerStep, sub.len())
}
