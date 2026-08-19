package ibft

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/0xPolygon/go-ibft/messages"
	"github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	"github.com/0xPolygon/polygon-edge/observability"
	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
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
	extraDataOriginal := newBlock.Header.ExtraData
	extraDataBackup := make([]byte, len(extraDataOriginal))
	copy(extraDataBackup, extraDataOriginal)

	signer, validators, hooks, err := getModulesFromForkManager(i.forkManager, newBlock.Number())
	if err != nil {
		i.logger.Error("cannot get modules from fork manager for", "block number", newBlock.Number(), "err", err)

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
		committedSealsStr := make([]string, len(committedSealsMap))
		for i, seal := range committedSeals {
			committedSealsStr[i] = fmt.Sprintf("{signer=%v signature=%v}",
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
func (i *backendIBFT) buildBlock(
	ctx context.Context,
	parent *types.Header) (
	*types.Block,
	[]*types.Receipt,
	error) {
	ctx, buildSpan := observability.Tracer().Start(ctx, "build")
	defer buildSpan.End()

	defer metrics.MeasureSince([]string{consensusMetrics, "span", "build"}, time.Now())

	header := &types.Header{
		ParentHash: parent.Hash,
		Number:     parent.Number + 1,
		Miner:      types.ZeroAddress.Bytes(),
		Nonce:      types.Nonce{},
		MixHash:    signer.IstanbulDigest,
		// this is required because blockchain needs difficulty to organize blocks and forks
		Difficulty: parent.Number + 1,
		StateRoot:  types.EmptyRootHash, // this avoids needing state for now
		Sha3Uncles: types.EmptyUncleHash,
		GasLimit:   parent.GasLimit, // Inherit from parent for now, will need to adjust dynamically later.
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

	transition, err := i.executor.BeginTxn(parent.StateRoot, header, signer.Address())
	if err != nil {
		return nil, nil, err
	}

	// Get the block transactions
	writeCtx, cancelFn := context.WithTimeout(context.Background(), i.blockTime)
	defer cancelFn()

	txs := i.writeTransactions(
		writeCtx,
		gasLimit,
		header.Number,
		transition,
	)

	// provide dummy block instance to the PreCommitState
	// (for the IBFT consensus, it is correct to have just a header, as only it is used)
	if err := i.PreCommitState(&types.Block{Header: header}, transition); err != nil {
		return nil, nil, err
	}

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

	buildBlockParams := consensus.BuildBlockParams{
		Header:   header,
		Txns:     txs,
		Receipts: transition.Receipts(),
	}

	if i.config.Params.Forks.At(header.Number).EIP7928 {
		bar := transition.GetBlockAccessRecord()
		header.BlockAccessRecordHash = bar.Hash()
		buildBlockParams.BlockAccessRecord = bar
	}

	// build the block
	block := consensus.BuildBlock(buildBlockParams)

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

	return block, transition.Receipts(), nil
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

type status uint8

const (
	success status = iota
	fail
	skip
)

type txExeResult struct {
	tx     *types.Transaction
	status status
}

func (i *backendIBFT) writeTransactions(
	writeCtx context.Context,
	gasLimit,
	blockNumber uint64,
	transition *state.Transition,
) (executed []*types.Transaction) {
	executed = make([]*types.Transaction, 0)

	hooks := i.forkManager.GetHooks(blockNumber)
	if !hooks.ShouldWriteTransactions(blockNumber) {
		return
	}

	var (
		successful = 0
		failed     = 0
		skipped    = 0
	)

	defer func() {
		i.logger.Info(
			"executed txs",
			"successful", successful,
			"failed", failed,
			"skipped", skipped,
			"remaining", i.txpool.Length(),
		)
	}()

	i.txpool.Prepare()
	i.buildBlockTxsRlpSize = 0

	var txRecorder *state.TxAccessRecorder

	var bar state.BlockAccessRecord

	if i.config.Params.Forks.At(blockNumber).EIP7928 {
		bar = state.NewBlockAccessRecord()

		defer func() {
			transition.SetBlockAccessRecord(bar.Pack())
		}()
	}

write:
	for {
		select {
		case <-writeCtx.Done():
			return
		default:
			if i.config.Params.Forks.At(blockNumber).EIP7928 {
				txRecorder = state.NewTxAccessRecorder(uint64(len(executed)))
				transition.SetTxAccessRecorder(txRecorder)
			}

			// execute transactions one by one
			result, ok := i.writeTransaction(
				i.txpool.Peek(),
				transition,
				gasLimit,
			)

			if !ok {
				break write
			}

			tx := result.tx

			switch result.status {
			case success:
				executed = append(executed, tx)
			case fail:
				failed++
			case skip:
				skipped++
			}

			bar.Insert(txRecorder)
		}
	}

	//	wait for the timer to expire
	<-writeCtx.Done()

	return
}

func (i *backendIBFT) writeTransaction(
	tx *types.Transaction,
	transition *state.Transition,
	gasLimit uint64,
) (*txExeResult, bool) {
	if tx == nil {
		return nil, false
	}

	if tx.Gas > gasLimit || tx.Size()+i.buildBlockTxsRlpSize > types.MaxTxsRlpSize {
		i.txpool.Drop(tx)

		// continue processing
		return &txExeResult{tx, fail}, true
	}

	if err := transition.Write(tx); err != nil {
		if _, ok := err.(*state.GasLimitReachedTransitionApplicationError); ok { //nolint:errorlint
			// stop processing
			return nil, false
		} else if appErr, ok := err.(*state.TransitionApplicationError); ok && appErr.IsRecoverable { //nolint:errorlint
			i.txpool.Demote(tx)

			return &txExeResult{tx, skip}, true
		} else {
			i.txpool.Drop(tx)

			return &txExeResult{tx, fail}, true
		}
	}

	i.txpool.Pop(tx)
	i.buildBlockTxsRlpSize += tx.Size()

	return &txExeResult{tx, success}, true
}

// extractCommittedSeals extracts CommittedSeals from header
func (i *backendIBFT) extractCommittedSeals(
	header *types.Header,
) (signer.Seals, error) {
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
) (signer.Seals, error) {
	if header.Number == 0 {
		return nil, nil
	}

	return i.extractCommittedSeals(header)
}
