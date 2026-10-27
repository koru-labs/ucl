package export

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/types/buildroot"
)

// Chain is the block store the exporter reads.
type Chain interface {
	// Header returns the head of the chain.
	Header() *types.Header
	GetHeaderByHash(hash types.Hash) (*types.Header, bool)
	// GetBlockByNumber returns the canonical block at a height, with its transactions' senders
	// recovered.
	GetBlockByNumber(number uint64, full bool) (*types.Block, bool)
}

// Executor executes a block on its parent's state, as the chain does when it imports it.
type Executor interface {
	ProcessBlock(parentRoot types.Hash, block *types.Block, blockCreator types.Address) (*state.Transition, error)
}

// Consensus is the part of the consensus engine that takes part in executing a block.
type Consensus interface {
	GetBlockCreator(header *types.Header) (types.Address, error)
	PreCommitState(block *types.Block, txn *state.Transition) error
}

// ErrIrreproducible is returned for a block whose re-execution does not reproduce what ucl stored.
var ErrIrreproducible = errors.New("block re-execution does not match the stored block")

// ErrUnknownBlock is returned for a height the chain does not have.
var ErrUnknownBlock = errors.New("unknown block")

// Exporter builds records, one block at a time, with no cache and no limits (see Service).
type Exporter struct {
	chain     Chain
	executor  Executor
	consensus Consensus
}

func NewExporter(chain Chain, executor Executor, consensus Consensus) *Exporter {
	return &Exporter{chain: chain, executor: executor, consensus: consensus}
}

// Export re-executes block `number` exactly as the chain did when it imported it
// (Blockchain.executeBlockTransactions: the block creator, ProcessBlock on the parent's state root,
// the consensus engine's PreCommitState, Commit), checks the result against the stored header and
// returns its record.
func (e *Exporter) Export(number uint64) (*Record, error) {
	if number == 0 {
		return nil, errors.New("block 0 is not exported: pitau takes it from the converted genesis")
	}

	block, ok := e.chain.GetBlockByNumber(number, true)
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnknownBlock, number)
	}

	header := block.Header

	parent, ok := e.chain.GetHeaderByHash(header.ParentHash)
	if !ok {
		return nil, fmt.Errorf("parent of block %d not found", number)
	}

	creator, err := e.consensus.GetBlockCreator(header)
	if err != nil {
		return nil, fmt.Errorf("block %d creator: %w", number, err)
	}

	txn, err := e.executor.ProcessBlock(parent.StateRoot, block, creator)
	if err != nil {
		return nil, fmt.Errorf("%w: block %d: %w", ErrIrreproducible, number, err)
	}

	if err := e.consensus.PreCommitState(block, txn); err != nil {
		return nil, fmt.Errorf("%w: block %d: pre-commit: %w", ErrIrreproducible, number, err)
	}

	_, root, objs, err := txn.CommitObjects()
	if err != nil {
		return nil, fmt.Errorf("block %d commit: %w", number, err)
	}

	receipts := txn.Receipts()
	if err := verify(block, root, txn.TotalGas(), receipts); err != nil {
		return nil, fmt.Errorf("%w: block %d: %w", ErrIrreproducible, number, err)
	}

	accounts, err := changes(txn.ParentSnapshot(), objs)
	if err != nil {
		return nil, fmt.Errorf("block %d state changes: %w", number, err)
	}

	record := &Record{
		Number:       number,
		Hash:         header.Hash,
		Header:       header.MarshalRLP(),
		Transactions: make([][]byte, len(block.Transactions)),
		Receipts:     make([]Receipt, len(receipts)),
		Accounts:     accounts,
	}

	for i, tx := range block.Transactions {
		record.Transactions[i] = tx.MarshalRLP()
	}

	for i, receipt := range receipts {
		record.Receipts[i] = convertReceipt(receipt)
	}

	return record, nil
}

