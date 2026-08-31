package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/hashicorp/go-hclog"
)

const (
	consensusStatePushPath             = "/api/v1/snapshots"
	consensusStatePushTimeout          = 2 * time.Second
	defaultConsensusStatePushHeartbeat = 30 * time.Second
)

// consensusStatePusher posts consensus diagnostics snapshots from a dedicated
// goroutine. Consensus events trigger immediate pushes; the heartbeat recovers
// from missed events and keeps idle nodes visible.
//
// Event-triggered pushes are skipped when the snapshot is unchanged apart from
// clock-derived fields (capturedAt, elapsed/remaining timers); heartbeat pushes
// are always sent so the backend keeps seeing the node as alive.
type consensusStatePusher struct {
	logger    hclog.Logger
	provider  consensus.ConsensusStateProvider
	events    <-chan struct{}
	url       string
	token     string
	heartbeat time.Duration
	client    *http.Client
	closeCh   chan struct{}
	closeOnce sync.Once

	// lastFingerprint is the hash of the last successfully pushed snapshot
	// with volatile fields cleared. Only touched by the loop goroutine.
	lastFingerprint [sha256.Size]byte
	hasFingerprint  bool
}

func newConsensusStatePusher(
	logger hclog.Logger,
	provider consensus.ConsensusStateProvider,
	pushURL, token string,
	interval time.Duration,
) *consensusStatePusher {
	var events <-chan struct{}
	if eventProvider, ok := provider.(consensus.ConsensusStateEventProvider); ok {
		events = eventProvider.ConsensusStateEvents()
	}

	return &consensusStatePusher{
		logger:    logger.Named("consensus-state-pusher"),
		provider:  provider,
		events:    events,
		url:       resolveConsensusStatePushEndpoint(pushURL),
		token:     token,
		heartbeat: interval,
		client: &http.Client{
			Timeout: consensusStatePushTimeout,
		},
		closeCh: make(chan struct{}),
	}
}

func resolveConsensusStatePushEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(baseURL, consensusStatePushPath) {
		return baseURL
	}

	return baseURL + consensusStatePushPath
}

func (p *consensusStatePusher) start() {
	go p.loop()
}

func (p *consensusStatePusher) stop() {
	p.closeOnce.Do(func() { close(p.closeCh) })
}

func (p *consensusStatePusher) loop() {
	if p.events != nil && !p.drainEvents() {
		p.events = nil
	}

	p.pushOnce(true)

	ticker := time.NewTicker(p.heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-p.closeCh:
			return
		case _, ok := <-p.events:
			if !ok {
				p.events = nil

				continue
			}
			// Collapse any event burst that accumulated before starting the
			// comparatively expensive snapshot capture and HTTP request.
			if !p.drainEvents() {
				p.events = nil
			}

			p.pushOnce(false)
		case <-ticker.C:
			if p.events != nil && !p.drainEvents() {
				p.events = nil
			}

			p.pushOnce(true)
		}
	}
}

func (p *consensusStatePusher) drainEvents() bool {
	for {
		select {
		case _, ok := <-p.events:
			if !ok {
				return false
			}
		default:
			return true
		}
	}
}

// pushOnce captures and posts one snapshot. When force is false the push is
// skipped if the snapshot's non-volatile content matches the last pushed one.
func (p *consensusStatePusher) pushOnce(force bool) {
	state, err := p.provider.GetConsensusState()
	if err != nil {
		p.logger.Debug("skip consensus state push", "err", err)

		return
	}

	if state == nil {
		p.logger.Debug("skip consensus state push", "err", "nil snapshot")

		return
	}

	fingerprint, fpOK := snapshotFingerprint(state)
	if !force && fpOK && p.hasFingerprint && fingerprint == p.lastFingerprint {
		return
	}

	body, err := json.Marshal(state)
	if err != nil {
		p.logger.Warn("failed to marshal consensus state", "err", err)

		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), consensusStatePushTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		p.logger.Warn("failed to create consensus state push request", "err", err)

		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.client.Do(req) //nolint:gosec // G704: URL is operator-configured, not request input
	if err != nil {
		p.logger.Warn("failed to push consensus state", "err", err)

		return
	}

	defer resp.Body.Close() //nolint:errcheck

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		p.logger.Warn("consensus state push rejected", "status", resp.StatusCode)

		return
	}

	if fpOK {
		p.lastFingerprint = fingerprint
		p.hasFingerprint = true
	}
}

// snapshotFingerprint hashes the snapshot with clock-derived fields cleared so
// two captures of the same consensus situation compare equal. The input is not
// modified: shallow copies are made of the structs whose fields are cleared.
func snapshotFingerprint(state *consensus.ConsensusState) ([sha256.Size]byte, bool) {
	stable := *state
	stable.CapturedAt = ""

	if state.Current != nil {
		cur := *state.Current
		cur.RoundDeadline = ""
		cur.RoundRemainingMs = 0
		cur.PhaseElapsedMs = 0
		cur.SequenceElapsedMs = 0
		cur.RoundElapsedMs = 0

		if n := len(cur.PhaseSnapshots); n > 0 {
			phases := make([]consensus.PhaseSnapshot, n)
			copy(phases, cur.PhaseSnapshots)

			for i := range phases {
				if phases[i].Status == "in_progress" {
					phases[i].DurationMs = 0
				}
			}

			cur.PhaseSnapshots = phases
		}

		stable.Current = &cur
	}

	raw, err := json.Marshal(&stable)
	if err != nil {
		return [sha256.Size]byte{}, false
	}

	return sha256.Sum256(raw), true
}
