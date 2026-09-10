package blockchain

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/0xPolygon/polygon-edge/types"
)

// UnwoundBlock is one canonical block removed from the head.
type UnwoundBlock struct {
	Number   uint64       `json:"number"`
	Hash     types.Hash   `json:"hash"`
	TxHashes []types.Hash `json:"txHashes"`
}

// UnwindResult is the outcome of moving HEAD backward.
type UnwindResult struct {
	FromNumber uint64         `json:"fromNumber"`
	FromHash   types.Hash     `json:"fromHash"`
	ToNumber   uint64         `json:"toNumber"`
	ToHash     types.Hash     `json:"toHash"`
	Removed    []UnwoundBlock `json:"removed"`
}

// Unwind drops the last `blocks` canonical blocks and points HEAD at the parent.
// Header/body/receipt bytes are left in the DB as orphans; canonical and tx
// lookups for the dropped heights are deleted. Does not modify the state trie
// (the new head's StateRoot is already stored).
func Unwind(db *storage.Storage, blocks uint64) (*UnwindResult, error) {
	if blocks == 0 {
		return nil, fmt.Errorf("blocks must be greater than 0")
	}

	headNum, ok := db.ReadHeadNumber()
	if !ok {
		return nil, fmt.Errorf("head number not found")
	}

	if blocks > headNum {
		return nil, fmt.Errorf("cannot unwind %d blocks: head is %d (genesis is 0)", blocks, headNum)
	}

	return UnwindTo(db, headNum-blocks)
}

// UnwindTo sets the canonical head to `target` (inclusive).
func UnwindTo(db *storage.Storage, target uint64) (*UnwindResult, error) {
	headNum, ok := db.ReadHeadNumber()
	if !ok {
		return nil, fmt.Errorf("head number not found")
	}

	headHash, ok := db.ReadHeadHash()
	if !ok {
		return nil, fmt.Errorf("head hash not found")
	}

	if target > headNum {
		return nil, fmt.Errorf("target %d is above current head %d", target, headNum)
	}

	if target == headNum {
		return nil, fmt.Errorf("head is already %d", headNum)
	}

	removed, targetHeader, err := collectUnwind(db, headNum, headHash, target)
	if err != nil {
		return nil, err
	}

	if err := applyUnwind(db, targetHeader, removed); err != nil {
		return nil, err
	}

	return &UnwindResult{
		FromNumber: headNum,
		FromHash:   headHash,
		ToNumber:   targetHeader.Number,
		ToHash:     targetHeader.Hash,
		Removed:    removed,
	}, nil
}

// PlanUnwind reports what UnwindTo would remove without writing.
func PlanUnwind(db *storage.Storage, target uint64) (*UnwindResult, error) {
	headNum, ok := db.ReadHeadNumber()
	if !ok {
		return nil, fmt.Errorf("head number not found")
	}

	headHash, ok := db.ReadHeadHash()
	if !ok {
		return nil, fmt.Errorf("head hash not found")
	}

	if target >= headNum {
		return nil, fmt.Errorf("target %d must be below current head %d", target, headNum)
	}

	removed, targetHeader, err := collectUnwind(db, headNum, headHash, target)
	if err != nil {
		return nil, err
	}

	return &UnwindResult{
		FromNumber: headNum,
		FromHash:   headHash,
		ToNumber:   targetHeader.Number,
		ToHash:     targetHeader.Hash,
		Removed:    removed,
	}, nil
}

func collectUnwind(
	db *storage.Storage,
	headNum uint64,
	headHash types.Hash,
	target uint64,
) ([]UnwoundBlock, *types.Header, error) {
	removed := make([]UnwoundBlock, 0, headNum-target)
	curNum, curHash := headNum, headHash

	for curNum > target {
		header, err := db.ReadHeader(curNum, curHash)
		if err != nil {
			return nil, nil, fmt.Errorf("read header %d (%s): %w", curNum, curHash, err)
		}

		// Header.Hash is not stored in RLP. Use the lookup key, not a recomputed
		// hash — IBFT substitutes HeaderHash and a default keccak would be wrong.
		header.Hash = curHash

		canon, ok := db.ReadCanonicalHash(curNum)
		if !ok || canon != curHash {
			return nil, nil, fmt.Errorf("head chain is not canonical at %d", curNum)
		}

		txHashes := []types.Hash{}

		if body, bodyErr := db.ReadBody(curNum, curHash); bodyErr == nil {
			for _, tx := range body.Transactions {
				if tx.Hash == types.ZeroHash {
					tx.ComputeHash(curNum)
				}

				txHashes = append(txHashes, tx.Hash)
			}
		}

		removed = append(removed, UnwoundBlock{
			Number:   curNum,
			Hash:     curHash,
			TxHashes: txHashes,
		})

		curHash = header.ParentHash
		curNum--
	}

	targetHeader, err := db.ReadHeader(curNum, curHash)
	if err != nil {
		return nil, nil, fmt.Errorf("read target header %d (%s): %w", curNum, curHash, err)
	}

	targetHeader.Hash = curHash

	if canon, ok := db.ReadCanonicalHash(curNum); !ok || canon != curHash {
		return nil, nil, fmt.Errorf("target %d is not on the canonical chain", curNum)
	}

	return removed, targetHeader, nil
}

func applyUnwind(db *storage.Storage, target *types.Header, removed []UnwoundBlock) error {
	batch := db.NewWriter()

	for _, block := range removed {
		batch.DeleteCanonicalHash(block.Number)
		batch.DeleteBlockLookup(block.Hash)

		for _, txHash := range block.TxHashes {
			batch.DeleteTxLookup(txHash)
		}
	}

	td, ok := db.ReadTotalDifficulty(target.Number, target.Hash)
	if !ok {
		return fmt.Errorf("total difficulty missing for target %d", target.Number)
	}

	batch.PutHeadHash(target.Hash)
	batch.PutHeadNumber(target.Number)
	batch.PutCanonicalHash(target.Number, target.Hash)
	batch.PutTotalDifficulty(target.Number, target.Hash, td)

	return batch.WriteBatch()
}
