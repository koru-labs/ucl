package ibft

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"sync"
	"time"

	"github.com/0xPolygon/go-ibft/messages"
	"github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/blockstm"
	sgn "github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	"github.com/0xPolygon/polygon-edge/observability"
	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/state/stm"
	"github.com/0xPolygon/polygon-edge/types"
	iradix "github.com/hashicorp/go-immutable-radix"
	"github.com/hashicorp/go-metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type sealKey struct {
	height uint64
	hash   types.Hash
}

// sealEntry carries per-proposal state from BuildProposal to InsertProposal,
// which don't thread a context between them.
type sealEntry struct {
	start time.Time
	span  trace.Span
}

// sealTimeStore tracks per-proposal state across the BuildProposal ->
// InsertProposal callbacks. It is safe for concurrent use, tolerates lookup
// misses, and evicts stale entries to bound memory.
type sealTimeStore struct {
	mu     sync.Mutex
	starts map[sealKey]sealEntry
}

func newSealTimeStore() *sealTimeStore {
	return &sealTimeStore{starts: make(map[sealKey]sealEntry)}
}

func (s *sealTimeStore) store(height uint64, hash types.Hash, entry sealEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := sealKey{height, hash}

	if old, ok := s.starts[key]; ok && old.span != nil {
		old.span.End()
	}

	s.starts[key] = entry
}

// take returns the entry for (height, hash) if present, deleting it. It also
// evicts every other entry — those belong to losing proposals of already-decided
// rounds that can never match again — and ends their orphaned root spans so they
// don't leak.
func (s *sealTimeStore) take(height uint64, hash types.Hash) (sealEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	want := sealKey{height, hash}
	entry, ok := s.starts[want]

	for k, e := range s.starts {
		if k != want && e.span != nil {
			e.span.End()
		}
	}

	clear(s.starts)

	return entry, ok
}

func (i *backendIBFT) BuildProposal(view *proto.View) []byte {
	start := time.Now().UTC()

	ctx, rootSpan := observability.Tracer().Start(
		context.Background(),
		"block.produce",
		trace.WithAttributes(attribute.Int64("block.number", int64(view.Height))),
	)

	var (
		latestHeader      = i.blockchain.Header()
		latestBlockNumber = latestHeader.Number
	)

	if latestBlockNumber+1 != view.Height {
		i.logger.Error(
			"unable to build block, due to lack of parent block",
			"num",
			latestBlockNumber,
		)

		rootSpan.SetStatus(codes.Error, "missing parent block")
		rootSpan.End()

		return nil
	}

	block, receipts, err := i.buildBlock(ctx, latestHeader)
	if err != nil {
		i.logger.Error("cannot build block", "num", view.Height, "err", err)

		rootSpan.RecordError(err)
		rootSpan.SetStatus(codes.Error, err.Error())
		rootSpan.End()

		return nil
	}

	i.blockchain.AddReceiptsToCache(block.Hash(), receipts)

	i.sealTimes.store(view.Height, block.Hash(), sealEntry{start: start, span: rootSpan})

	return block.MarshalRLP()
}

