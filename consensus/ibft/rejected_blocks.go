package ibft

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
)

const (
	RejectedBlocksFileName    = "rejected-blocks.rlp"
	DefaultRejectedBlocksKeep = 16
	rejectedBlocksMagic       = "RJ02"
	// reasonLen + unix + blockLen + extraLen
	rejectedBlocksRecordHeader = 4 + 8 + 4 + 4
	txOutcomeSize              = 32 + 8 + 8 + 32 + 32
)

// RejectedBlock is one IBFT proposal that failed local validation.
type RejectedBlock struct {
	Reason         string
	Timestamp      int64
	Block          *types.Block
	LocalStateRoot types.Hash
	Outcomes       []state.TxExecOutcome
}

// RejectedBlocksPath is the on-disk ring under a validator data dir.
func RejectedBlocksPath(dataDir string) string {
	return filepath.Join(dataDir, "consensus", RejectedBlocksFileName)
}

type rejectedBlockStore struct {
	path   string
	keep   int
	logger hclog.Logger
	lock   sync.Mutex
}

func newRejectedBlockStore(path string, keep int, logger hclog.Logger) *rejectedBlockStore {
	if keep <= 0 {
		keep = DefaultRejectedBlocksKeep
	}

	return &rejectedBlockStore{
		path:   path,
		keep:   keep,
		logger: logger,
	}
}

func (s *rejectedBlockStore) record(
	block *types.Block,
	reason string,
	localRoot types.Hash,
	outcomes []state.TxExecOutcome,
) {
	if s == nil || block == nil {
		return
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	recs, err := readRejectedBlocks(s.path)
	if err != nil {
		s.logger.Error("failed to load rejected blocks", "err", err)

		recs = nil
	}

	recs = append(recs, RejectedBlock{
		Reason:         reason,
		Timestamp:      time.Now().UTC().Unix(),
		Block:          block,
		LocalStateRoot: localRoot,
		Outcomes:       outcomes,
	})

	if len(recs) > s.keep {
		recs = recs[len(recs)-s.keep:]
	}

	if err := writeRejectedBlocks(s.path, recs); err != nil {
		s.logger.Error("failed to persist rejected block", "err", err, "number", block.Number())
	}
}

// LoadRejectedBlocks reads the on-disk ring. Missing file is empty.
func LoadRejectedBlocks(path string) ([]RejectedBlock, error) {
	return readRejectedBlocks(path)
}

func readRejectedBlocks(path string) ([]RejectedBlock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	if len(data) < 4 || string(data[:4]) != rejectedBlocksMagic {
		return nil, fmt.Errorf("invalid rejected-blocks file")
	}

	data = data[4:]
	recs := make([]RejectedBlock, 0)

	for len(data) > 0 {
		if len(data) < rejectedBlocksRecordHeader {
			return nil, fmt.Errorf("truncated rejected-blocks record header")
		}

		reasonLen := binary.LittleEndian.Uint32(data[0:4])
		unix := int64(binary.LittleEndian.Uint64(data[4:12]))
		blockLen := binary.LittleEndian.Uint32(data[12:16])
		extraLen := binary.LittleEndian.Uint32(data[16:20])
		data = data[rejectedBlocksRecordHeader:]

		need := int(reasonLen) + int(blockLen) + int(extraLen)
		if len(data) < need {
			return nil, fmt.Errorf("truncated rejected-blocks record body")
		}

		reason := string(data[:reasonLen])
		data = data[reasonLen:]

		block := &types.Block{}
		if err := block.UnmarshalRLP(data[:blockLen]); err != nil {
			return nil, fmt.Errorf("decode rejected block: %w", err)
		}

		data = data[blockLen:]

		localRoot, outcomes, err := decodeOutcomes(data[:extraLen])
		if err != nil {
			return nil, err
		}

		data = data[extraLen:]

		recs = append(recs, RejectedBlock{
			Reason:         reason,
			Timestamp:      unix,
			Block:          block,
			LocalStateRoot: localRoot,
			Outcomes:       outcomes,
		})
	}

	return recs, nil
}

func writeRejectedBlocks(path string, recs []RejectedBlock) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	tmp := path + ".new"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec
	if err != nil {
		return err
	}

	if _, err := f.Write([]byte(rejectedBlocksMagic)); err != nil {
		_ = f.Close()

		return err
	}

	for _, rec := range recs {
		if rec.Block == nil {
			continue
		}

		reason := []byte(rec.Reason)
		blockRLP := rec.Block.MarshalRLP()
		extra := encodeOutcomes(rec.LocalStateRoot, rec.Outcomes)

		hdr := make([]byte, rejectedBlocksRecordHeader)
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(reason)))
		binary.LittleEndian.PutUint64(hdr[4:12], uint64(rec.Timestamp))
		binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(blockRLP)))
		binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(extra)))

		if _, err := f.Write(hdr); err != nil {
			_ = f.Close()

			return err
		}

		if _, err := f.Write(reason); err != nil {
			_ = f.Close()

			return err
		}

		if _, err := f.Write(blockRLP); err != nil {
			_ = f.Close()

			return err
		}

		if _, err := f.Write(extra); err != nil {
			_ = f.Close()

			return err
		}
	}

	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

func encodeOutcomes(localRoot types.Hash, outcomes []state.TxExecOutcome) []byte {
	buf := make([]byte, 32+4+len(outcomes)*txOutcomeSize)
	copy(buf[:32], localRoot[:])
	binary.LittleEndian.PutUint32(buf[32:36], uint32(len(outcomes)))

	off := 36
	for _, o := range outcomes {
		copy(buf[off:off+32], o.Hash[:])
		binary.LittleEndian.PutUint64(buf[off+32:off+40], uint64(o.Status))
		binary.LittleEndian.PutUint64(buf[off+40:off+48], o.GasUsed)
		copy(buf[off+48:off+80], o.ReturnHash[:])
		copy(buf[off+80:off+112], o.DeltaHash[:])
		off += txOutcomeSize
	}

	return buf
}

func decodeOutcomes(data []byte) (types.Hash, []state.TxExecOutcome, error) {
	if len(data) == 0 {
		return types.ZeroHash, nil, nil
	}

	if len(data) < 36 {
		return types.ZeroHash, nil, fmt.Errorf("truncated rejected-block outcomes")
	}

	var localRoot types.Hash
	copy(localRoot[:], data[:32])

	count := binary.LittleEndian.Uint32(data[32:36])
	data = data[36:]

	if len(data) != int(count)*txOutcomeSize {
		return types.ZeroHash, nil, fmt.Errorf("invalid rejected-block outcomes size")
	}

	out := make([]state.TxExecOutcome, 0, count)
	for i := uint32(0); i < count; i++ {
		var rec state.TxExecOutcome
		copy(rec.Hash[:], data[:32])
		rec.Status = types.ReceiptStatus(binary.LittleEndian.Uint64(data[32:40]))
		rec.GasUsed = binary.LittleEndian.Uint64(data[40:48])
		copy(rec.ReturnHash[:], data[48:80])
		copy(rec.DeltaHash[:], data[80:112])
		out = append(out, rec)
		data = data[txOutcomeSize:]
	}

	return localRoot, out, nil
}
