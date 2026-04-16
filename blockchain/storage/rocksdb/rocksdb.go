package rocksdb

import (
	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/hashicorp/go-hclog"
	"github.com/linxGnu/grocksdb"
)

// rocksDB is the rocksdb implementation of the kv storage
type rocksDB struct {
	db   *grocksdb.DB
	ro   *grocksdb.ReadOptions
	wo   *grocksdb.WriteOptions
	opts *grocksdb.Options // RocksDB configuration options
}

var (
	_ storage.Database = (*rocksDB)(nil)
	_ storage.Batch    = (*batchRocksDB)(nil)
)

var tableMapper = map[uint8][]byte{
	storage.BODY:         []byte("b"), // DB key = block number + block hash + mapper, value = block body
	storage.DIFFICULTY:   []byte("d"), // DB key = block number + block hash + mapper, value = block total diffculty
	storage.HEADER:       []byte("h"), // DB key = block number + block hash + mapper, value = block header
	storage.RECEIPTS:     []byte("r"), // DB key = block number + block hash + mapper, value = block receipts
	storage.CANONICAL:    {},          // DB key = block number + mapper, value = block hash
	storage.FORK:         {},          // DB key = FORK_KEY + mapper, value = fork hashes
	storage.HEAD_HASH:    {},          // DB key = HEAD_HASH_KEY + mapper, value = head hash
	storage.HEAD_NUMBER:  {},          // DB key = HEAD_NUMBER_KEY + mapper, value = head number
	storage.BLOCK_LOOKUP: {},          // DB key = block hash + mapper, value = block number
	storage.TX_LOOKUP:    {},          // DB key = tx hash + mapper, value = block number
}

// NewRocksDBStorage creates the new storage reference with rocksdb default options
func NewRocksDBStorage(path string, logger hclog.Logger) (*storage.Storage, error) {
	var ldbs [2]storage.Database

	opts := getRocksDBOptions()

	db, err := grocksdb.OpenDb(opts, path)
	if err != nil {
		return nil, err
	}

	// Create reusable read options
	readOpts := grocksdb.NewDefaultReadOptions()
	// Set read optimization options
	readOpts.SetFillCache(true)        // Enable cache filling
	readOpts.SetVerifyChecksums(false) // Disable checksums in storage scenarios for performance

	// Create reusable write options
	writeOpts := grocksdb.NewDefaultWriteOptions()
	// Set write optimization options
	writeOpts.SetSync(false) // Async write for performance, rely on system cache

	ldbs[0] = &rocksDB{
		db:   db,
		ro:   readOpts,
		wo:   writeOpts,
		opts: opts,
	}
	ldbs[1] = nil

	return storage.Open(logger.Named("rocksdb"), ldbs)
}

func getRocksDBOptions() *grocksdb.Options {
	// Create RocksDB options configuration
	opts := grocksdb.NewDefaultOptions()

	// Basic configuration optimization
	opts.SetCreateIfMissing(true)                // Create database if it doesn't exist
	opts.SetMaxOpenFiles(1000)                   // Maximum number of open files
	opts.SetWriteBufferSize(64 * 1024 * 1024)    // Write buffer size: 64MB
	opts.SetMaxWriteBufferNumber(3)              // Maximum number of write buffers
	opts.SetTargetFileSizeBase(64 * 1024 * 1024) // Target file size: 64MB

	// Compression configuration
	opts.SetCompression(grocksdb.SnappyCompression) // Use Snappy compression

	// Create bloom filter and set table options
	filter := grocksdb.NewBloomFilter(10)
	blockOpts := grocksdb.NewDefaultBlockBasedTableOptions()
	blockOpts.SetFilterPolicy(filter)
	blockOpts.SetBlockCache(grocksdb.NewLRUCache(128 * 1024 * 1024)) // 128MB cache
	opts.SetBlockBasedTableFactory(blockOpts)

	return opts
}

// Get retrieves the key-value pair in rocksdb storage
func (r *rocksDB) Get(t uint8, k []byte) ([]byte, bool, error) {
	mc := tableMapper[t]
	k = append(k, mc...)

	slice, err := r.db.Get(r.ro, k)
	if err != nil {
		return nil, false, err
	}

	defer slice.Free()

	// Quick check if data exists
	if !slice.Exists() {
		return nil, false, nil
	}

	// Get data size to avoid repeated calls
	size := slice.Size()
	if size == 0 {
		return []byte{}, true, nil // Return empty byte slice instead of nil
	}

	// Optimized memory allocation: allocate exact size directly
	data := make([]byte, size)
	copy(data, slice.Data())

	return data, true, nil
}

// Close closes the rocksdb storage instance
func (r *rocksDB) Close() error {
	// Release reusable write options
	if r.wo != nil {
		r.wo.Destroy()
	}

	// Release reusable read options
	if r.ro != nil {
		r.ro.Destroy()
	}

	// Close database connection
	r.db.Close()

	// Release database options
	r.opts.Destroy()

	return nil
}

// NewBatch creates batch for database write operations
func (r *rocksDB) NewBatch() storage.Batch {
	return newBatchRocksDB(r.db, r.wo)
}