// InsertProposal inserts a proposal of which the consensus has been got
func (i *backendIBFT) InsertProposal(
	proposal *proto.Proposal,
	committedSeals []*messages.CommittedSeal,
) {
	newBlock := &types.Block{}
	if err := newBlock.UnmarshalRLP(proposal.RawProposal); err != nil {
		i.logger.Error("cannot unmarshal proposal", "err", err)

		return
	}

	// Capture the proposed identity before WriteCommittedSeals mutates the
	// header (and therefore the hash); needed to match the BuildProposal start.
	proposedNumber := newBlock.Number()
	proposedHash := newBlock.Hash()

	committedSealsMap := make(map[types.Address][]byte, len(committedSeals))

	for _, cm := range committedSeals {
		committedSealsMap[types.BytesToAddress(cm.Signer)] = cm.Signature
	}

	// Copy extra data for debugging purposes
	extraDataBackup := slices.Clone(newBlock.Header.ExtraData)

	signer, validators, hooks, err := getModulesFromForkManager(i.forkManager, proposedNumber)
	if err != nil {
		i.logger.Error("cannot get modules from fork manager for", "block number", proposedNumber, "err", err)

		return
	}

	// Push the committed seals to the header
	header, err := signer.WriteCommittedSeals(newBlock.Header, proposal.Round, committedSealsMap)
	if err != nil {
		i.logger.Error("cannot write committed seals", "err", err)

		return
	}

	// WriteCommittedSeals alters the extra data before writing the block
	// It doesn't handle errors while pushing changes which can result in
	// corrupted extra data.
	// We don't know exact circumstance of the unmarshalRLP error
	// This is a safety net to help us narrow down and also recover before
	// writing the block
	if err := i.ValidateExtraDataFormat(newBlock.Header); err != nil {
		// Format committed seals to make them more readable
		committedSealsStr := make([]string, len(committedSeals))
		for idx, seal := range committedSeals {
			committedSealsStr[idx] = fmt.Sprintf("{signer=%v signature=%v}",
				hex.EncodeToHex(seal.Signer),
				hex.EncodeToHex(seal.Signature))
		}

		i.logger.Error("cannot write block: corrupted extra data",
			"err", err,
			"before", hex.EncodeToHex(extraDataBackup),
			"after", hex.EncodeToHex(header.ExtraData),
			"committedSeals", committedSealsStr)

		return
	}

	newBlock.Header = header

	// Save the block locally
	commitStart := time.Now().UTC()

	if err := i.blockchain.WriteBlock(newBlock, "consensus"); err != nil {
		i.logger.Error("cannot write block", "err", err)

		return
	}

	metrics.MeasureSince([]string{consensusMetrics, "span", "commit"}, commitStart)
	metrics.SetGauge([]string{consensusMetrics, "block_size_bytes"}, float32(len(proposal.RawProposal)))

	logger := i.logger

	if entry, ok := i.sealTimes.take(proposedNumber, proposedHash); ok {
		metrics.MeasureSince([]string{consensusMetrics, "finality", "seal_total"}, entry.start)

		ctx := trace.ContextWithSpan(context.Background(), entry.span)

		_, commitSpan := observability.Tracer().Start(ctx, "commit", trace.WithTimestamp(commitStart))
		commitSpan.End()

		entry.span.End()

		logger = logger.With(observability.LogFields(ctx)...)
	}

	i.updateMetrics(newBlock)

	logger.Info(
		"block committed",
		"number", newBlock.Number(),
		"hash", newBlock.Hash(),
		"validation_type", signer.Type(),
		"validators", validators.Len(),
		"committed", len(committedSeals),
	)

	if err := hooks.PostInsertBlock(newBlock); err != nil {
		i.logger.Error(
			"failed to call PostInsertBlock hook",
			"height", newBlock.Number(),
			"hash", newBlock.Hash(),
			"err", err,
		)

		return
	}

	// after the block has been written we reset the txpool so that
	// the old transactions are removed
	i.txpool.ResetWithBlock(newBlock)
}

func (i *backendIBFT) ID() []byte {
	// signer for pending block
	signer, err := i.forkManager.GetSigner(i.blockchain.Header().Number + 1)
	if err != nil {
		i.logger.Error("cannot get signer for pending block", "err", err)

		return []byte{}
	}

	return signer.Address().Bytes()
}

func (i *backendIBFT) MaximumFaultyNodes() uint64 {
	// validators for pending block
	validators, err := i.forkManager.GetValidators(i.blockchain.Header().Number + 1)
	if err != nil {
		i.logger.Error("cannot get validators for pending block", "err", err)

		return 0
	}

	return uint64(calcMaxFaultyNodes(validators))
}

// DISCLAIMER: IBFT will be deprecated so we set 1 as a voting power to all validators
func (i *backendIBFT) GetVotingPowers(height uint64) (map[string]*big.Int, error) {
	validators, err := i.forkManager.GetValidators(height)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*big.Int, validators.Len())

	for index := 0; index < validators.Len(); index++ {
		strAddress := types.AddressToString(validators.At(uint64(index)).Addr())
		result[strAddress] = big.NewInt(1) // set 1 as voting power to everyone
	}

	return result, nil
}

