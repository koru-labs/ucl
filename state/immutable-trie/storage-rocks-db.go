package itrie

import (
	"errors"
	"fmt"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/linxGnu/grocksdb"
)

// Correct Besu Column Family Names for Bonsai Tries
const (
	// blockchainCF  = 1
	// worldStateCF = 2
	// No longer used but retained for DB backwards compatibility
	// privateTransactionsCF = 3
	// privateStateCF        = 4
	// pruningStateCF        = 5
	accountInfoCF    = 6
	codeStorageCF    = 7
	accountStorageCF = 8
	trieBranchCF     = 9
	trieLogStorageCF = 10
	// variablesCF      = 11 // formerly GOQUORUM_PRIVATE_WORLD_STATE
	// previously supported GoQuorum private states
	// goQuorumPrivateStorageCF      = 12
	// backwardSyncHeadersCF         = 13
	// backwardSyncBlocksCF          = 14
	// backwardSyncChainCF           = 15
	// snapSyncMissingAccountRangeCF = 16
	// snapSyncAccountToFixCF        = 17
	// chainPrunerStateCF            = 18
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
	opts := getOptions(isReadOnly)
	// In NewRocksDBStorage update your slice:
	cfNames := []string{
		string(rune(accountInfoCF)),
		string(rune(accountStorageCF)), string(rune(trieBranchCF)),
		string(rune(trieLogStorageCF)), string(rune(codeStorageCF)), "default",
	}

	cfOpts := make([]*grocksdb.Options, len(cfNames))
	for i := range cfOpts {
		cfOpts[i] = opts
	}

	var (
		db        *grocksdb.DB
		cfHandles []*grocksdb.ColumnFamilyHandle
		err       error
	)

	if isReadOnly {
		db, cfHandles, err = grocksdb.OpenDbForReadOnlyColumnFamilies(
			opts, path, cfNames, cfOpts, false)
	} else {
		db, cfHandles, err = grocksdb.OpenDbColumnFamilies(
			opts, path, cfNames, cfOpts)
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
	writeOpts.SetSync(true) // Ensure data is flushed to disk for durability

	return &rocksDBStorage{
		db:               db,
		readOpts:         readOpts,
		writeOpts:        writeOpts,
		opts:             opts,
		accountInfoCF:    cfHandles[0],
		accountStorageCF: cfHandles[1],
		trieBranchCF:     cfHandles[2],
		trieLogStorageCF: cfHandles[3],
		codeStorageCF:    cfHandles[4],
	}, nil
}

// rocksDBStorage is a RocksDB-based state storage implementation
// Provides high-performance backend for Merkle Patricia Trie state storage
type rocksDBStorage struct {
	db        *grocksdb.DB           // RocksDB database instance
	readOpts  *grocksdb.ReadOptions  // Reusable read options to reduce object allocation
	writeOpts *grocksdb.WriteOptions // Reusable write options to reduce object allocation
	opts      *grocksdb.Options      // Database options for reference (not used directly in methods)
	// Besu Column Families
	accountInfoCF    *grocksdb.ColumnFamilyHandle
	accountStorageCF *grocksdb.ColumnFamilyHandle
	trieBranchCF     *grocksdb.ColumnFamilyHandle
	trieLogStorageCF *grocksdb.ColumnFamilyHandle
	codeStorageCF    *grocksdb.ColumnFamilyHandle
}

// Put stores a key-value pair
// Implements the Put method of Storage interface
// Optimized version: reuses write options to reduce object allocation
func (r *rocksDBStorage) Put(k, v []byte) error {
	prefix, realKey, err := splitKey(k)
	if err != nil {
		return err
	}

	columnFamily := r.getFamily(prefix)

	// Use reusable write options to avoid frequent create/destroy
	return r.db.PutCF(r.writeOpts, columnFamily, realKey, v)
}

// Get retrieves a key-value pair
// Implements the Get method of Storage interface
// Optimized version: reuses read options and optimizes memory allocation
func (r *rocksDBStorage) Get(k []byte) ([]byte, bool, error) {
	prefix, realKey, err := splitKey(k)
	if err != nil {
		return nil, false, err
	}

	columnFamily := r.getFamily(prefix)

	val, err := r.db.GetCF(r.readOpts, columnFamily, realKey)
	if err != nil {
		return nil, false, err
	}

	defer val.Free()

	if !val.Exists() {
		return nil, false, nil
	}

	// Get data size to avoid repeated calls
	size := val.Size()
	if size == 0 {
		return []byte{}, true, nil // Return empty byte slice instead of nil
	}

	// Optimized memory allocation: allocate exact size directly
	data := make([]byte, size)
	copy(data, val.Data())

	return data, true, nil
}

// Batch creates a batch operation instance
// Implements the Batch method of Storage interface
func (r *rocksDBStorage) Batch() Batch {
	return &rocksDBBatch{
		db:               r.db,
		writeBatch:       grocksdb.NewWriteBatch(),
		writeOpts:        r.writeOpts, // Reuse parent object's write options
		accountInfoCF:    r.accountInfoCF,
		accountStorageCF: r.accountStorageCF,
		trieBranchCF:     r.trieBranchCF,
		trieLogStorageCF: r.trieLogStorageCF,
		codeStorageCF:    r.codeStorageCF,
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
	prefix, realKey, err := splitKey(k)
	if err != nil {
		return false, err
	}

	columnFamily := r.getFamily(prefix)

	slice := r.db.KeyMayExistsCF(r.readOpts, columnFamily, realKey, "")
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

func (b *rocksDBStorage) getFamily(prefix byte) *grocksdb.ColumnFamilyHandle {
	switch prefix {
	case 'c':
		return b.codeStorageCF
	case 'a':
		return b.accountInfoCF
	case 's':
		return b.accountStorageCF
	case 'l':
		return b.trieLogStorageCF
	default:
		return b.trieBranchCF // case 'n' and default
	}
}

// rocksDBBatch is a RocksDB batch operation implementation
// Provides efficient batch writes for state storage
type rocksDBBatch struct {
	db         *grocksdb.DB           // Database reference
	writeBatch *grocksdb.WriteBatch   // Write batch
	writeOpts  *grocksdb.WriteOptions // Reusable write options to reduce object allocation
	// Besu Column Families
	accountInfoCF    *grocksdb.ColumnFamilyHandle
	accountStorageCF *grocksdb.ColumnFamilyHandle
	trieBranchCF     *grocksdb.ColumnFamilyHandle
	trieLogStorageCF *grocksdb.ColumnFamilyHandle
	codeStorageCF    *grocksdb.ColumnFamilyHandle
}

// Put adds a write operation to the batch
// Implements the Put method of Batch interface
func (b *rocksDBBatch) Put(k, v []byte) {
	prefix, realKey, err := splitKey(k)
	if err != nil {
		return
	}

	columnFamily := b.getFamily(prefix)

	b.writeBatch.PutCF(columnFamily, realKey, v)
}

// Write executes the batch write
// Implements the Write method of Batch interface
// Optimized version: reuses write options to reduce object allocation
func (b *rocksDBBatch) Write() error {
	// Use reusable write options to avoid frequent create/destroy
	defer b.writeBatch.Destroy() // Ensure WriteBatch is properly released

	return b.db.Write(b.writeOpts, b.writeBatch)
}

func (b *rocksDBBatch) getFamily(prefix byte) *grocksdb.ColumnFamilyHandle {
	switch prefix {
	case 'c':
		return b.codeStorageCF
	case 'a':
		return b.accountInfoCF
	case 's':
		return b.accountStorageCF
	case 'l':
		return b.trieLogStorageCF
	default:
		return b.trieBranchCF // case 'n' and default
	}
}

func getOptions(isReadOnly bool) *grocksdb.Options {
	opts := grocksdb.NewDefaultOptions()

	// State storage specific configuration
	opts.SetCreateIfMissing(!isReadOnly)          // Create database if missing
	opts.SetMaxOpenFiles(2000)                    // State storage needs more file handles
	opts.SetWriteBufferSize(512 * 1024 * 1024)    // Write buffer: 512MB (more state writes)
	opts.SetMaxWriteBufferNumber(8)               // Maximum write buffer count
	opts.SetTargetFileSizeBase(128 * 1024 * 1024) // Target file size: 128MB

	// Pipeline write optimization
	// Multiple write requests can be executed in parallel: WAL write and MemTable insertion.
	opts.SetEnablePipelinedWrite(true)
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

	return opts
}

func splitKey(k []byte) (byte, []byte, error) {
	if len(k) <= 32 {
		return 'n', k, nil
	}

	if len(k) == 0 {
		return 0, nil, errors.New("empty key")
	}

	if len(k) >= 4 && string(k[:4]) == "code" {
		return 'c', k[4:], nil
	}

	return k[0], k[1:], nil
}

var (
	_ Batch   = (*rocksDBBatch)(nil)
	_ Storage = (*rocksDBStorage)(nil)
)
