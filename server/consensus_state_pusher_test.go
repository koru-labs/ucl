package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

type mockConsensusStateProvider struct {
	mu    sync.Mutex
	state *consensus.ConsensusState
	err   error
	calls atomic.Int64
}

func (m *mockConsensusStateProvider) GetConsensusState() (*consensus.ConsensusState, error) {
	m.calls.Add(1)

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.state, m.err
}

func (m *mockConsensusStateProvider) setState(state *consensus.ConsensusState) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state = state
}

type eventedConsensusStateProvider struct {
	*mockConsensusStateProvider
	events chan struct{}
}

func (m *eventedConsensusStateProvider) ConsensusStateEvents() <-chan struct{} {
	return m.events
}

// providerConsensus implements both consensus.Consensus and ConsensusStateProvider.
type providerConsensus struct {
	consensus.Consensus
	provider consensus.ConsensusStateProvider
}

func (p *providerConsensus) GetConsensusState() (*consensus.ConsensusState, error) {
	return p.provider.GetConsensusState()
}

// plainConsensus implements consensus.Consensus but not ConsensusStateProvider.
type plainConsensus struct {
	consensus.Consensus
}

func TestConsensusStatePusherPostsSnapshot(t *testing.T) {
	t.Parallel()

	var (
		mu         sync.Mutex
		gotAuth    string
		gotContent string
		gotBody    consensus.ConsensusState
		hits       atomic.Int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, consensusStatePushPath, r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		var parsed consensus.ConsensusState
		require.NoError(t, json.Unmarshal(body, &parsed))

		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotContent = r.Header.Get("Content-Type")
		gotBody = parsed
		mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	provider := &eventedConsensusStateProvider{
		mockConsensusStateProvider: &mockConsensusStateProvider{
			state: &consensus.ConsensusState{
				CapturedAt: "2026-08-05T12:00:00Z",
				Complete:   true,
				NodeID:     "0xabc",
				Current: &consensus.HeightState{
					Status: "running",
					Height: 7,
					Round:  0,
					Phase:  "prepare",
				},
			},
		},
		events: make(chan struct{}, 1),
	}

	pusher := newConsensusStatePusher(
		hclog.NewNullLogger(),
		provider,
		srv.URL,
		"secret-token",
		time.Hour,
	)
	pusher.start()
	t.Cleanup(pusher.stop)

	require.Eventually(t, func() bool {
		return hits.Load() == 1
	}, time.Second, 20*time.Millisecond)

	// Advance consensus (new round) and signal: the loop is parked on the
	// events channel, so the send orders this write before the next capture.
	provider.setState(&consensus.ConsensusState{
		CapturedAt: "2026-08-05T12:00:01Z",
		Complete:   true,
		NodeID:     "0xabc",
		Current: &consensus.HeightState{
			Status: "running",
			Height: 7,
			Round:  1,
			Phase:  "new_round",
		},
	})
	provider.events <- struct{}{}

	require.Eventually(t, func() bool {
		return hits.Load() == 2
	}, time.Second, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, "Bearer secret-token", gotAuth)
	require.Equal(t, "application/json", gotContent)
	require.Equal(t, "0xabc", gotBody.NodeID)
	require.Equal(t, uint64(7), gotBody.Current.Height)
	require.Equal(t, uint64(1), gotBody.Current.Round)
	require.GreaterOrEqual(t, provider.calls.Load(), int64(2))
}

func TestConsensusStatePusherSkipsUnchangedSnapshotOnEvent(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	mkState := func(capturedAt string, elapsed int64) *consensus.ConsensusState {
		return &consensus.ConsensusState{
			CapturedAt: capturedAt,
			Complete:   true,
			NodeID:     "0xabc",
			Current: &consensus.HeightState{
				Status:           "running",
				Height:           3,
				Round:            0,
				Phase:            "prepare",
				RoundRemainingMs: 10000 - elapsed,
				PhaseElapsedMs:   elapsed,
				RoundElapsedMs:   elapsed,
				PhaseSnapshots: []consensus.PhaseSnapshot{
					{Phase: "new_round", Status: "completed", DurationMs: 40},
					{Phase: "prepare", Status: "in_progress", DurationMs: elapsed},
				},
			},
		}
	}

	provider := &eventedConsensusStateProvider{
		mockConsensusStateProvider: &mockConsensusStateProvider{state: mkState("2026-08-05T12:00:00Z", 5)},
		events:                     make(chan struct{}, 1),
	}

	pusher := newConsensusStatePusher(hclog.NewNullLogger(), provider, srv.URL, "tok", time.Hour)
	pusher.start()
	t.Cleanup(pusher.stop)

	require.Eventually(t, func() bool { return hits.Load() == 1 }, time.Second, 10*time.Millisecond)

	// Only clock-derived fields differ: an event must NOT produce a push.
	provider.setState(mkState("2026-08-05T12:00:00.5Z", 505))
	provider.events <- struct{}{}

	require.Eventually(t, func() bool { return provider.calls.Load() >= 2 }, time.Second, 10*time.Millisecond)
	require.Never(t, func() bool { return hits.Load() > 1 }, 100*time.Millisecond, 10*time.Millisecond)

	// A real change (round advanced) pushes immediately on the next event.
	next := mkState("2026-08-05T12:00:02Z", 7)
	next.Current.Round = 1

	provider.setState(next)
	provider.events <- struct{}{}

	require.Eventually(t, func() bool { return hits.Load() == 2 }, time.Second, 10*time.Millisecond)

	// The heartbeat must still push an unchanged snapshot (liveness signal).
	var hbHits atomic.Int64

	hbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hbHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hbSrv.Close)

	hbProvider := &mockConsensusStateProvider{state: mkState("2026-08-05T12:00:00Z", 5)}
	hbPusher := newConsensusStatePusher(hclog.NewNullLogger(), hbProvider, hbSrv.URL, "tok", 50*time.Millisecond)
	hbPusher.start()
	t.Cleanup(hbPusher.stop)

	require.Eventually(t, func() bool { return hbHits.Load() >= 3 }, 2*time.Second, 10*time.Millisecond)
}