// buildBlock builds the block, based on the passed in snapshot and parent header
func (i *backendIBFT) buildBlock(ctx context.Context, parent *types.Header) (*types.Block, []*types.Receipt, error) {
	ctx, buildSpan := observability.Tracer().Start(ctx, "build")
	defer buildSpan.End()

	defer metrics.MeasureSince([]string{consensusMetrics, "span", "build"}, time.Now())

	// tests construct backendIBFT as a struct literal, so the engine is not always
	// wired up by the constructor
	if i.stmEngine == nil {
		i.stmEngine = stm.NewEngine(stm.EngineConfig{}, i.logger)
	}

	header := &types.Header{
		ParentHash: parent.Hash,
		Number:     parent.Number + 1,
		Miner:      types.ZeroAddress.Bytes(),
		Nonce:      types.Nonce{},
		MixHash:    sgn.IstanbulDigest,
		// this is required because blockchain needs difficulty to organize blocks and forks
		Difficulty: parent.Number + 1,
		StateRoot:  types.EmptyRootHash, // this avoids needing state for now
		Sha3Uncles: types.EmptyUncleHash,
	}

	// calculate gas limit based on parent header
	gasLimit, err := i.blockchain.CalculateGasLimit(header.Number)
	if err != nil {
		return nil, nil, err
	}

	header.GasLimit = gasLimit
	header.BaseFee = i.blockchain.CalculateBaseFee(parent)

	signer, validators, hooks, err := getModulesFromForkManager(i.forkManager, header.Number)
	if err != nil {
		i.logger.Error("cannot get modules from fork manager for", "block number", header.Number, "err", err)

		return nil, nil, err
	}

	if err := hooks.ModifyHeader(header, signer.Address()); err != nil {
		return nil, nil, err
	}

	// Set the header timestamp
	potentialTimestamp := i.calcHeaderTimestamp(parent.Timestamp, time.Now().UTC())
	header.Timestamp = uint64(potentialTimestamp.Unix())

	parentCommittedSeals, err := i.extractParentCommittedSeals(parent)
	if err != nil {
		return nil, nil, err
	}

	signer.InitIBFTExtra(header, validators, parentCommittedSeals)

	execStart := time.Now().UTC()

	tranGasLimit := new(uint64)
	*tranGasLimit = header.GasLimit

	// dst is the block-wide merge sink every STM batch's validated writes land in, in final
	// order; buildTransactions merges into it batch by batch, and its wrapping Transition is
	// what gets Commit()'d below - the same "one shared radix, many Transitions" pattern
	// TxDependancyExecutor.Execute already uses on the verification side.
	var dst *state.TxnVerifier

	blockMutex := &sync.RWMutex{}
	blockRadix := iradix.New().Txn()

	transition, err := i.executor.BeginTxnWithCustomTxn(
		parent.StateRoot, header, signer.Address(), tranGasLimit, func(s state.Snapshot) state.ITransitionTxn {
			dst = state.NewTxnVerifier(s, blockMutex, blockRadix)

			return dst
		})
	if err != nil {
		return nil, nil, err
	}

	// Get the block transactions
	writeCtx, cancelFn := context.WithTimeout(ctx, i.blockTime)
	defer cancelFn()

	txs, receipts, blockGasUsed, err := i.buildTransactions(
		writeCtx, gasLimit, header, signer.Address(), dst, tranGasLimit)
	if err != nil {
		return nil, nil, err
	}

	// provide dummy block instance to the PreCommitState
	// (for the IBFT consensus, it is correct to have just a header, as only it is used)
	if err := i.PreCommitState(&types.Block{Header: header}, transition); err != nil {
		return nil, nil, err
	}

	transition.AddPendingBalances()

	// transition.Write is never called directly on this block-wide Transition (each batch
	// executes through its own per-incarnation Transitions instead), so its own totalGas is
	// never incremented - it must be set explicitly from buildTransactions' running total.
	transition.SetTotalGas(blockGasUsed)

	_, root, err := transition.Commit()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to commit the state changes: %w", err)
	}

	metrics.MeasureSince([]string{consensusMetrics, "span", "execution"}, execStart)

	// Recorded retrospectively; error paths above abort before this point.
	_, execSpan := observability.Tracer().Start(ctx, "execution", trace.WithTimestamp(execStart))
	execSpan.End()

	header.StateRoot = root
	header.GasUsed = transition.TotalGas()

	// build the block
	block := consensus.BuildBlock(consensus.BuildBlockParams{
		Header:   header,
		Txns:     txs,
		Receipts: receipts,
	})

	// write the seal of the block after all the fields are completed
	header, err = signer.WriteProposerSeal(header)
	if err != nil {
		return nil, nil, err
	}

	block.Header = header

	// compute the hash, this is only a provisional hash since the final one
	// is sealed after all the committed seals
	block.Header.ComputeHash()

	i.logger.Info("build block", "number", header.Number, "txs", len(txs))

	return block, receipts, nil
}

