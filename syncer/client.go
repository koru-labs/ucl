package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/network"
	"github.com/0xPolygon/polygon-edge/network/event"
	"github.com/0xPolygon/polygon-edge/syncer/proto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-metrics"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	SyncPeerClientLoggerName = "sync-peer-client"
	statusTopicName          = "syncer/status/0.1"
	defaultTimeoutForStatus  = 10 * time.Second
)

type SyncBlock struct {
	Block *types.Block
}

type ReceiptsMsg struct {
	BlockNumber uint64
	Receipts    types.Receipts
	Received    bool
}

type syncPeerClient struct {
	logger     hclog.Logger // logger used for console logging
	network    Network      // reference to the network module
	blockchain Blockchain   // reference to the blockchain module
	txPool     TxPool       // reference to the txpool module

	subscription           blockchain.Subscription // reference to the blockchain subscription
	topic                  *network.Topic          // reference to the network topic
	id                     string                  // node id
	peerStatusUpdateCh     chan *NoForkPeer        // peer status update channel
	peerConnectionUpdateCh chan *event.PeerEvent   // peer connection update channel

	shouldEmitBlocks bool // flag for emitting blocks in the topic
	closeCh          chan struct{}
	closed           atomic.Bool

	peerStatusUpdateChLock   sync.Mutex
	peerStatusUpdateChClosed bool
}

func NewSyncPeerClient(
	logger hclog.Logger,
	network Network,
	blockchain Blockchain,
	txPool TxPool,
) SyncPeerClient {
	return &syncPeerClient{
		logger:                 logger.Named(SyncPeerClientLoggerName),
		network:                network,
		blockchain:             blockchain,
		id:                     network.AddrInfo().ID.String(),
		peerStatusUpdateCh:     make(chan *NoForkPeer, 1),
		peerConnectionUpdateCh: make(chan *event.PeerEvent, 1),
		shouldEmitBlocks:       true,
		closeCh:                make(chan struct{}),

		peerStatusUpdateChLock:   sync.Mutex{},
		peerStatusUpdateChClosed: false,
		txPool:                   txPool,
	}
}

// Start processes for SyncPeerClient
func (m *syncPeerClient) Start() error {
	// Mark client active.
	m.closed.Store(false)

	go m.startNewBlockProcess()
	go m.startPeerEventProcess()

	if err := m.startGossip(); err != nil {
		return err
	}

	return nil
}

// Close terminates running processes for SyncPeerClient
func (m *syncPeerClient) Close() {
	if m.closed.Swap(true) {
		// Already closed.
		return
	}

	if m.topic != nil {
		m.topic.Close()
	}

	if m.subscription != nil {
		m.blockchain.UnsubscribeEvents(m.subscription)

		m.subscription = nil
	}

	if m.closeCh != nil {
		close(m.closeCh)
	}

	m.peerStatusUpdateChLock.Lock()
	m.peerStatusUpdateChClosed = true
	close(m.peerStatusUpdateCh)
	m.peerStatusUpdateChLock.Unlock()
}

// DisablePublishingPeerStatus disables publishing own status via gossip
func (m *syncPeerClient) DisablePublishingPeerStatus() {
	m.shouldEmitBlocks = false
}

// EnablePublishingPeerStatus enables publishing own status via gossip
func (m *syncPeerClient) EnablePublishingPeerStatus() {
	m.shouldEmitBlocks = true
}

// GetPeerStatus fetches peer status
func (m *syncPeerClient) GetPeerStatus(peerID peer.ID) (*NoForkPeer, error) {
	clt, err := m.newSyncPeerClient(peerID)
	if err != nil {
		return nil, err
	}

	defer m.CloseStream(peerID) //nolint:errcheck

	timeoutCtx, cancel := context.WithTimeout(context.Background(), defaultTimeoutForStatus)
	defer cancel()

	status, err := clt.GetStatus(timeoutCtx, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}

	return &NoForkPeer{
		ID:       peerID,
		Number:   status.Number,
		Distance: m.network.GetPeerDistance(peerID),
	}, nil
}

// GetConnectedPeerStatuses fetches the statuses of all connecting peers
func (m *syncPeerClient) GetConnectedPeerStatuses() []*NoForkPeer {
	var (
		ps            = m.network.Peers()
		syncPeers     = make([]*NoForkPeer, 0, len(ps))
		syncPeersLock sync.Mutex
		wg            sync.WaitGroup
	)

	for _, p := range ps {
		p := p

		wg.Add(1)

		go func() {
			defer wg.Done()

			peerID := p.Info.ID

			status, err := m.GetPeerStatus(peerID)
			if err != nil {
				m.logger.Warn("failed to get status from a peer, skip", "id", peerID, "err", err)

				return // Skip appending nil status
			}

			syncPeersLock.Lock()

			syncPeers = append(syncPeers, status)

			syncPeersLock.Unlock()
		}()
	}

	wg.Wait()

	return syncPeers
}

