package pebble

import (
	"errors"

	storagev2 "github.com/0xPolygon/polygon-edge/blockchain/storage"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/cockroachdb/pebble"
	"github.com/hashicorp/go-hclog"
)

// pebbleDB is the pebble implementation of the kv storage
type pebbleDB struct {
	db *pebble.DB
}

var tableMapper = map[uint8][]byte{
	storagev2.BODY:              []byte("b"), // DB key = block number + block hash + mapper, value = block body
	storagev2.DIFFICULTY:        []byte("d"), // DB key = block number + block hash + mapper, value = block total diffculty
	storagev2.HEADER:            []byte("h"), // DB key = block number + block hash + mapper, value = block header
	storagev2.RECEIPTS:          []byte("r"), // DB key = block number + block hash + mapper, value = block receipts
	storagev2.BLOCK_ACCESS_LIST: []byte("a"), // DB key = block number + block hash + mapper, value = block access list
	storagev2.CANONICAL:         {},          // DB key = block number + mapper, value = block hash
	storagev2.FORK:              {},          // DB key = FORK_KEY + mapper, value = fork hashes
	storagev2.HEAD_HASH:         {},          // DB key = HEAD_HASH_KEY + mapper, value = head hash
	storagev2.HEAD_NUMBER:       {},          // DB key = HEAD_NUMBER_KEY + mapper, value = head number
	storagev2.BLOCK_LOOKUP:      {},          // DB key = block hash + mapper, value = block number
	storagev2.TX_LOOKUP:         {},          // DB key = tx hash + mapper, value = block number
}

// NewPebbleDBStorage creates the new storage reference with pebble default options
func NewPebbleDBStorage(path string, logger hclog.Logger) (*storagev2.Storage, error) {
	var ldbs [2]storagev2.Database

	// Open pebble storage
	maindb, err := openPebbleDBStorage(path, getPebbleDBOptions())
	if err != nil {
		return nil, err
	}

	ldbs[0] = &pebbleDB{maindb}
	ldbs[1] = nil

	return storagev2.Open(logger.Named("pebble"), ldbs)
}

func openPebbleDBStorage(path string, options *pebble.Options) (*pebble.DB, error) {
	db, err := pebble.Open(path, options)
	if err != nil {
		return nil, err
	}

	return db, nil
}

func getPebbleDBOptions() *pebble.Options {
	options := &pebble.Options{
		Logger: itrie.PebbleLogger{},
	}

	return options
}

// Get retrieves the key-value pair in pebble storage
func (p *pebbleDB) Get(t uint8, k []byte) ([]byte, bool, error) {
	mc := tableMapper[t]
	k = append(k, mc...)

	data, closer, err := p.db.Get(k)
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

// Close closes the pebble storage instance
func (p *pebbleDB) Close() error {
	return p.db.Close()
}

// NewBatch creates batch for database write operations
func (p *pebbleDB) NewBatch() storagev2.Batch {
	return newBatchPebbleDB(p.db)
}