// calcHeaderTimestamp calculates the new block timestamp, based
// on the block time and parent timestamp
func (i *backendIBFT) calcHeaderTimestamp(parentUnix uint64, currentTime time.Time) time.Time {
	var (
		parentTimestamp    = time.Unix(int64(parentUnix), 0)
		potentialTimestamp = parentTimestamp.Add(i.blockTime)
	)

	if potentialTimestamp.Before(currentTime) {
		potentialTimestamp = currentTime
	}

	return potentialTimestamp
}

// minSTMBatch and maxSTMBatch clamp the candidate-batch size pulled per STM round: too small
// starves the scheduler of independent work once a few txs conflict, too large deepens the
// blast radius of a cascading abort and delays the "is the block already full" check.
const (
	minSTMBatch = 32
	maxSTMBatch = 256
)

// buildTransactions drives the block-STM engine over the txpool, batch by batch, until the
// block is full, the txpool is exhausted, or writeCtx's deadline (bounded by i.blockTime)
// passes - then, like the sequential builder it replaces, waits out whatever remains of that
// deadline before returning. Every batch's validated writes are merged into dst (in final
// block order) as they land, and every batch's read/write sets feed blockstm.DepsBuilder,
// whose resulting dependency DAG is packed into header.ExtraData unconditionally: STM inherently
// produces these sets as part of its own conflict detection, so exporting them for verifiers is
// effectively free and keeps the proposer/verifier paths symmetric regardless of which
// verification-side forks happen to be active.
func (i *backendIBFT) buildTransactions(
	writeCtx context.Context,
	gasLimit uint64,
	header *types.Header,
	coinbase types.Address,
	dst *state.TxnVerifier,
	tranGasLimit *uint64,
) ([]*types.Transaction, []*types.Receipt, uint64, error) {
	hooks := i.forkManager.GetHooks(header.Number)
	if !hooks.ShouldWriteTransactions(header.Number) {
		return nil, nil, 0, nil
	}

	i.txpool.Prepare()
	i.buildBlockTxsRlpSize = 0

	var (
		depsBuilder      = blockstm.NewDepsBuilder()
		depsBuilderOK    = true
		nextDAGIndex     = 0
		batchSize        = min(max(4*i.stmEngine.Workers(), minSTMBatch), maxSTMBatch)
		dropped, demoted int
		executed         = make([]*types.Transaction, 0, batchSize)
		allReceipts      = make([]*types.Receipt, 0, batchSize)
		blockGasUsed     uint64
	)

batchLoop:
	for {
		select {
		case <-writeCtx.Done():
			break batchLoop
		default:
		}

		batch := i.pullCandidateBatch(gasLimit, batchSize)
		if len(batch) == 0 {
			break batchLoop
		}

		outcome, err := i.stmEngine.RunBatch(
			writeCtx, i.executor, header, coinbase, dst, *tranGasLimit, batch,
		)
		if err != nil {
			return nil, nil, 0, err
		}

		for _, tx := range outcome.Pop {
			i.txpool.Pop(tx)
			i.buildBlockTxsRlpSize += tx.Size()
		}

		for _, tx := range outcome.Drop {
			i.txpool.Drop(tx)

			dropped++
		}

		for _, tx := range outcome.Demote {
			i.txpool.Demote(tx)

			demoted++
		}

		if depsBuilderOK {
			for _, rws := range outcome.ReadWriteSets {
				if err := depsBuilder.AddTransaction(nextDAGIndex, rws.ReadList, rws.WriteList); err != nil {
					i.logger.Error("Failed to build tx dependency metadata, dropping DAG hint",
						"tx", nextDAGIndex, "err", err)

					depsBuilderOK = false

					break
				}

				nextDAGIndex++
			}
		}

		// outcome.Receipts' CumulativeGasUsed is batch-local (finalize starts every batch's walk
		// at zero); receipts must report gas used across the whole block, so re-base each one by
		// however much every earlier batch already used before appending.
		for _, r := range outcome.Receipts {
			r.CumulativeGasUsed += blockGasUsed
		}

		executed = append(executed, outcome.Included...)
		allReceipts = append(allReceipts, outcome.Receipts...)
		blockGasUsed += outcome.GasUsed
		*tranGasLimit -= outcome.GasUsed

		if len(outcome.Included) < len(batch) {
			// finalize hit the gas-limit cutoff partway through this batch: the block is full
			break batchLoop
		}
	}

	i.logger.Info(
		"executed txs",
		"successful", len(executed),
		"dropped", dropped,
		"demoted", demoted,
		"remaining", i.txpool.Length(),
	)

	if depsBuilderOK {
		txDependency := depsBuilder.GetDeps()
		// deps is nil when DepsBuilder errored, and non-nil empty when no transactions were added.
		if txDependency == nil {
			i.logger.Error("Failed to build tx dependency DAG, skipping metadata", "number", header.Number)
		}

		header.ExtraData = sgn.PackTxDependencyIntoExtra(header.ExtraData, txDependency)
	}

	// wait for the timer to expire, matching the sequential builder's fixed wall-clock policy
	<-writeCtx.Done()

	return executed, allReceipts, blockGasUsed, nil
}

