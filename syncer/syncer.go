package syncer

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/helper/progress"
	"github.com/0xPolygon/polygon-edge/network/event"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-metrics"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	syncerName  = "syncer"
	syncerProto = "/syncer/0.2"
)

var (
	errTimeout = errors.New("timeout awaiting block from peer")
)

// XXX: Don't use this syncer for the consensus that may cause fork.
// This syncer doesn't assume forks
type syncer struct {
	logger          hclog.Logger
	blockchain      Blockchain
	syncProgression Progression

	peerMap         *PeerMap
	syncPeerService SyncPeerService
	syncPeerClient  SyncPeerClient

	// Timeout for syncing a block
	blockTimeout time.Duration

	// Channel to notify Sync that a new status arrived
	newStatusCh chan struct{}

	forkManager forkManagerInterface

	enableBlockAccessList bool

	list                       *list.List
	mu                         sync.Mutex
	ch                         chan struct{}
	shutDownCh                 chan struct{}
	currentReceiptsBlockNumber uint64
}

type forkManagerInterface interface {
	GetSigner(uint64) (signer.Signer, error)
	GetValidators(uint64) (validators.Validators, error)
}

func NewSyncer(
	logger hclog.Logger,
	network Network,
	blockchain Blockchain,
	txPool TxPool,
	blockTimeout time.Duration,
	forkManager forkManagerInterface,
	enableBlockAccessList bool,
) Syncer {
	currentReceiptsBlockNumber := blockchain.GetLastSyncReceiptsBlock() + 1

	return &syncer{
		logger:                     logger.Named(syncerName),
		blockchain:                 blockchain,
		syncProgression:            progress.NewProgressionWrapper(progress.ChainSyncBulk),
		syncPeerService:            NewSyncPeerService(network, blockchain, txPool),
		syncPeerClient:             NewSyncPeerClient(logger, network, blockchain, txPool),
		blockTimeout:               blockTimeout,
		newStatusCh:                make(chan struct{}),
		peerMap:                    new(PeerMap),
		forkManager:                forkManager,
		enableBlockAccessList:      enableBlockAccessList,
		list:                       list.New(),
		ch:                         make(chan struct{}, 1),
		mu:                         sync.Mutex{},
		currentReceiptsBlockNumber: currentReceiptsBlockNumber,
	}
}

// Start starts goroutine processes
func (s *syncer) Start() error {
	if err := s.syncPeerClient.Start(); err != nil {
		return err
	}

	s.syncPeerService.Start()

	s.initializePeerMap()

	go s.startPeerStatusUpdateProcess()
	go s.startPeerConnectionEventProcess()

	return nil
}

// Close terminates goroutine processes
func (s *syncer) Close() error {
	close(s.newStatusCh)

	if err := s.syncPeerService.Close(); err != nil {
		return err
	}

	s.syncPeerClient.Close()

	return nil
}

// initializePeerMap fetches peer statuses and initializes map
func (s *syncer) initializePeerMap() {
	peerStatuses := s.syncPeerClient.GetConnectedPeerStatuses()
	s.peerMap.Put(peerStatuses...)
}

// startPeerStatusUpdateProcess subscribes peer status change event and updates peer map
func (s *syncer) startPeerStatusUpdateProcess() {
	for peerStatus := range s.syncPeerClient.GetPeerStatusUpdateCh() {
		s.putToPeerMap(peerStatus)
	}
}

// startPeerConnectionEventProcess processes peer connection change events
func (s *syncer) startPeerConnectionEventProcess() {
	for e := range s.syncPeerClient.GetPeerConnectionUpdateEventCh() {
		peerID := e.PeerID

		switch e.Type {
		case event.PeerConnected:
			go s.initNewPeerStatus(peerID)
		case event.PeerDisconnected:
			s.removeFromPeerMap(peerID)
		}
	}
}

// initNewPeerStatus fetches status of the peer and put to peer map
func (s *syncer) initNewPeerStatus(peerID peer.ID) {
	status, err := s.syncPeerClient.GetPeerStatus(peerID)
	if err != nil {
		s.logger.Warn("failed to get peer status, skip", "id", peerID, "err", err)

		return
	}

	s.putToPeerMap(status)
}

// putToPeerMap puts given status to peer map
func (s *syncer) putToPeerMap(status *NoForkPeer) {
	s.peerMap.Put(status)
	s.notifyNewStatusEvent()
}

