package unwind

import (
	"fmt"
	"path/filepath"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/blockchain/storage/pebble"
	"github.com/0xPolygon/polygon-edge/command"
	ibftFork "github.com/0xPolygon/polygon-edge/consensus/ibft/fork"
	"github.com/hashicorp/go-hclog"
	"github.com/spf13/cobra"
)

const (
	dataDirFlag = "data-dir"
	blocksFlag  = "blocks"
	toFlag      = "to"
	dryRunFlag  = "dry-run"
)

func GetCommand() *cobra.Command {
	var (
		dataDir string
		blocks  uint64
		to      uint64
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "unwind",
		Short: "Move a stopped validator's canonical HEAD backward by N blocks (or to a height)",
		Long: `Offline tool. Stop the validator first.

Moves HEAD back, deletes canonical and tx-lookup indexes for the dropped
heights, and clamps IBFT snapshot files. Header/body/receipt bytes and the
state trie are left on disk.

Every validator that should stay in the same network must unwind to the
same height before restart. This does not remove a journaled transaction;
use "txpool journal remove" if a bad local tx is still being proposed.`,
		Run: func(cmd *cobra.Command, _ []string) {
			outputter := command.InitializeOutputter(cmd)
			defer outputter.WriteOutput()

			useBlocks := cmd.Flags().Changed(blocksFlag)

			res, err := execute(dataDir, blocks, to, useBlocks, dryRun)
			if err != nil {
				outputter.SetError(err)

				return
			}

			outputter.SetCommandResult(res)
		},
	}

	cmd.Flags().StringVar(&dataDir, dataDirFlag, "", "validator data directory (e.g. ./test-chain-1)")
	cmd.Flags().Uint64Var(&blocks, blocksFlag, 0, "number of blocks to drop from HEAD")
	cmd.Flags().Uint64Var(&to, toFlag, 0, "canonical height to keep as the new HEAD")
	cmd.Flags().BoolVar(&dryRun, dryRunFlag, false, "print what would be removed without writing")
	_ = cmd.MarkFlagRequired(dataDirFlag)
	cmd.MarkFlagsOneRequired(blocksFlag, toFlag)
	cmd.MarkFlagsMutuallyExclusive(blocksFlag, toFlag)

	return cmd
}

func execute(dataDir string, blocks, to uint64, useBlocks, dryRun bool) (*unwindResult, error) {
	dbPath := filepath.Join(dataDir, "blockchain")

	db, err := pebble.NewPebbleDBStorage(dbPath, hclog.NewNullLogger())
	if err != nil {
		return nil, fmt.Errorf("open blockchain db at %s: %w (stop the validator first)", dbPath, err)
	}
	defer db.Close()

	headNum, ok := db.ReadHeadNumber()
	if !ok {
		return nil, fmt.Errorf("head number not found in %s", dbPath)
	}

	target, err := resolveTarget(headNum, blocks, to, useBlocks)
	if err != nil {
		return nil, err
	}

	var chain *blockchain.UnwindResult

	if dryRun {
		chain, err = blockchain.PlanUnwind(db, target)
	} else {
		chain, err = blockchain.UnwindTo(db, target)
	}

	if err != nil {
		return nil, err
	}

	snaps, err := ibftFork.TrimSnapshots(filepath.Join(dataDir, "consensus"), chain.ToNumber, dryRun)
	if err != nil {
		return nil, fmt.Errorf("trim IBFT snapshots: %w", err)
	}

	return newResult(dataDir, dryRun, chain, snaps), nil
}

func resolveTarget(head, blocks, to uint64, useBlocks bool) (uint64, error) {
	if useBlocks {
		if blocks == 0 {
			return 0, fmt.Errorf("blocks must be greater than 0")
		}

		if blocks > head {
			return 0, fmt.Errorf("cannot unwind %d blocks: head is %d (genesis is 0)", blocks, head)
		}

		return head - blocks, nil
	}

	if to > head {
		return 0, fmt.Errorf("target %d is above current head %d", to, head)
	}

	if to == head {
		return 0, fmt.Errorf("head is already %d", head)
	}

	return to, nil
}
