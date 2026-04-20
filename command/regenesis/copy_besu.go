package regenesis

import (
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/linxGnu/grocksdb"
	"github.com/umbracle/fastrlp"
)

type besuColumnFamily int

const (
	// blockchainCF  = 1
	// worldStateCF = 2
	// No longer used but retained for DB backwards compatibility
	// privateTransactionsCF = 3
	// privateStateCF        = 4
	// pruningStateCF        = 5
	accountInfoCF    besuColumnFamily = 6
	codeStorageCF    besuColumnFamily = 7
	accountStorageCF besuColumnFamily = 8
	trieBranchCF     besuColumnFamily = 9
	trieLogStorageCF besuColumnFamily = 10
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

// NewRocksDBStorage creates a RocksDB-based state storage instance
// Parameters:
//   - path: RocksDB database path
//   - isReadOnly: whether to open the database in read-only mode
//
// Returns: (storage instance, error)
//
// Provides RocksDB storage backend for Merkle Patricia Trie
// Specially optimized for state storage scenarios
func newRocksDBStorage(path string, isReadOnly bool) (*rocksDBStorage, error) {
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

func (r *rocksDBStorage) Iterate(column besuColumnFamily, handler func(k, v []byte) error) error {
	columnFamily := r.getColumnFamilyHandle(column)
	it := r.db.NewIteratorCF(r.readOpts, columnFamily)
	defer it.Close()

	for it.SeekToFirst(); it.Valid(); it.Next() {
		k := it.Key()
		v := it.Value()

		err := handler(k.Data(), v.Data())
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *rocksDBStorage) Get(k []byte, column besuColumnFamily) ([]byte, bool, error) {
	columnFamily := r.getColumnFamilyHandle(column)

	val, err := r.db.GetCF(r.readOpts, columnFamily, k)
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

func (r *rocksDBStorage) getColumnFamilyHandle(column besuColumnFamily) *grocksdb.ColumnFamilyHandle {
	switch column {
	case accountInfoCF:
		return r.accountInfoCF
	case accountStorageCF:
		return r.accountStorageCF
	case trieLogStorageCF:
		return r.trieLogStorageCF
	case codeStorageCF:
		return r.codeStorageCF
	default: // case trieBranchCF:
		return r.trieBranchCF
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

type accountStorageKey struct {
	AddrHash types.Hash
	SlotHash types.Hash
}

func (k accountStorageKey) Bytes() []byte {
	r := make([]byte, 64)
	copy(r, k.AddrHash.Bytes())
	copy(r[32:], k.SlotHash.Bytes())

	return r
}

func (k accountStorageKey) String() string {
	return fmt.Sprintf("AddrHash: %s, SlotHash: %s", k.AddrHash, k.SlotHash)
}

func parseAccountStorageKey(data []byte) (key accountStorageKey, err error) {
	if len(data) != 64 {
		return key, fmt.Errorf("invalid account storage key length: expected 64 bytes, got %d", len(data))
	}

	copy(key.AddrHash[:], data[:32])
	copy(key.SlotHash[:], data[32:])

	return key, nil
}

type rawAccount struct {
	Nonce    uint64
	Balance  *big.Int
	Root     types.Hash
	CodeHash types.Hash
}

func (ra rawAccount) String() string {
	return fmt.Sprintf("Nonce: %d, Balance: %s, Root: %s, CodeHash: %s",
		ra.Nonce, ra.Balance, ra.Root, ra.CodeHash)
}

var parserPool fastrlp.ParserPool

func parseRawAccount(data []byte) (*rawAccount, error) {
	p := parserPool.Get()
	defer parserPool.Put(p)

	res, err := p.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse account data: %w", err)
	}

	balanceBI := new(big.Int)
	rootHash := types.EmptyRootHash
	codeHash := types.EmptyCodeHash
	elsCnt := res.Elems()

	nonce, err := res.Get(0).GetUint64()
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	err = res.Get(1).GetBigInt(balanceBI)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	if elsCnt > 2 {
		err = res.Get(2).GetHash(rootHash[:])
		if err != nil {
			return nil, fmt.Errorf("failed to get root hash: %w", err)
		}
	}

	if elsCnt > 3 {
		err = res.Get(3).GetHash(codeHash[:])
		if err != nil {
			return nil, fmt.Errorf("failed to get code hash: %w", err)
		}
	}

	if elsCnt > 4 {
		return nil, fmt.Errorf("unexpected extra fields in account data: %d", elsCnt)
	}

	return &rawAccount{
		Nonce:    nonce,
		Balance:  balanceBI,
		Root:     rootHash,
		CodeHash: codeHash,
	}, nil
}

func copyTrieBesu(srcPath string, dstStorage itrie.Storage, outputter command.OutputFormatter) error {
	srcStorage, err := newRocksDBStorage(srcPath, true)
	if err != nil {
		return err
	}

	defer srcStorage.Close()

	rootHashBytes, exists, err := srcStorage.Get(types.StringToBytes(besuWorldRootHashKey), trieBranchCF)
	if err != nil {
		return fmt.Errorf("get besu world root hash error: %w", err)
	} else if !exists {
		return fmt.Errorf("besu world root hash not found")
	}

	trieRoot := types.BytesToHash(rootHashBytes)

	outputter.Write(fmt.Appendf(nil, "Besu trie root: %s\n", trieRoot))

	accountsStorage := make(map[types.Hash]map[types.Hash][]byte)

	err = srcStorage.Iterate(accountStorageCF, func(k, v []byte) error {
		key, err := parseAccountStorageKey(k)
		if err != nil {
			return fmt.Errorf("failed to parse account storage key: %w", err)
		}

		if _, exists := accountsStorage[key.AddrHash]; !exists {
			accountsStorage[key.AddrHash] = make(map[types.Hash][]byte)
		}

		accountsStorage[key.AddrHash][key.SlotHash] = v

		return nil
	})

	objects := []*state.ObjectBesu(nil)

	// Implement the logic to copy data from srcStorage to dstStorage
	err = srcStorage.Iterate(accountInfoCF, func(k, v []byte) error {
		account, err := parseRawAccount(v)
		if err != nil {
			return fmt.Errorf("failed to parse account for key %s: %w", hex.EncodeToString(k), err)
		}

		object := &state.ObjectBesu{
			Deleted:   false,
			AddrHash:  types.BytesToHash(k),
			Nonce:     account.Nonce,
			Balance:   account.Balance,
			Root:      account.Root,
			CodeHash:  account.CodeHash,
			DirtyCode: false,
			Code:      nil,
			Storage:   nil,
		}

		if account.CodeHash != types.EmptyCodeHash {
			code, exists, err := srcStorage.Get(account.CodeHash[:], codeStorageCF)
			if err != nil {
				return fmt.Errorf("get code for account %s error: %w", hex.EncodeToString(k), err)
			} else if !exists {
				return fmt.Errorf("code for account %s not found", hex.EncodeToString(k))
			} else {
				object.DirtyCode = true
				object.Code = code
			}
		}

		items := accountsStorage[object.AddrHash]
		if len(items) > 0 {
			object.ExpectedRoot = account.Root
			object.Root = types.EmptyRootHash // will be recalculated during commit
			object.Storage = make([]*state.StorageObjectBesu, 0, len(items))

			for slotHash, val := range items {
				object.Storage = append(object.Storage, &state.StorageObjectBesu{
					Deleted:  false,
					SlotHash: slotHash,
					Val:      val,
				})

				outputter.Write(fmt.Appendf(nil, "Account storage key: %s, value: %s\n",
					slotHash, hex.EncodeToString(v)))
			}
		}

		objects = append(objects, object)

		outputter.Write(fmt.Appendf(nil, "Account keccak addr = %s, val = %s, slots = %d\n",
			object.AddrHash, account, len(object.Storage)))

		return nil
	})

	snapshot, err := itrie.NewState(dstStorage).NewSnapshot(types.ZeroHash)
	if err != nil {
		return fmt.Errorf("create snapshot error: %w", err)
	}

	snapshot, root, err := (snapshot.(*itrie.Snapshot)).CommitBesu(objects)
	if err != nil {
		return fmt.Errorf("commit besu error: %w", err)
	}

	if rootHash := types.BytesToHash(root); rootHash != trieRoot {
		return fmt.Errorf("incorrect trie root error: expected %s, got %s", trieRoot, rootHash)
	}

	return nil
}
