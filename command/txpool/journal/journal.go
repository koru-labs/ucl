package journal

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/command"
	ibft "github.com/0xPolygon/polygon-edge/consensus/ibft"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/txpool"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/spf13/cobra"
)

const (
	dataDirFlag = "data-dir"
	hashFlag    = "hash"
	chainIDFlag = "chain-id"
	removedFlag = "removed"
)

type journalParams struct {
	dataDir string
	chainID uint64
}

func GetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "journal",
		Short: "Inspect and edit the offline local txpool journal (stop the validator first).",
	}

	cmd.PersistentFlags().String(dataDirFlag, "", "validator data directory (e.g. ./test-chain-1)")
	cmd.PersistentFlags().Uint64(chainIDFlag, command.DefaultChainID, "chain id used to recover the sender")
	_ = cmd.MarkPersistentFlagRequired(dataDirFlag)

	cmd.AddCommand(listCommand(), removeCommand(), restoreCommand(), rejectedCommand())

	return cmd
}

func listCommand() *cobra.Command {
	var showRemoved bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List transactions in the local journal (or the removed quarantine file)",
		Run: func(cmd *cobra.Command, _ []string) {
			run(cmd, func(p journalParams) (command.CommandResult, error) {
				path := txpool.JournalPath(p.dataDir)
				title := "TXPOOL JOURNAL"

				if showRemoved {
					path = txpool.RemovedJournalPath(p.dataDir)
					title = "TXPOOL REMOVED JOURNAL"
				}

				txs, err := txpool.ReadJournalFile(path)
				if err != nil {
					return nil, err
				}

				return &journalListResult{
					Title: title,
					Path:  path,
					Txs:   decorateTxs(txs, p.chainID, nil),
				}, nil
			})
		},
	}

	cmd.Flags().BoolVar(&showRemoved, removedFlag, false, "list the quarantine file instead of the live journal")

	return cmd
}

func removeCommand() *cobra.Command {
	return mutateCommand(
		"remove",
		"Move a journaled transaction into the quarantine file",
		"removed",
		"transaction hash to remove (repeatable)",
		txpool.RemovedJournalPath,
		txpool.RemoveFromJournal,
	)
}

func restoreCommand() *cobra.Command {
	return mutateCommand(
		"restore",
		"Move a quarantined transaction back into the live journal",
		"restored",
		"transaction hash to restore (repeatable)",
		txpool.JournalPath,
		txpool.RestoreToJournal,
	)
}

func mutateCommand(
	use, short, action, hashUsage string,
	resultPath func(string) string,
	mutate func(string, string, []types.Hash) ([]*types.Transaction, error),
) *cobra.Command {
	var hashes []string

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Run: func(cmd *cobra.Command, _ []string) {
			run(cmd, func(p journalParams) (command.CommandResult, error) {
				parsed, err := parseHashes(hashes)
				if err != nil {
					return nil, err
				}

				moved, err := mutate(
					txpool.JournalPath(p.dataDir),
					txpool.RemovedJournalPath(p.dataDir),
					parsed,
				)
				if err != nil {
					return nil, err
				}

				return &journalMutateResult{
					Action: action,
					Path:   resultPath(p.dataDir),
					Txs:    decorateTxs(moved, p.chainID, nil),
				}, nil
			})
		},
	}

	cmd.Flags().StringSliceVar(&hashes, hashFlag, nil, hashUsage)
	_ = cmd.MarkFlagRequired(hashFlag)

	return cmd
}

func rejectedCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rejected",
		Short: "List last rejected IBFT proposals and their transactions",
		Run: func(cmd *cobra.Command, _ []string) {
			run(cmd, func(p journalParams) (command.CommandResult, error) {
				recs, err := ibft.LoadRejectedBlocks(ibft.RejectedBlocksPath(p.dataDir))
				if err != nil {
					return nil, err
				}

				journalTxs, err := txpool.ReadJournalFile(txpool.JournalPath(p.dataDir))
				if err != nil {
					return nil, err
				}

				inJournal := make(map[types.Hash]struct{}, len(journalTxs))
				for _, tx := range journalTxs {
					inJournal[txHash(tx)] = struct{}{}
				}

				out := make([]rejectedBlockView, 0, len(recs))
				for i := len(recs) - 1; i >= 0; i-- {
					rec := recs[i]

					block := rec.Block
					if block == nil {
						continue
					}

					if block.Header.Hash == types.ZeroHash {
						block.Header.ComputeHash()
					}

					txs := decorateTxs(block.Transactions, p.chainID, inJournal)
					applyOutcomes(txs, rec.Outcomes)

					out = append(out, rejectedBlockView{
						Number:            block.Number(),
						Hash:              block.Hash().String(),
						Reason:            rec.Reason,
						Timestamp:         rec.Timestamp,
						ProposedStateRoot: block.Header.StateRoot.String(),
						LocalStateRoot:    rec.LocalStateRoot.String(),
						Txs:               txs,
					})
				}

				return &rejectedListResult{
					Path:   ibft.RejectedBlocksPath(p.dataDir),
					Blocks: out,
				}, nil
			})
		},
	}
}

func run(cmd *cobra.Command, fn func(journalParams) (command.CommandResult, error)) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	dataDir, err := cmd.Flags().GetString(dataDirFlag)
	if err != nil {
		outputter.SetError(err)

		return
	}

	chainID, err := cmd.Flags().GetUint64(chainIDFlag)
	if err != nil {
		outputter.SetError(err)

		return
	}

	res, err := fn(journalParams{dataDir: dataDir, chainID: chainID})
	if err != nil {
		outputter.SetError(err)

		return
	}

	outputter.SetCommandResult(res)
}

func parseHashes(raw []string) ([]types.Hash, error) {
	out := make([]types.Hash, 0, len(raw))

	for _, s := range raw {
		if s == "" {
			continue
		}

		out = append(out, types.StringToHash(s))
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no transaction hashes given")
	}

	return out, nil
}

func txHash(tx *types.Transaction) types.Hash {
	if tx.Hash == types.ZeroHash {
		tx.ComputeHash(0)
	}

	return tx.Hash
}

func decorateTxs(txs []*types.Transaction, chainID uint64, inJournal map[types.Hash]struct{}) []txView {
	forks := chain.AllForksEnabled.At(0)
	signer := crypto.NewSigner(forks, chainID)
	out := make([]txView, 0, len(txs))

	for _, tx := range txs {
		h := txHash(tx)
		from := tx.From

		if from == types.ZeroAddress {
			if recovered, err := signer.Sender(tx); err == nil {
				from = recovered
			}
		}

		to := "contract-create"
		if tx.To != nil {
			to = tx.To.String()
		}

		value := "0"
		if tx.Value != nil {
			value = tx.Value.String()
		}

		gasPrice := "0"
		if tx.GasPrice != nil {
			gasPrice = tx.GasPrice.String()
		}

		view := txView{
			Hash:     h.String(),
			From:     from.String(),
			To:       to,
			Nonce:    tx.Nonce,
			Value:    value,
			Gas:      tx.Gas,
			GasPrice: gasPrice,
			Type:     tx.Type.String(),
			InputLen: len(tx.Input),
		}

		if inJournal != nil {
			_, ok := inJournal[h]
			view.InJournal = &ok
		}

		out = append(out, view)
	}

	return out
}

func applyOutcomes(txs []txView, outcomes []state.TxExecOutcome) {
	byHash := make(map[types.Hash]state.TxExecOutcome, len(outcomes))
	for _, o := range outcomes {
		byHash[o.Hash] = o
	}

	for i := range txs {
		o, ok := byHash[types.StringToHash(txs[i].Hash)]
		if !ok {
			continue
		}

		status := "failed"
		if o.Status == types.ReceiptSuccess {
			status = "success"
		}

		txs[i].LocalStatus = status
		txs[i].LocalGasUsed = o.GasUsed
		txs[i].ReturnHash = o.ReturnHash.String()
		txs[i].StateDeltaHash = o.DeltaHash.String()
	}
}
