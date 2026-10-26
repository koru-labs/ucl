package export

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/0xPolygon/polygon-edge/types"
)

// Cache keeps every frame computed, one file per block: the block's hash (32 bytes), then the
// frame. IBFT blocks are final, so an entry never goes stale; the hash still guards against a cache
// directory reused with another chain's data. The directory carries the format version, so a format
// change starts an empty cache.
type Cache struct {
	dir string
}

// blocksPerDir keeps directories small: 10,000 files each.
const blocksPerDir = 10_000

func NewCache(dir string) (*Cache, error) {
	dir = filepath.Join(dir, "v"+strconv.Itoa(FormatVersion))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	return &Cache{dir: dir}, nil
}

func (c *Cache) path(number uint64) string {
	return filepath.Join(
		c.dir,
		strconv.FormatUint(number/blocksPerDir, 10),
		strconv.FormatUint(number, 10)+".rec",
	)
}

// Get returns the cached frame of block `number` if it is there and was made for `hash`. A damaged
// entry (wrong hash, short file, bad checksum) is removed and reported as missing.
func (c *Cache) Get(number uint64, hash types.Hash) ([]byte, bool) {
	data, err := os.ReadFile(c.path(number))
	if err != nil {
		return nil, false
	}

	if len(data) < types.HashLength || !bytes.Equal(data[:types.HashLength], hash.Bytes()) {
		_ = os.Remove(c.path(number))

		return nil, false
	}

	frame := data[types.HashLength:]
	if _, err := DecodeFrame(frame); err != nil {
		_ = os.Remove(c.path(number))

		return nil, false
	}

	return frame, true
}

// Put stores a frame, atomically: a reader sees the whole entry or none.
func (c *Cache) Put(number uint64, hash types.Hash, frame []byte) error {
	path := c.path(number)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}

	_, err = tmp.Write(append(hash.Bytes(), frame...))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(tmp.Name())

		return err
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("cache entry for block %d: %w", number, err)
	}

	return nil
}
