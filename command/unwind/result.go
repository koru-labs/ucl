package unwind

import (
	"bytes"
	"fmt"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/command/helper"
	ibftFork "github.com/0xPolygon/polygon-edge/consensus/ibft/fork"
)

type unwoundBlockView struct {
	Number   uint64   `json:"number"`
	Hash     string   `json:"hash"`
	TxHashes []string `json:"txHashes"`
}

type snapshotTrimView struct {
	Dir          string `json:"dir"`
	OldLastBlock uint64 `json:"oldLastBlock"`
	NewLastBlock uint64 `json:"newLastBlock"`
	Dropped      int    `json:"dropped"`
	Kept         int    `json:"kept"`
	Skipped      string `json:"skipped,omitempty"`
}

type unwindResult struct {
	DataDir    string             `json:"dataDir"`
	DryRun     bool               `json:"dryRun"`
	FromNumber uint64             `json:"fromNumber"`
	FromHash   string             `json:"fromHash"`
	ToNumber   uint64             `json:"toNumber"`
	ToHash     string             `json:"toHash"`
	Removed    []unwoundBlockView `json:"removed"`
	Snapshots  snapshotTrimView   `json:"snapshots"`
}

func newResult(
	dataDir string,
	dryRun bool,
	chain *blockchain.UnwindResult,
	snaps *ibftFork.SnapshotTrimResult,
) *unwindResult {
	removed := make([]unwoundBlockView, 0, len(chain.Removed))
	for _, block := range chain.Removed {
		txs := make([]string, 0, len(block.TxHashes))
		for _, h := range block.TxHashes {
			txs = append(txs, h.String())
		}

		removed = append(removed, unwoundBlockView{
			Number:   block.Number,
			Hash:     block.Hash.String(),
			TxHashes: txs,
		})
	}

	snapView := snapshotTrimView{}
	if snaps != nil {
		snapView = snapshotTrimView{
			Dir:          snaps.Dir,
			OldLastBlock: snaps.OldLastBlock,
			NewLastBlock: snaps.NewLastBlock,
			Dropped:      snaps.Dropped,
			Kept:         snaps.Kept,
			Skipped:      snaps.Skipped,
		}
	}

	return &unwindResult{
		DataDir:    dataDir,
		DryRun:     dryRun,
		FromNumber: chain.FromNumber,
		FromHash:   chain.FromHash.String(),
		ToNumber:   chain.ToNumber,
		ToHash:     chain.ToHash.String(),
		Removed:    removed,
		Snapshots:  snapView,
	}
}

func (r *unwindResult) GetOutput() string {
	var buffer bytes.Buffer

	title := "BLOCKCHAIN UNWIND"
	if r.DryRun {
		title = "BLOCKCHAIN UNWIND (dry-run)"
	}

	buffer.WriteString(fmt.Sprintf("\n[%s]\n", title))
	buffer.WriteString(helper.FormatKV([]string{
		fmt.Sprintf("Data dir|%s", r.DataDir),
		fmt.Sprintf("From|%d %s", r.FromNumber, r.FromHash),
		fmt.Sprintf("To|%d %s", r.ToNumber, r.ToHash),
		fmt.Sprintf("Removed blocks|%d", len(r.Removed)),
	}))
	buffer.WriteString("\n")

	for i, block := range r.Removed {
		buffer.WriteString(fmt.Sprintf("\n[REMOVED %d]\n", i))
		buffer.WriteString(helper.FormatKV([]string{
			fmt.Sprintf("Number|%d", block.Number),
			fmt.Sprintf("Hash|%s", block.Hash),
			fmt.Sprintf("Transactions|%d", len(block.TxHashes)),
		}))
		buffer.WriteString("\n")

		for j, h := range block.TxHashes {
			buffer.WriteString(fmt.Sprintf("  tx %d = %s\n", j, h))
		}
	}

	buffer.WriteString("\n[IBFT SNAPSHOTS]\n")

	if r.Snapshots.Skipped != "" {
		buffer.WriteString(helper.FormatKV([]string{
			fmt.Sprintf("Dir|%s", r.Snapshots.Dir),
			fmt.Sprintf("Skipped|%s", r.Snapshots.Skipped),
		}))
	} else {
		buffer.WriteString(helper.FormatKV([]string{
			fmt.Sprintf("Dir|%s", r.Snapshots.Dir),
			fmt.Sprintf("LastBlock|%d -> %d", r.Snapshots.OldLastBlock, r.Snapshots.NewLastBlock),
			fmt.Sprintf("Dropped snapshots|%d", r.Snapshots.Dropped),
			fmt.Sprintf("Kept snapshots|%d", r.Snapshots.Kept),
		}))
	}

	buffer.WriteString("\n")

	return buffer.String()
}