// removeFromPeerMap removes the peer from peer map
func (s *syncer) removeFromPeerMap(peerID peer.ID) {
	s.peerMap.Remove(peerID)
}

// notifyNewStatusEvent emits signal to newStatusCh
func (s *syncer) notifyNewStatusEvent() {
	select {
	case s.newStatusCh <- struct{}{}:
	default:
	}
}

// GetSyncProgression returns progression
func (s *syncer) GetSyncProgression() *progress.Progression {
	return s.syncProgression.GetProgression()
}

// HasSyncPeer returns whether syncer has the peer to syncs blocks
// return false if syncer has no peer whose latest block height doesn't exceed local height
func (s *syncer) HasSyncPeer() bool {
	bestPeer := s.peerMap.BestPeer(nil)
	header := s.blockchain.Header()

	return bestPeer != nil && bestPeer.Number > header.Number
}

// SyncTxPool syncs tx pool with the best peer and returns error if failed
func (s *syncer) SyncTxPool() error {
	s.initializePeerMap()

	bestPeer := s.peerMap.BestPeer(nil)
	if bestPeer == nil {
		timeCh, ticker := time.After(20*time.Second), time.Tick(2*time.Second)

	loop:
		for {
			select {
			case <-ticker:
				s.initializePeerMap()

				bestPeer = s.peerMap.BestPeer(nil)
				if bestPeer != nil {
					break loop
				}
			case <-timeCh:
				return fmt.Errorf("no best peer found")
			}
		}
	}

	return s.syncPeerClient.SyncTxPool(bestPeer.ID)
}

// Sync syncs block with the best peer until callback returns true
func (s *syncer) Sync(callback func(*types.FullBlock) bool) error {
	localLatest := s.blockchain.Header().Number
	skipList := make(map[peer.ID]bool)

	for {
		// Wait for a new event to arrive
		select {
		case <-s.newStatusCh:
			s.logger.Debug("new peer status arrived, start syncing")
		case <-time.After(s.blockTimeout):
			s.logger.Debug("timeout while waiting for new peer status, start manual syncing")
			s.initializePeerMap() // fetch peer statuses just in case
		}

		// fetch local latest block
		if header := s.blockchain.Header(); header != nil {
			localLatest = header.Number
		}

		// pick one best peer
		bestPeer := s.peerMap.BestPeer(skipList)
		if bestPeer == nil {
			// Empty skipList map if there are no best peers
			skipList = make(map[peer.ID]bool)

			continue
		}

		// if the bestPeer does not have a new block continue
		if bestPeer.Number <= localLatest {
			continue
		}

		// fetch block from the peer
		lastNumber, shouldTerminate, err := s.bulkSyncWithPeer(bestPeer.ID, bestPeer.Number, callback)
		if err != nil {
			s.logger.Warn("failed to complete bulk sync with peer, try to next one", "peer ID", "error", bestPeer.ID, err)
		}

		if lastNumber < bestPeer.Number {
			skipList[bestPeer.ID] = true

			// continue to next peer
			continue
		}

		if shouldTerminate {
			break
		}
	}

	return nil
}

