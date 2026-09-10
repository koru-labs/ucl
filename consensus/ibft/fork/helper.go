package fork

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/validators/store/snapshot"
)

// SnapshotTrimResult is the outcome of clamping IBFT snapshot files to a new head.
type SnapshotTrimResult struct {
	Dir          string
	OldLastBlock uint64
	NewLastBlock uint64
	Dropped      int
	Kept         int
	Skipped      string
}

// TrimSnapshots drops validator snapshots after newHead and clamps LastBlock.
// Missing or unreadable files are skipped (the chain unwind still stands).
func TrimSnapshots(dirPath string, newHead uint64, dryRun bool) (*SnapshotTrimResult, error) {
	res := &SnapshotTrimResult{
		Dir:          dirPath,
		NewLastBlock: newHead,
	}

	metaPath := filepath.Join(dirPath, snapshotMetadataFilename)
	snapsPath := filepath.Join(dirPath, snapshotSnapshotsFilename)

	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		if _, snapErr := os.Stat(snapsPath); os.IsNotExist(snapErr) {
			res.Skipped = "no snapshot files"

			return res, nil
		}
	}

	meta, err := loadSnapshotMetadata(metaPath)
	if isJSONSyntaxError(err) {
		res.Skipped = "unreadable snapshot metadata"

		return res, nil
	} else if err != nil {
		return nil, err
	}

	snaps, err := loadSnapshots(snapsPath)
	if isJSONSyntaxError(err) {
		res.Skipped = "unreadable snapshots file"

		return res, nil
	} else if err != nil {
		return nil, err
	}

	if meta != nil {
		res.OldLastBlock = meta.LastBlock
	}

	kept := make([]*snapshot.Snapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap == nil {
			continue
		}

		if snap.Number > newHead {
			res.Dropped++

			continue
		}

		kept = append(kept, snap)
	}

	res.Kept = len(kept)

	if meta == nil {
		meta = &snapshot.SnapshotMetadata{}
	}

	if meta.LastBlock > newHead {
		meta.LastBlock = newHead
	}

	res.NewLastBlock = meta.LastBlock

	if dryRun {
		return res, nil
	}

	if err := writeDataStore(metaPath, meta); err != nil {
		return nil, err
	}

	if err := writeDataStore(snapsPath, kept); err != nil {
		return nil, err
	}

	return res, nil
}

// loadSnapshotMetadata loads Metadata from file
func loadSnapshotMetadata(path string) (*snapshot.SnapshotMetadata, error) {
	var meta *snapshot.SnapshotMetadata
	if err := readDataStore(path, &meta); err != nil {
		return nil, err
	}

	return meta, nil
}

// loadSnapshots loads Snapshots from file
func loadSnapshots(path string) ([]*snapshot.Snapshot, error) {
	snaps := []*snapshot.Snapshot{}
	if err := readDataStore(path, &snaps); err != nil {
		return nil, err
	}

	return snaps, nil
}

// readDataStore attempts to read the specific file from file storage
// return nil if the file doesn't exist
func readDataStore(path string, obj interface{}) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(data, obj); err != nil {
		return err
	}

	return nil
}

// writeDataStore attempts to write the specific file to file storage
func writeDataStore(path string, obj interface{}) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}

	if err := common.SaveFileSafe(path, data, 0660); err != nil {
		return err
	}

	return nil
}
