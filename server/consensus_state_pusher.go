package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
type consensusStatePusher struct {
	logger    hclog.Logger
	provider  consensus.ConsensusStateProvider
	events    <-chan struct{}
	url       string
	token     string
	heartbeat time.Duration
	client    *http.Client
	closeCh   chan struct{}
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
	select {
	case <-p.closeCh:
		return
	default:
		close(p.closeCh)
	}
}

func (p *consensusStatePusher) loop() {
	if p.events != nil && !p.drainEvents() {
		p.events = nil
	}
	p.pushOnce()

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
			p.pushOnce()
		case <-ticker.C:
			if p.events != nil && !p.drainEvents() {
				p.events = nil
			}
			p.pushOnce()
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

func (p *consensusStatePusher) pushOnce() {
	state, err := p.provider.GetConsensusState()
	if err != nil {
		p.logger.Debug("skip consensus state push", "err", err)

		return
	}

	if state == nil {
		p.logger.Debug("skip consensus state push", "err", "nil snapshot")

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

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Warn("failed to push consensus state", "err", err)

		return
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		p.logger.Warn("consensus state push rejected", "status", resp.StatusCode)
	}
}