// bulkSyncWithPeer syncs block with a given peer
func (s *syncer) bulkSyncWithPeer(peerID peer.ID, peerLatestBlock uint64,
	newBlockCallback func(*types.FullBlock) bool) (uint64, bool, error) {
	localLatest := s.blockchain.Header().Number
	shouldTerminate := false

	getReceipts := s.enableBlockAccessList && !s.isLocalValidator(localLatest+1)

	blockCh, clt, err := s.syncPeerClient.GetBlocks(peerID, localLatest+1, s.blockTimeout, getReceipts)
	if err != nil {
		return 0, false, err
	}

	// Create a blockchain subscription for the sync progression and start tracking
	subscription := s.blockchain.SubscribeEvents()
	s.syncProgression.StartProgression(localLatest+1, subscription)
	s.syncProgression.UpdateHighestProgression(peerLatestBlock)

	receiptsStopChannel := make(chan struct{})
	s.shutDownCh = make(chan struct{})

	receiptsCtx, receiptsCancel := context.WithCancel(context.Background())

	defer func() {
		if getReceipts {
			receiptsCancel()
			close(s.shutDownCh)
			<-receiptsStopChannel
		}

		err := s.syncPeerClient.CloseStream(peerID)
		if err != nil {
			s.logger.Error("Failed to close stream: ", err)
		}

		// Stop monitoring the sync progression upon exit
		s.syncProgression.StopProgression()
		s.blockchain.UnsubscribeEvents(subscription)
	}()

	if getReceipts {
		go func() {
			for {
				var header *types.Header

				if s.currentReceiptsBlockNumber <= localLatest {
					currentBlock, _ := s.blockchain.GetBlockByNumber(s.currentReceiptsBlockNumber, false)

					header = currentBlock.Header
				} else {
					header = s.getBlock()
				}

				select {
				case <-s.shutDownCh:
					receiptsStopChannel <- struct{}{}

					return
				default:
				}

				receipts, err := s.syncPeerClient.GetReceipts(receiptsCtx, clt, peerID, header.Number)
				if err != nil {
					s.logger.Error("failed to get receipts", "blockNumber", header.Number, "peerID", peerID, "err", err)

					continue
				}

				if !receipts.Received {
					time.Sleep(1 * time.Second)

					continue
				}

				if err := s.blockchain.VerifyAndApplyReceipts(header, receipts.Receipts); err != nil {
					s.logger.Error("failed to get receipts", "blockNumber", header.Number, "peerID", peerID, "err", err)

					time.Sleep(1 * time.Second)

					continue
				}

				s.currentReceiptsBlockNumber++
			}
		}()
	}

	var lastReceivedNumber uint64

	for {
		select {
		case syncBlock, ok := <-blockCh:
			if !ok {
				return lastReceivedNumber, shouldTerminate, nil
			}

			// safe check
			if syncBlock.Block.Number() == 0 || syncBlock.Block.Number() <= s.blockchain.Header().Number {
				continue
			}

			fullBlock, err := s.applyBlock(syncBlock, getReceipts)
			if err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)

				return lastReceivedNumber, false, fmt.Errorf("unable to verify block, %w", err)
			}

			if err := s.blockchain.WriteFullBlock(fullBlock, syncerName); err != nil {
				metrics.IncrCounter([]string{syncerMetrics, "bad_block"}, 1)

				return lastReceivedNumber, false, fmt.Errorf("failed to write block while bulk syncing: %w", err)
			}

			updateMetrics(fullBlock)
			shouldTerminate = newBlockCallback(fullBlock)

			lastReceivedNumber = syncBlock.Block.Number()
		case <-time.After(s.blockTimeout):
			return lastReceivedNumber, shouldTerminate, errTimeout
		}
	}
}

func (s *syncer) applyBlock(syncBlock *SyncBlock, getReceipts bool) (*types.FullBlock, error) {
	if !getReceipts {
		fullBlock, err := s.blockchain.VerifyFinalizedBlock(syncBlock.Block)

		return fullBlock, err
	}

	fullBlock, err := s.blockchain.ApplyFinalizedBlockFromBAL(
		syncBlock.Block,
		syncBlock.BlockAccessList,
		s,
	)

	return fullBlock, err
}

func (s *syncer) isLocalValidator(blockNumber uint64) bool {
	sgn, err := s.forkManager.GetSigner(blockNumber)
	if err != nil {
		return false
	}

	vals, err := s.forkManager.GetValidators(blockNumber)
	if err != nil {
		return false
	}

	return vals.Includes(sgn.Address())
}

func (s *syncer) AddBlock(header *types.Header) {
	s.mu.Lock()

	s.list.PushBack(header)

	select {
	case s.ch <- struct{}{}:
	default:
	}

	s.mu.Unlock()
}

func (s *syncer) getBlock() *types.Header {
	s.mu.Lock()

	for s.list.Len() == 0 {
		s.mu.Unlock()

		select {
		case <-s.ch:
			select {
			case <-s.shutDownCh:
				return nil
			default:
			}

			s.mu.Lock()
		case <-s.shutDownCh:
			return nil
		}
	}

	front := s.list.Front()
	header, _ := s.list.Remove(front).(*types.Header)

	s.mu.Unlock()

	return header
}

func updateMetrics(fullBlock *types.FullBlock) {
	metrics.SetGauge([]string{syncerMetrics, "tx_num"}, float32(len(fullBlock.Block.Transactions)))
	metrics.SetGauge([]string{syncerMetrics, "receipts_num"}, float32(len(fullBlock.Receipts)))
	metrics.SetGauge([]string{syncerMetrics, "blocks_num"}, 1)
}
