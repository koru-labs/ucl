package syncer

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/0xPolygon/polygon-edge/network/grpc"
	"github.com/0xPolygon/polygon-edge/syncer/proto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/hashicorp/go-metrics"
)

var (
	ErrBlockNotFound = errors.New("block not found")
)

type syncPeerService struct {
	proto.UnimplementedSyncPeerServer

	blockchain Blockchain       // reference to the blockchain module
	network    Network          // reference to the network module
	txPool     TxPool           // reference to the txpool module
	stream     *grpc.GrpcStream // reference to the grpc stream
}

func NewSyncPeerService(
	network Network,
	blockchain Blockchain,
	txPool TxPool,
) SyncPeerService {
	return &syncPeerService{
		blockchain: blockchain,
		network:    network,
		txPool:     txPool,
	}
}

// Start starts syncPeerService
func (s *syncPeerService) Start() {
	s.setupGRPCServer()
}

// Close closes syncPeerService
func (s *syncPeerService) Close() error {
	return s.stream.Close()
}

// setupGRPCServer setup GRPC server
func (s *syncPeerService) setupGRPCServer() {
	s.stream = grpc.NewGrpcStream()

	proto.RegisterSyncPeerServer(s.stream.GrpcServer(), s)
	s.stream.Serve()
	s.network.RegisterProtocol(syncerProto, s.stream)
}

// GetBlocks is a gRPC endpoint to return blocks from the specific height via stream
func (s *syncPeerService) GetBlocks(
	req *proto.GetBlocksRequest,
	stream proto.SyncPeer_GetBlocksServer,
) error {
	// from to latest
	for i := req.From; i <= s.blockchain.Header().Number; i++ {
		block, ok := s.blockchain.GetBlockByNumber(i, true)
		if !ok {
			return ErrBlockNotFound
		}

		receipts, err := s.blockchain.GetReceiptsByHash(block.Number(), block.Hash())
		if err != nil {
			receipts = nil
		}

		resp := toProtoBlock(block, receipts)
		metrics.SetGauge([]string{syncerMetrics, "egress_bytes"}, float32(len(resp.Block)))

		// if client closes stream, context.Canceled is given
		if err := stream.Send(resp); err != nil {
			break
		}
	}

	return nil
}

// GetStatus is a gRPC endpoint to return the latest block number as a node status
func (s *syncPeerService) GetStatus(
	ctx context.Context,
	req *empty.Empty,
) (*proto.SyncPeerStatus, error) {
	var number uint64
	if header := s.blockchain.Header(); header != nil {
		number = header.Number
	}

	return &proto.SyncPeerStatus{
		Number: number,
	}, nil
}

func (s *syncPeerService) GetTxPool(req *empty.Empty, stream proto.SyncPeer_GetTxPoolServer) error {
	if s.txPool == nil {
		return nil
	}

	allTxs := s.txPool.GetAllTxs()

	// maxBatchSize batch size is 10k
	const maxBatchSize = 10000

	arrSize := len(allTxs)

	for i := range int(math.Ceil(float64(arrSize) / maxBatchSize)) {
		start := i * maxBatchSize

		batchSize := arrSize - start
		if batchSize > maxBatchSize {
			batchSize = maxBatchSize
		}

		end := start + batchSize

		if err := sendTxPoolBatch(allTxs[start:end], stream); err != nil {
			return err
		}
	}

	return nil
}

func (s *syncPeerService) GetReceipts(
	ctx context.Context,
	req *proto.GetReceiptsRequest,
) (*proto.Receipts, error) {
	block, ok := s.blockchain.GetBlockByNumber(req.BlockNumber, true)
	if !ok {
		return nil, ErrBlockNotFound
	}

	receipts, err := s.blockchain.GetReceiptsByHash(block.Number(), block.Hash())
	if err != nil {
		if errors.Is(err, fmt.Errorf("not found")) {
			return &proto.Receipts{
				BlockNumber: block.Number(),
				Receipts:    nil,
				Received:    false,
			}, nil
		}

		return nil, err
	}

	var receiptsBytes []byte
	if len(receipts) > 0 {
		receiptsBytes = types.Receipts(receipts).MarshalRLPTo(nil)
	}

	metrics.SetGauge([]string{syncerMetrics, "egress_bytes"}, float32(len(receiptsBytes)))

	return &proto.Receipts{
		BlockNumber: block.Number(),
		Receipts:    receiptsBytes,
		Received:    true,
	}, nil
}

func sendTxPoolBatch(txs types.Transactions, stream proto.SyncPeer_GetTxPoolServer) error {
	txPool := &proto.Transactions{
		Txs: txs.MarshalRLPTo(nil),
	}

	metrics.SetGauge([]string{syncerMetrics, "egress_bytes"}, float32(len(txPool.Txs)))

	return stream.Send(txPool)
}

// toProtoBlock converts type.Block -> proto.Block
func toProtoBlock(block *types.Block, receipts types.Receipts) *proto.Block {
	resp := &proto.Block{
		Block: block.MarshalRLP(),
	}

	if len(block.BlockAccessRecord) > 0 {
		resp.BlockAccessList = block.MarshalRLP()
	}

	return resp
}
