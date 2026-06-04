package ibft

import (
	"context"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"time"

	"github.com/0xPolygon/go-ibft/messages"
	"github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/blockstm"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
)

func (i *backendIBFT) BuildProposal(view *proto.View) []byte {
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

		return nil
	}

	block, receipts, err := i.buildBlock(latestHeader)
	if err != nil {
		i.logger.Error("cannot build block", "num", view.Height, "err", err)

		return nil
	}

	i.blockchain.AddReceiptsToCache(block.Hash(), receipts)

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
	if err := i.blockchain.WriteBlock(newBlock, "consensus"); err != nil {
		i.logger.Error("cannot write block", "err", err)

		return
	}

	i.updateMetrics(newBlock)

	i.logger.Info(
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
func (i *backendIBFT) buildBlock(parent *types.Header) (*types.Block, []*types.Receipt, error) {
	var (
		m1, m2 runtime.MemStats
		start  = time.Now()
	)

	runtime.GC() // optional, reduce noise
	runtime.ReadMemStats(&m1)

	defer func() {
		runtime.ReadMemStats(&m2)

		i.logger.Debug("Build block allocated bytes CREW", "value", m2.TotalAlloc-m1.TotalAlloc)
		i.logger.Debug("Build block mallocs CREW", "value", m2.Mallocs-m1.Mallocs)
		i.logger.Debug("Build block elapsed time CREW", "value", time.Since(start))
	}()

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

	transition, err := i.executor.BeginTxn(parent.StateRoot, header, signer.Address())
	if err != nil {
		return nil, nil, err
	}

	// Get the block transactions
	writeCtx, cancelFn := context.WithTimeout(context.Background(), i.blockTime)
	defer cancelFn()

	var (
		depsBuilder *blockstm.DepsBuilder        = blockstm.NewDepsBuilder()
		chDeps      chan blockstm.TxReadWriteSet = make(chan blockstm.TxReadWriteSet)
		depsWg      sync.WaitGroup
		once        sync.Once
	)

	// Make sure we safely close the channel in case of interrupt
	defer once.Do(func() {
		close(chDeps)
	})

	depsWg.Add(1)

	go func(chDeps chan blockstm.TxReadWriteSet) {
		for t := range chDeps {
			if err := depsBuilder.AddTransaction(t.Index, t.ReadList, t.WriteList); err != nil {
				// Non-sequential index indicates a systematic bug, not a transient error.
				// Drain the channel so the sender never blocks, then stop processing.
				i.logger.Error("Failed to build tx dependency metadata, dropping DAG hint", "tx", t.Index, "err", err)

				for range chDeps {
				}

				break
			}
		}

		depsWg.Done()
	}(chDeps)

	txs, hasBalanceReads := i.writeTransactions(
		writeCtx,
		gasLimit,
		header.Number,
		transition,
		chDeps,
		&once,
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

	once.Do(func() {
		close(chDeps)
	})
	depsWg.Wait()

	deps := depsBuilder.GetDeps()
	if deps == nil {
		i.logger.Error("Failed to build tx dependency DAG, skipping metadata", "number", header.Number)
	}

	var dependencies [][]uint64

	// deps is nil when DepsBuilder errored, and non-nil empty when no transactions were added.
	if deps != nil && !hasBalanceReads {
		tempDeps := make([][]uint64, len(txs))

		for j := range deps[0] {
			tempDeps[0] = append(tempDeps[0], uint64(j))
		}

		delayFlag := true

		for i := 1; i < len(txs); i++ {
			for j := range deps[i] {
				tempDeps[i] = append(tempDeps[i], uint64(j))
			}
		}

		if delayFlag {
			dependencies = tempDeps
		}
	}

	for idx, dep := range dependencies {
		i.logger.Debug("tx dependecies CREW", "txIndx", idx, "dependency", dep)
	}

	header.StateRoot = root
	header.GasUsed = transition.TotalGas()

	// build the block
	block := consensus.BuildBlock(consensus.BuildBlockParams{
		Header:   header,
		Txns:     txs,
		Receipts: transition.Receipts(),
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

type transitionInterface interface {
	Write(txn *types.Transaction) error
	GetTxReadWriteSet() blockstm.TxReadWriteSet
}

func (i *backendIBFT) writeTransactions(
	writeCtx context.Context,
	gasLimit,
	blockNumber uint64,
	transition transitionInterface,
	chDeps chan blockstm.TxReadWriteSet,
	once *sync.Once,
) (executed []*types.Transaction, hasBalanceReads bool) {
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

write:
	for {
		select {
		case <-writeCtx.Done():
			return
		default:
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
				// Send with timeout to prevent deadlock
				select {
				case chDeps <- transition.GetTxReadWriteSet():
					// Successfully sent
				case <-time.After(1 * time.Second):
					// Timeout after 1 second - channel is blocked
					once.Do(func() {
						close(chDeps)
					})

					return
				}

				executed = append(executed, tx)
				successful++
			case fail:
				failed++
			case skip:
				skipped++
			}
		}
	}

	//	wait for the timer to expire
	<-writeCtx.Done()

	return
}

func (i *backendIBFT) writeTransaction(
	tx *types.Transaction,
	transition transitionInterface,
	gasLimit uint64,
) (*txExeResult, bool) {
	if tx == nil {
		return nil, false
	}

	if tx.Gas > gasLimit {
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
