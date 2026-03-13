package memory

import (
	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/0xPolygon/polygon-edge/helper/hex"
)

type memoryKV struct {
	kv map[string][]byte
}
type memoryDB struct {
	db []memoryKV
}

// NewMemoryStorage creates the new storage reference with inmemory
func NewMemoryStorage() (*storage.Storage, error) {
	var ldbs [2]storage.Database

	kvs := []memoryKV{}

	for i := 0; uint8(i) < storage.MAX_TABLES; i++ {
		kvs = append(kvs, memoryKV{kv: map[string][]byte{}})
	}

	db := &memoryDB{db: kvs}

	ldbs[0] = db
	ldbs[1] = nil

	return storage.Open(nil, ldbs)
}

func (m *memoryDB) Get(t uint8, k []byte) ([]byte, bool, error) {
	v, ok := m.db[t].kv[hex.EncodeToHex(k)]
	if !ok {
		return nil, false, nil
	}

	return v, true, nil
}

func (m *memoryDB) Close() error {
	return nil
}

func (m *memoryDB) NewBatch() storage.Batch {
	return newBatchMemory(m.db)
}
