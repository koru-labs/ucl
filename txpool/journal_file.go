package txpool

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/0xPolygon/polygon-edge/types"
)

const (
	JournalDir             = "txpool"
	JournalFileName        = "transactions.rlp"
	RemovedJournalFileName = "removed.rlp"
)

// JournalPath is the live local-tx journal under a validator data dir.
func JournalPath(dataDir string) string {
	return filepath.Join(dataDir, JournalDir, JournalFileName)
}

// RemovedJournalPath is the quarantine file for txs removed from the journal.
func RemovedJournalPath(dataDir string) string {
	return filepath.Join(dataDir, JournalDir, RemovedJournalFileName)
}

// ReadJournalFile decodes a concatenated journal dump. Missing file is empty.
func ReadJournalFile(path string) ([]*types.Transaction, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	txs := make([]*types.Transaction, 0)

	for len(data) > 0 {
		tx := &types.Transaction{}
		if err := tx.UnmarshalJournal(data); err != nil {
			return nil, err
		}

		data = data[tx.JournalSize():]
		txs = append(txs, tx)
	}

	return txs, nil
}

// WriteJournalFile atomically replaces a journal dump.
func WriteJournalFile(path string, txs []*types.Transaction) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o770); err != nil {
		return err
	}

	tmp := path + ".new"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec
	if err != nil {
		return err
	}

	for _, tx := range txs {
		if _, err := f.Write(tx.MarshalJournal()); err != nil {
			_ = f.Close()

			return err
		}
	}

	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

func hashSet(hashes []types.Hash) map[types.Hash]struct{} {
	set := make(map[types.Hash]struct{}, len(hashes))
	for _, h := range hashes {
		set[h] = struct{}{}
	}

	return set
}

func txHash(tx *types.Transaction) types.Hash {
	if tx.Hash == types.ZeroHash {
		tx.ComputeHash(0)
	}

	return tx.Hash
}

// RemoveFromJournal moves matching txs from the live journal into the quarantine file.
func RemoveFromJournal(journalPath, removedPath string, hashes []types.Hash) ([]*types.Transaction, error) {
	if len(hashes) == 0 {
		return nil, fmt.Errorf("no transaction hashes given")
	}

	want := hashSet(hashes)

	live, err := ReadJournalFile(journalPath)
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}

	kept := make([]*types.Transaction, 0, len(live))
	moved := make([]*types.Transaction, 0)

	for _, tx := range live {
		if _, ok := want[txHash(tx)]; ok {
			moved = append(moved, tx)
			delete(want, tx.Hash)

			continue
		}

		kept = append(kept, tx)
	}

	if len(moved) == 0 {
		return nil, fmt.Errorf("no matching transactions in journal")
	}

	quarantine, err := ReadJournalFile(removedPath)
	if err != nil {
		return nil, fmt.Errorf("read removed journal: %w", err)
	}

	if err := WriteJournalFile(journalPath, kept); err != nil {
		return nil, fmt.Errorf("write journal: %w", err)
	}

	if err := WriteJournalFile(removedPath, append(quarantine, moved...)); err != nil {
		return nil, fmt.Errorf("write removed journal: %w", err)
	}

	return moved, nil
}

// RestoreToJournal moves matching txs from the quarantine file back into the live journal.
func RestoreToJournal(journalPath, removedPath string, hashes []types.Hash) ([]*types.Transaction, error) {
	if len(hashes) == 0 {
		return nil, fmt.Errorf("no transaction hashes given")
	}

	want := hashSet(hashes)

	quarantine, err := ReadJournalFile(removedPath)
	if err != nil {
		return nil, fmt.Errorf("read removed journal: %w", err)
	}

	kept := make([]*types.Transaction, 0, len(quarantine))
	restored := make([]*types.Transaction, 0)

	for _, tx := range quarantine {
		if _, ok := want[txHash(tx)]; ok {
			restored = append(restored, tx)
			delete(want, tx.Hash)

			continue
		}

		kept = append(kept, tx)
	}

	if len(restored) == 0 {
		return nil, fmt.Errorf("no matching transactions in removed journal")
	}

	live, err := ReadJournalFile(journalPath)
	if err != nil {
		return nil, fmt.Errorf("read journal: %w", err)
	}

	if err := WriteJournalFile(removedPath, kept); err != nil {
		return nil, fmt.Errorf("write removed journal: %w", err)
	}

	if err := WriteJournalFile(journalPath, append(live, restored...)); err != nil {
		return nil, fmt.Errorf("write journal: %w", err)
	}

	return restored, nil
}