// GetPeerStatusUpdateCh returns a channel of peer's status update
func (m *syncPeerClient) GetPeerStatusUpdateCh() <-chan *NoForkPeer {
	return m.peerStatusUpdateCh
}

// GetPeerConnectionUpdateEventCh returns peer's connection change event
func (m *syncPeerClient) GetPeerConnectionUpdateEventCh() <-chan *event.PeerEvent {
	return m.peerConnectionUpdateCh
}

// startGossip creates new topic and starts subscribing
func (m *syncPeerClient) startGossip() error {
	topic, err := m.network.NewTopic(statusTopicName, &proto.SyncPeerStatus{})
	if err != nil {
		return err
	}

	if err := topic.Subscribe(m.handleStatusUpdate); err != nil {
		return fmt.Errorf("unable to subscribe to gossip topic, %w", err)
	}

	m.topic = topic

	return nil
}

// handleStatusUpdate is a handler of gossip
func (m *syncPeerClient) handleStatusUpdate(obj interface{}, from peer.ID) {
	status, ok := obj.(*proto.SyncPeerStatus)
	if !ok {
		m.logger.Error("failed to cast gossiped message to txn")

		return
	}

	if !m.network.IsConnected(from) {
		if m.id != from.String() {
			m.logger.Debug("received status from non-connected peer, ignore", "id", from)
		}

		return
	}

	m.peerStatusUpdateChLock.Lock()
	defer m.peerStatusUpdateChLock.Unlock()

	if !m.peerStatusUpdateChClosed {
		m.peerStatusUpdateCh <- &NoForkPeer{
			ID:       from,
			Number:   status.Number,
			Distance: m.network.GetPeerDistance(from),
		}
	}
}

// startNewBlockProcess starts blockchain event subscription
func (m *syncPeerClient) startNewBlockProcess() {
	m.subscription = m.blockchain.SubscribeEvents()
	eventCh := m.subscription.GetEventCh()

	for {
		var event *blockchain.Event

		select {
		case <-m.closeCh:
			return
		case event = <-eventCh:
		}

		if !m.shouldEmitBlocks {
			continue
		}

		if l := len(event.NewChain); l > 0 {
			latest := event.NewChain[l-1]
			// Publish status
			if err := m.topic.Publish(&proto.SyncPeerStatus{
				Number: latest.Number,
			}); err != nil {
				m.logger.Warn("failed to publish status", "err", err)
			}
		}
	}
}

// startPeerEventProcess starts subscribing peer connection change events and process them
func (m *syncPeerClient) startPeerEventProcess() {
	defer close(m.peerConnectionUpdateCh)

	peerEventCh, err := m.network.SubscribeCh(context.Background())
	if err != nil {
		m.logger.Error("failed to subscribe", "err", err)

		return
	}

	for {
		select {
		case <-m.closeCh:
			return

		case e := <-peerEventCh:
			if e != nil && (e.Type == event.PeerConnected || e.Type == event.PeerDisconnected) {
				m.peerConnectionUpdateCh <- e
			}
		}
	}
}

// CloseStream closes stream
func (m *syncPeerClient) CloseStream(peerID peer.ID) error {
	return m.network.CloseProtocolStream(syncerProto, peerID)
}

