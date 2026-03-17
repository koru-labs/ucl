package jsonrpc

import (
	"time"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/jellydator/ttlcache/v3"
)

type blockCache struct {
	fullBlock *types.FullBlock

	txHashToIndex map[types.Hash]int
	logIndex      []int
}

type rpcCache struct {
	blockCache *ttlcache.Cache[uint64, *blockCache]

	store  ethStore
	logger hclog.Logger
}

func initRPCCache(store ethStore, logger hclog.Logger, ttl time.Duration, capacity uint64) *rpcCache {
	cache := ttlcache.New[uint64, *blockCache](
		ttlcache.WithTTL[uint64, *blockCache](ttl),
		ttlcache.WithCapacity[uint64, *blockCache](capacity),
	)

	go cache.Start() // starts automatic expired item deletion

	return &rpcCache{blockCache: cache, store: store, logger: logger}
}

func (r *rpcCache) getBlockCache(num uint64) *blockCache {
	r.logger.Debug("rpccache", "getBlockCache called with block number", num)

	if r.blockCache.Has(num) {
		return r.blockCache.Get(num).Value()
	}

	// load block
	block, ok := r.store.GetBlockByNumber(num, true)
	if !ok {
		return nil
	}

	// load receipts
	receipts, err := r.store.GetReceiptsByHash(num, block.Hash())
	if err != nil {
		// block receipts not found
		return nil
	}

	fullBlock := &types.FullBlock{Block: block, Receipts: receipts}
	retVal := &blockCache{fullBlock: fullBlock}

	// don't cache small blocks, so return at this place with block and receipts
	if len(block.Transactions) < 10 {
		return retVal
	}

	txHashToIndex := make(map[types.Hash]int, len(block.Transactions))
	logIndex := make([]int, len(block.Transactions))
	index := 0
	// calculate txs indexes and receipts offset
	for i, txn := range block.Transactions {
		txHashToIndex[txn.Hash] = i
		logIndex[i] = index
		index += len(receipts[i].Logs)
	}

	retVal.txHashToIndex = txHashToIndex
	retVal.logIndex = logIndex

	r.blockCache.Set(num, retVal, ttlcache.DefaultTTL)
	r.logger.Debug("rpccache", "added to cache block with number", num)

	return retVal
}

func (b *blockCache) getBlock() *types.Block {
	return b.fullBlock.Block
}

func (b *blockCache) getReceipts() []*types.Receipt {
	return b.fullBlock.Receipts
}

func (b *blockCache) getTransaction(hash types.Hash) (*types.Transaction, int) {
	if b.txHashToIndex != nil {
		index, ok := b.txHashToIndex[hash]
		if !ok {
			return nil, -1
		}

		tx := b.fullBlock.Block.Transactions[index]

		return tx, index
	}

	return types.FindTxByHash(b.fullBlock.Block.Transactions, hash)
}

func (b *blockCache) getLogIndex(txIndex int) int {
	if txIndex < 0 {
		return -1
	}

	if b.logIndex != nil {
		if txIndex < len(b.logIndex) {
			return b.logIndex[txIndex]
		} else {
			return -1
		}
	}

	logIndex := 0
	for i := 0; i < txIndex; i++ {
		// accumulate receipt logs indexes from block transactions
		// that are before the desired transaction
		logIndex += len(b.fullBlock.Receipts[i].Logs)
	}

	return logIndex
}
