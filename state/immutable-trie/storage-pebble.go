package itrie

import (
	"errors"
	"fmt"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/cockroachdb/pebble"
	"github.com/hashicorp/go-hclog"
)

// PebbleStorage is a k/v storage using pebble db
type pebbleStorage struct {
	db *pebble.DB // Underlying pebble storage engine
}

// pebbleBatch is a batch write for pebble db
type pebbleBatch struct {
	db    *pebble.DB // Underlying pebble storage engine
	batch *pebble.Batch
}

func NewPebbleDBStorageWithOpts(path string, opts *pebble.Options) (Storage, error) {
	trieDB, err := pebble.Open(path, opts)
	if err != nil {
		return nil, err
	}

	return &pebbleStorage{db: trieDB}, nil
}

func NewPebbleDBStorage(path string, logger hclog.Logger) (Storage, error) {
	opts := &pebble.Options{Logger: PebbleLogger{}}

	db, err := pebble.Open(path, opts)
	if err != nil {
		return nil, err
	}

	return &pebbleStorage{db}, nil
}

func (b *pebbleBatch) Put(k, v []byte) {
	_ = b.batch.Set(k, v, nil)
}

func (b *pebbleBatch) Write() error {
	return b.batch.Commit(nil)
}

func (ps *pebbleStorage) SetCode(hash types.Hash, code []byte) error {
	return ps.Put(GetCodeKey(hash), code)
}

func (ps *pebbleStorage) GetCode(hash types.Hash) ([]byte, bool) {
	res, ok, err := ps.Get(GetCodeKey(hash))
	if err != nil {
		return nil, false
	}

	return res, ok
}

func (ps *pebbleStorage) Batch() Batch {
	return &pebbleBatch{db: ps.db, batch: ps.db.NewBatch()}
}

func (ps *pebbleStorage) Put(k, v []byte) error {
	return ps.db.Set(k, v, nil)
}

func (ps *pebbleStorage) Get(k []byte) ([]byte, bool, error) {
	data, closer, err := ps.db.Get(k)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, false, nil
		}

		return nil, false, err
	}

	ret := make([]byte, len(data))
	copy(ret, data)

	if err = closer.Close(); err != nil {
		return nil, false, err
	}

	return ret, true, nil
}

func (ps *pebbleStorage) Has(k []byte) (bool, error) {
	_, closer, err := ps.db.Get(k)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}

		return false, err
	}

	if err = closer.Close(); err != nil {
		return false, err
	}

	return true, nil
}

// Stat returns a particular internal stat of the database.
func (ps *pebbleStorage) Stat(property string) (string, error) {
	return "", fmt.Errorf("stat method not supported at database level")
}

// Compact flattens the underlying data store for the given key range. In essence,
// deleted and overwritten versions are discarded, and the data is rearranged to
// reduce the cost of operations needed to access them.
//
// A nil start is treated as a key before all keys in the data store; a nil limit
// is treated as a key after all keys in the data store. If both is nil then it
// will compact entire data store.
func (ps *pebbleStorage) Compact(start []byte, limit []byte) error {
	return ps.db.Compact(start, limit, true)
}

func (ps *pebbleStorage) Close() error {
	return ps.db.Close()
}

// PebbleLogger is just a noop logger to disable Pebble's internal logger.
type PebbleLogger struct{}

func (l PebbleLogger) Infof(format string, args ...interface{}) {
}

func (l PebbleLogger) Errorf(format string, args ...interface{}) {
}

func (l PebbleLogger) Fatalf(format string, args ...interface{}) {
	panic(fmt.Errorf("pebble fatal: "+format, args...)) //nolint:gocritic
}