// GetBlocks returns a stream of SyncBlock (block + optional receipts/BAL) from given height to peer's latest
func (m *syncPeerClient) GetBlocks(
	peerID peer.ID,
	from uint64,
	timeoutPerBlock time.Duration,
	getReceipts bool,
) (<-chan *SyncBlock, proto.SyncPeerClient, error) {
	clt, err := m.newSyncPeerClient(peerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create sync peer client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	stream, err := clt.GetBlocks(ctx, &proto.GetBlocksRequest{
		From:        from,
		IsValidator: getReceipts,
	})
	if err != nil {
		cancel()

		return nil, nil, fmt.Errorf("failed to open GetBlocks stream: %w", err)
	}

	// input channel
	streamBlockCh, streamErrorCh := blockStreamToChannel(stream)

	// output channel
	blockCh := make(chan *SyncBlock, 1)

	go func() {
		defer cancel()
		defer close(blockCh)

		for {
			select {
			case block, ok := <-streamBlockCh:
				if !ok {
					return
				}

				blockCh <- block
			case err := <-streamErrorCh:
				m.logger.Error("failed to get block from gRPC stream", "peer", peerID, "err", err)

				return
			case <-time.After(timeoutPerBlock):
				m.logger.Warn("block doesn't reach within timeout", "timeout", timeoutPerBlock)

				return
			}
		}
	}()

	return blockCh, clt, nil
}

func (m *syncPeerClient) SyncTxPool(
	peerID peer.ID,
) error {
	clt, err := m.newSyncPeerClient(peerID)
	if err != nil {
		return fmt.Errorf("failed to create sync peer client: %w", err)
	}

	defer m.CloseStream(peerID) //nolint:errcheck

	stream, err := clt.GetTxPool(context.Background(), &emptypb.Empty{})
	if err != nil {
		return fmt.Errorf("failed to open GetTxPool stream: %w", err)
	}

	txsCh, errorCh := fromTransactionStreamToChannel(stream)

	for {
		select {
		case txs, ok := <-txsCh:
			if !ok {
				return nil
			}

			m.addTxsToPool(*txs)
		case err := <-errorCh:
			return fmt.Errorf("failed to get txs from gRPC stream: %w", err)
		}
	}
}

func (m *syncPeerClient) GetReceipts(
	ctx context.Context,
	clt proto.SyncPeerClient,
	peerID peer.ID,
	blockNumber uint64,
) (*ReceiptsMsg, error) {
	resp, err := clt.GetReceipts(ctx, &proto.GetReceiptsRequest{
		BlockNumber: blockNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch receipts for block %d from peer %s: %w",
			blockNumber, peerID, err)
	}

	if resp.BlockNumber != blockNumber {
		return nil, fmt.Errorf("block number mismatch from peer %s: got %d, want %d",
			peerID, resp.BlockNumber, blockNumber)
	}

	msg, err := fromProtoReceipts(resp)
	if err != nil {
		metrics.IncrCounter([]string{syncerMetrics, "bad_receipts"}, 1)

		return nil, fmt.Errorf("failed to decode receipts for block %d: %w", blockNumber, err)
	}

	metrics.SetGauge(
		[]string{syncerMetrics, "ingress_bytes"},
		float32(len(resp.Receipts)),
	)

	return msg, nil
}

func (m *syncPeerClient) addTxsToPool(txs []*types.Transaction) {
	for _, tx := range txs {
		if err := m.txPool.AddTxSync(tx); err != nil {
			m.logger.Error("failed to add tx to pool: %w", err)
		}
	}
}

// newSyncPeerClient creates gRPC client
func (m *syncPeerClient) newSyncPeerClient(peerID peer.ID) (proto.SyncPeerClient, error) {
	conn, err := m.network.NewProtoConnection(syncerProto, peerID)
	if err != nil {
		return nil, fmt.Errorf("failed to open a stream, err %w", err)
	}

	m.network.SaveProtocolStream(syncerProto, conn, peerID)

	return proto.NewSyncPeerClient(conn), nil
}

// fromProto converts a gRPC proto.Block response into a SyncBlock
func fromProto(protoBlock *proto.Block) (*SyncBlock, error) {
	block := &types.Block{}
	if err := block.UnmarshalRLP(protoBlock.Block); err != nil {
		return nil, err
	}

	result := &SyncBlock{
		Block: block,
	}

	return result, nil
}

func fromProtoReceipts(protoReceipts *proto.Receipts) (*ReceiptsMsg, error) {
	receipts := &types.Receipts{}
	if err := receipts.UnmarshalRLP(protoReceipts.Receipts); err != nil {
		return nil, err
	}

	return &ReceiptsMsg{
		BlockNumber: protoReceipts.BlockNumber,
		Receipts:    *receipts,
	}, nil
}

func blockStreamToChannel(stream proto.SyncPeer_GetBlocksClient) (<-chan *SyncBlock, <-chan error) {
	blockCh := make(chan *SyncBlock)
	errorCh := make(chan error, 1)

	go func() {
		defer close(blockCh)

		for {
			protoBlock, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_message"}, 1)

				errorCh <- err

				break
			}

			block, err := fromProto(protoBlock)
			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)

				errorCh <- err

				break
			}

			metrics.SetGauge([]string{syncerMetrics, "ingress_bytes"},
				float32(len(protoBlock.Block))+float32(len(protoBlock.BlockAccessList)))

			blockCh <- block
		}
	}()

	return blockCh, errorCh
}

func fromTransactionStreamToChannel(stream proto.SyncPeer_GetTxPoolClient) (<-chan *types.Transactions, <-chan error) {
	txsCh := make(chan *types.Transactions)
	errorCh := make(chan error, 1)

	go func() {
		defer close(txsCh)

		for {
			protoTxs, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_message"}, 1)

				errorCh <- err

				break
			}

			txs := &types.Transactions{}
			if err := txs.UnmarshalRLP(protoTxs.Txs); err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_tx"}, 1)

				errorCh <- err

				break
			}

			metrics.SetGauge([]string{syncerMetrics, "ingress_bytes"}, float32(len(protoTxs.Txs)))

			txsCh <- txs
		}
	}()

	return txsCh, errorCh
}