// pullCandidateBatch pulls up to batchSize candidates from the txpool via repeated Peek()
// calls. Peek() only ever resurfaces a second tx from the same account after Pop() is called on
// the first, so a batch built this way naturally contains at most one candidate per account -
// bookkeeping (Pop/Drop/Demote) is deferred until the batch's STM results are known, so nothing
// here mutates the pool beyond the same size/gas pre-filter writeTransaction used to apply
// inline.
//
// The RLP-size budget has to be tracked as the batch is assembled, not just against
// i.buildBlockTxsRlpSize: that counter only advances once a batch's results come back, so
// checking every candidate against the same starting value would let a single batch admit an
// unbounded number of txs past types.MaxTxsRlpSize. Candidates that later drop out (failed or
// past the gas cutoff) leave the projection conservative, never over-permissive.
func (i *backendIBFT) pullCandidateBatch(gasLimit uint64, batchSize int) []*types.Transaction {
	batch := make([]*types.Transaction, 0, batchSize)
	projectedRlpSize := i.buildBlockTxsRlpSize

	for len(batch) < batchSize {
		tx := i.txpool.Peek()
		if tx == nil {
			break
		}

		txSize := tx.Size()

		if tx.Gas > gasLimit || txSize+projectedRlpSize > types.MaxTxsRlpSize {
			i.txpool.Drop(tx)

			continue
		}

		projectedRlpSize += txSize

		batch = append(batch, tx)
	}

	return batch
}

// extractCommittedSeals extracts CommittedSeals from header
func (i *backendIBFT) extractCommittedSeals(
	header *types.Header,
) (sgn.Seals, error) {
	signer, err := i.forkManager.GetSigner(header.Number)
	if err != nil {
		return nil, err
	}

	extra, err := signer.GetIBFTExtra(header)
	if err != nil {
		return nil, err
	}

	return extra.CommittedSeals, nil
}

// extractParentCommittedSeals extracts ParentCommittedSeals from header
func (i *backendIBFT) extractParentCommittedSeals(
	header *types.Header,
) (sgn.Seals, error) {
	if header.Number == 0 {
		return nil, nil
	}

	return i.extractCommittedSeals(header)
}