func TestConsensusStatePusherStopIsIdempotent(t *testing.T) {
	t.Parallel()

	provider := &mockConsensusStateProvider{err: errors.New("not ready")}
	pusher := newConsensusStatePusher(hclog.NewNullLogger(), provider, "http://127.0.0.1:1", "tok", time.Hour)
	pusher.start()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			pusher.stop()
		}()
	}

	wg.Wait()
	pusher.stop()
}

func TestConsensusStatePusherDisabledWhenURLEmpty(t *testing.T) {
	t.Parallel()

	s := &Server{
		logger: hclog.NewNullLogger(),
		config: &Config{ConsensusStatePushURL: ""},
		consensus: &providerConsensus{
			provider: &mockConsensusStateProvider{
				state: &consensus.ConsensusState{NodeID: "0x1"},
			},
		},
	}

	s.startConsensusStatePusher()
	require.Nil(t, s.consensusStatePusher)
}

func TestConsensusStatePusherUnsupportedEngine(t *testing.T) {
	t.Parallel()

	s := &Server{
		logger: hclog.NewNullLogger(),
		config: &Config{
			ConsensusStatePushURL:      "http://127.0.0.1:9",
			ConsensusStatePushToken:    "token",
			ConsensusStatePushInterval: time.Second,
		},
		consensus: &plainConsensus{},
	}

	s.startConsensusStatePusher()
	require.Nil(t, s.consensusStatePusher)
}

func TestConsensusStatePusherSkipsProviderErrors(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	provider := &mockConsensusStateProvider{err: errors.New("not ready")}
	pusher := newConsensusStatePusher(
		hclog.NewNullLogger(),
		provider,
		srv.URL,
		"token",
		30*time.Millisecond,
	)
	pusher.start()
	t.Cleanup(pusher.stop)

	time.Sleep(100 * time.Millisecond)
	require.Equal(t, int64(0), hits.Load())
	require.GreaterOrEqual(t, provider.calls.Load(), int64(1))
}

func TestConsensusStatePusherHandlesHTTPFailures(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	provider := &mockConsensusStateProvider{
		state: &consensus.ConsensusState{
			NodeID:  "0xabc",
			Current: &consensus.HeightState{Status: "running"},
		},
	}
	pusher := newConsensusStatePusher(
		hclog.NewNullLogger(),
		provider,
		srv.URL,
		"token",
		30*time.Millisecond,
	)
	pusher.start()
	t.Cleanup(pusher.stop)

	require.Eventually(t, func() bool {
		return provider.calls.Load() >= 2
	}, time.Second, 20*time.Millisecond)
}

func TestResolveConsensusStatePushEndpoint(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"http://example.com/api/v1/snapshots",
		resolveConsensusStatePushEndpoint("http://example.com"),
	)
	require.Equal(
		t,
		"http://example.com/api/v1/snapshots",
		resolveConsensusStatePushEndpoint("http://example.com/api/v1/snapshots"),
	)
}

func TestStartConsensusStatePusherWiresWorker(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	provider := &mockConsensusStateProvider{
		state: &consensus.ConsensusState{
			NodeID:  "0xabc",
			Current: &consensus.HeightState{Status: "running"},
		},
	}

	s := &Server{
		logger: hclog.NewNullLogger(),
		config: &Config{
			ConsensusStatePushURL:      srv.URL,
			ConsensusStatePushToken:    "token",
			ConsensusStatePushInterval: 40 * time.Millisecond,
		},
		consensus: &providerConsensus{provider: provider},
	}

	s.startConsensusStatePusher()
	require.NotNil(t, s.consensusStatePusher)
	t.Cleanup(s.consensusStatePusher.stop)

	require.Eventually(t, func() bool {
		return provider.calls.Load() >= 1
	}, time.Second, 20*time.Millisecond)
}
