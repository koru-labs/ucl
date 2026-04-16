package itrie

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/linxGnu/grocksdb"
)

// NewRocksDBStorage creates a RocksDB-based state storage instance
// Parameters:
//   - path: RocksDB database path
//   - isReadOnly: whether to open the database in read-only mode
//
// Returns: (storage instance, error)
//
// Provides RocksDB storage backend for Merkle Patricia Trie
// Specially optimized for state storage scenarios
func NewRocksDBStorage(path string, isReadOnly bool) (Storage, error) {
	// Create RocksDB configuration optimized for state storage
	opts := grocksdb.NewDefaultOptions()

	// State storage specific configuration
	opts.SetCreateIfMissing(!isReadOnly)          // Create database if missing
	opts.SetMaxOpenFiles(2000)                    // State storage needs more file handles
	opts.SetWriteBufferSize(512 * 1024 * 1024)    // Write buffer: 512MB (more state writes)
	opts.SetMaxWriteBufferNumber(8)               // Maximum write buffer count
	opts.SetTargetFileSizeBase(128 * 1024 * 1024) // Target file size: 128MB

	// Pipeline write optimization
	opts.SetEnablePipelinedWrite(true)        // Multiple write requests can be executed in parallel: WAL write and MemTable insertion.
	opts.SetLevel0FileNumCompactionTrigger(8) // Delay L0 compaction
	opts.SetMinWriteBufferNumberToMerge(2)    // Merge 2 MemTables

	// Compression configuration - use stronger compression for state data
	opts.SetCompression(grocksdb.LZ4Compression)

	// Bloom filter - frequent state queries, use larger bloom filter
	filter := grocksdb.NewBloomFilter(15)
	blockBasedTableOptions := grocksdb.NewDefaultBlockBasedTableOptions()
	blockBasedTableOptions.SetBlockCache(grocksdb.NewLRUCache(256 * 1024 * 1024)) // 256MB cache
	blockBasedTableOptions.SetFilterPolicy(filter)
	blockBasedTableOptions.SetBlockSize(16 * 1024) // 16KB Block
	opts.SetBlockBasedTableFactory(blockBasedTableOptions)

	opts.SetAllowConcurrentMemtableWrites(true)
	opts.SetEnableWriteThreadAdaptiveYield(true)
	opts.SetUseAdaptiveMutex(true)
	opts.SetWALBytesPerSync(512 * 1024)
	opts.SetRecycleLogFileNum(4)
	opts.SetMaxBackgroundJobs(16)
	opts.SetMaxSubcompactions(4)

	var (
		db  *grocksdb.DB
		err error
	)

	if isReadOnly {
		db, err = grocksdb.OpenDbForReadOnly(opts, path, true)
	} else {
		db, err = grocksdb.OpenDb(opts, path)
	}

	if err != nil {
		opts.Destroy()
		return nil, err
	}

	// Create reusable read options
	readOpts := grocksdb.NewDefaultReadOptions()
	// Set read optimization options
	readOpts.SetFillCache(true)        // Enable cache filling
	readOpts.SetVerifyChecksums(false) // Disable checksums for performance in state read scenarios

	// Create reusable write options
	writeOpts := grocksdb.NewDefaultWriteOptions()
	// Set write optimization options
	writeOpts.SetSync(false) // Async writes for better performance, rely on system cache

	return &rocksDBStorage{
		db:        db,
		opts:      opts,
		readOpts:  readOpts,
		writeOpts: writeOpts,
	}, nil
}

// rocksDBStorage is a RocksDB-based state storage implementation
// Provides high-performance backend for Merkle Patricia Trie state storage
type rocksDBStorage struct {
	db        *grocksdb.DB           // RocksDB database instance
	opts      *grocksdb.Options      // RocksDB configuration options
	readOpts  *grocksdb.ReadOptions  // Reusable read options to reduce object allocation
	writeOpts *grocksdb.WriteOptions // Reusable write options to reduce object allocation
}

// Put stores a key-value pair
// Implements the Put method of Storage interface
// Optimized version: reuses write options to reduce object allocation
func (r *rocksDBStorage) Put(k, v []byte) error {
	// Use reusable write options to avoid frequent create/destroy
	return r.db.Put(r.writeOpts, k, v)
}

// Get retrieves a key-value pair
// Implements the Get method of Storage interface
// Optimized version: reuses read options and optimizes memory allocation
func (r *rocksDBStorage) Get(k []byte) ([]byte, bool, error) {
	// Use reusable read options to avoid frequent create/destroy
	slice, err := r.db.Get(r.readOpts, k)
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

// Batch creates a batch operation instance
// Implements the Batch method of Storage interface
func (r *rocksDBStorage) Batch() Batch {
	return &rocksDBBatch{
		db:         r.db,
		writeBatch: grocksdb.NewWriteBatch(),
		writeOpts:  r.writeOpts, // Reuse parent object's write options
	}
}

// SetCode stores smart contract code
// Implements the SetCode method of Storage interface
func (r *rocksDBStorage) SetCode(hash types.Hash, code []byte) error {
	return r.Put(GetCodeKey(hash), code)
}

// GetCode retrieves smart contract code
// Implements the GetCode method of Storage interface
func (r *rocksDBStorage) GetCode(hash types.Hash) ([]byte, bool) {
	//code, exists, err := r.Get(hash.Bytes())
	code, exists, err := r.Get(GetCodeKey(hash))
	if err != nil {
		return nil, false
	}

	return code, exists
}

func (r *rocksDBStorage) Compact(start []byte, limit []byte) error {
	r.db.CompactRange(grocksdb.Range{
		Start: start,
		Limit: limit,
	})

	return nil
}

func (r *rocksDBStorage) Has(k []byte) (bool, error) {
	ro := grocksdb.NewDefaultReadOptions()
	defer ro.Destroy()

	slice := r.db.KeyMayExists(ro, k, "")
	if slice == nil {
		return false, nil
	}

	defer slice.Free()

	return true, nil
}

func (r *rocksDBStorage) Stat(property string) (string, error) {
	return "", fmt.Errorf("stat method not supported at database level")
}

// Close closes the storage connection
// Implements the Close method of Storage interface, following RocksDB resource management specifications
func (r *rocksDBStorage) Close() error {
	// Release reusable write options
	if r.writeOpts != nil {
		r.writeOpts.Destroy()
	}

	// Release reusable read options
	if r.readOpts != nil {
		r.readOpts.Destroy()
	}

	// Close database connection
	r.db.Close()

	// Release database options
	r.opts.Destroy()

	return nil
}

// rocksDBBatch is a RocksDB batch operation implementation
// Provides efficient batch writes for state storage
type rocksDBBatch struct {
	db         *grocksdb.DB           // Database reference
	writeBatch *grocksdb.WriteBatch   // Write batch
	writeOpts  *grocksdb.WriteOptions // Reusable write options to reduce object allocation
}

// Put adds a write operation to the batch
// Implements the Put method of Batch interface
func (b *rocksDBBatch) Put(k, v []byte) {
	b.writeBatch.Put(k, v)
}

// Write executes the batch write
// Implements the Write method of Batch interface
// Optimized version: reuses write options to reduce object allocation
func (b *rocksDBBatch) Write() error {
	// Use reusable write options to avoid frequent create/destroy
	defer b.writeBatch.Destroy() // Ensure WriteBatch is properly released

	return b.db.Write(b.writeOpts, b.writeBatch)
}

var (
	_ Batch   = (*rocksDBBatch)(nil)
	_ Storage = (*rocksDBStorage)(nil)
)