// verify requires the re-execution to reproduce every root and total in the stored header.
func verify(block *types.Block, root types.Hash, gasUsed uint64, receipts []*types.Receipt) error {
	header := block.Header

	if root != header.StateRoot {
		return fmt.Errorf("state root %s, stored %s", root, header.StateRoot)
	}

	if gasUsed != header.GasUsed {
		return fmt.Errorf("gas used %d, stored %d", gasUsed, header.GasUsed)
	}

	if len(receipts) != len(block.Transactions) {
		return fmt.Errorf("%d receipts for %d transactions", len(receipts), len(block.Transactions))
	}

	if txRoot := buildroot.CalculateTransactionsRoot(block.Transactions, header.Number); txRoot != header.TxRoot {
		return fmt.Errorf("transactions root %s, stored %s", txRoot, header.TxRoot)
	}

	if receiptsRoot := buildroot.CalculateReceiptsRoot(receipts); receiptsRoot != header.ReceiptsRoot {
		return fmt.Errorf("receipts root %s, stored %s", receiptsRoot, header.ReceiptsRoot)
	}

	// IBFT leaves the header's logs bloom empty (jsonrpc's LogQuery.MatchBloom treats a zero bloom
	// as unknown for that reason); the receipts root above covers the logs. A bloom that is set must
	// be the logs'.
	if bloom := types.CreateBloom(receipts); header.LogsBloom != (types.Bloom{}) && bloom != header.LogsBloom {
		return errors.New("logs bloom differs from the stored one")
	}

	return nil
}

func convertReceipt(r *types.Receipt) Receipt {
	out := Receipt{
		Type:              uint8(r.TransactionType),
		Success:           r.Status != nil && *r.Status == types.ReceiptSuccess,
		CumulativeGasUsed: r.CumulativeGasUsed,
		Logs:              make([]Log, len(r.Logs)),
	}

	for i, log := range r.Logs {
		out.Logs[i] = Log{Address: log.Address, Topics: log.Topics, Data: log.Data}
	}

	return out
}

// changes turns the committed objects into account changes, reading every value before the block
// from the parent's state. Objects that changed nothing are left out.
func changes(parent state.Snapshot, objs []*state.Object) ([]AccountChange, error) {
	out := make([]AccountChange, 0, len(objs))

	for _, obj := range objs {
		pre, err := parent.GetAccount(obj.Address)
		if err != nil {
			return nil, fmt.Errorf("account %s before the block: %w", obj.Address, err)
		}

		change := AccountChange{Address: obj.Address}

		if pre != nil {
			change.Before = &Account{
				Nonce:    pre.Nonce,
				Balance:  pre.Balance,
				CodeHash: types.BytesToHash(pre.CodeHash),
			}
		}

		if !obj.Deleted {
			change.After = &Account{Nonce: obj.Nonce, Balance: obj.Balance, CodeHash: obj.CodeHash}

			if obj.DirtyCode {
				change.Code = obj.Code
			}

			preRoot := types.EmptyRootHash
			if pre != nil {
				preRoot = pre.Root
			}

			// The commit builds the account's storage on obj.Root, the root the block started it
			// from: the parent's, unless the block destroyed the account and created it again, which
			// starts it empty (state.Txn.CreateAccount). Then every slot of the parent's storage is
			// gone, and the slots the block wrote are the whole storage after it.
			change.StorageReset = obj.Root != preRoot

			for _, slot := range obj.Storage {
				key := types.BytesToHash(slot.Key)
				before := parent.GetStorage(obj.Address, preRoot, key)

				after := types.ZeroHash
				if !slot.Deleted {
					after = types.BytesToHash(slot.Val)
				}

				if before != after || (change.StorageReset && after != types.ZeroHash) {
					change.Storage = append(change.Storage, SlotChange{Slot: key, Before: before, After: after})
				}
			}
		}

		if unchanged(&change) {
			continue
		}

		out = append(out, change)
	}

	return out, nil
}

// unchanged reports whether a change leaves the account as it was.
func unchanged(c *AccountChange) bool {
	if len(c.Storage) != 0 || len(c.Code) != 0 || c.StorageReset {
		return false
	}

	if c.Before == nil || c.After == nil {
		return c.Before == nil && c.After == nil
	}

	return c.Before.Nonce == c.After.Nonce &&
		c.Before.Balance.Cmp(c.After.Balance) == 0 &&
		bytes.Equal(c.Before.CodeHash.Bytes(), c.After.CodeHash.Bytes())
}
