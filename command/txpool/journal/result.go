package journal

import (
	"bytes"
	"fmt"
	"time"

	"github.com/0xPolygon/polygon-edge/command/helper"
)

type txView struct {
	Hash           string `json:"hash"`
	From           string `json:"from"`
	To             string `json:"to"`
	Nonce          uint64 `json:"nonce"`
	Value          string `json:"value"`
	Gas            uint64 `json:"gas"`
	GasPrice       string `json:"gasPrice"`
	Type           string `json:"type"`
	InputLen       int    `json:"inputLen"`
	InJournal      *bool  `json:"inJournal,omitempty"`
	LocalStatus    string `json:"localStatus,omitempty"`
	LocalGasUsed   uint64 `json:"localGasUsed,omitempty"`
	ReturnHash     string `json:"returnHash,omitempty"`
	StateDeltaHash string `json:"stateDeltaHash,omitempty"`
}

type journalListResult struct {
	Title string   `json:"title"`
	Path  string   `json:"path"`
	Txs   []txView `json:"transactions"`
}

func (r *journalListResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString(fmt.Sprintf("\n[%s]\n", r.Title))
	buffer.WriteString(helper.FormatKV([]string{
		fmt.Sprintf("Path|%s", r.Path),
		fmt.Sprintf("Count|%d", len(r.Txs)),
	}))
	buffer.WriteString("\n")

	for i, tx := range r.Txs {
		buffer.WriteString(fmt.Sprintf("\n[TX %d]\n", i))
		buffer.WriteString(formatTx(tx))
		buffer.WriteString("\n")
	}

	return buffer.String()
}

type journalMutateResult struct {
	Action string   `json:"action"`
	Path   string   `json:"path"`
	Txs    []txView `json:"transactions"`
}

func (r *journalMutateResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString(fmt.Sprintf("\n[TXPOOL JOURNAL %s]\n", r.Action))
	buffer.WriteString(helper.FormatKV([]string{
		fmt.Sprintf("Quarantine / journal path|%s", r.Path),
		fmt.Sprintf("Count|%d", len(r.Txs)),
	}))
	buffer.WriteString("\n")

	for i, tx := range r.Txs {
		buffer.WriteString(fmt.Sprintf("\n[TX %d]\n", i))
		buffer.WriteString(formatTx(tx))
		buffer.WriteString("\n")
	}

	return buffer.String()
}

type rejectedBlockView struct {
	Number            uint64   `json:"number"`
	Hash              string   `json:"hash"`
	Reason            string   `json:"reason"`
	Timestamp         int64    `json:"timestamp"`
	ProposedStateRoot string   `json:"proposedStateRoot"`
	LocalStateRoot    string   `json:"localStateRoot"`
	Txs               []txView `json:"transactions"`
}

type rejectedListResult struct {
	Path   string              `json:"path"`
	Blocks []rejectedBlockView `json:"blocks"`
}

func (r *rejectedListResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString("\n[IBFT REJECTED BLOCKS]\n")
	buffer.WriteString(helper.FormatKV([]string{
		fmt.Sprintf("Path|%s", r.Path),
		fmt.Sprintf("Count|%d", len(r.Blocks)),
	}))
	buffer.WriteString("\n")

	for i, b := range r.Blocks {
		buffer.WriteString(fmt.Sprintf("\n[BLOCK %d]\n", i))
		buffer.WriteString(helper.FormatKV([]string{
			fmt.Sprintf("Number|%d", b.Number),
			fmt.Sprintf("Hash|%s", b.Hash),
			fmt.Sprintf("Reason|%s", b.Reason),
			fmt.Sprintf("Time|%s", time.Unix(b.Timestamp, 0).UTC().Format(time.RFC3339)),
			fmt.Sprintf("Proposed state root|%s", b.ProposedStateRoot),
			fmt.Sprintf("Local state root|%s", b.LocalStateRoot),
			fmt.Sprintf("Transactions|%d", len(b.Txs)),
		}))
		buffer.WriteString("\n")

		for j, tx := range b.Txs {
			buffer.WriteString(fmt.Sprintf("\n  [TX %d]\n", j))
			buffer.WriteString(formatTx(tx))
			buffer.WriteString("\n")
		}
	}

	return buffer.String()
}

func formatTx(tx txView) string {
	rows := []string{
		fmt.Sprintf("Hash|%s", tx.Hash),
		fmt.Sprintf("From|%s", tx.From),
		fmt.Sprintf("To|%s", tx.To),
		fmt.Sprintf("Nonce|%d", tx.Nonce),
		fmt.Sprintf("Value|%s", tx.Value),
		fmt.Sprintf("Gas|%d", tx.Gas),
		fmt.Sprintf("GasPrice|%s", tx.GasPrice),
		fmt.Sprintf("Type|%s", tx.Type),
		fmt.Sprintf("Input bytes|%d", tx.InputLen),
	}

	if tx.InJournal != nil {
		rows = append(rows, fmt.Sprintf("In local journal|%t", *tx.InJournal))
	}

	if tx.LocalStatus != "" {
		rows = append(rows,
			fmt.Sprintf("Local status|%s", tx.LocalStatus),
			fmt.Sprintf("Local gas used|%d", tx.LocalGasUsed),
			fmt.Sprintf("Return hash|%s", tx.ReturnHash),
			fmt.Sprintf("State delta hash|%s", tx.StateDeltaHash),
		)
	}

	return helper.FormatKV(rows)
}
