package rocksdb

import (
	"github.com/linxGnu/grocksdb"
)

type batchRocksDB struct {
	db *grocksdb.DB
	wo *grocksdb.WriteOptions
	b  *grocksdb.WriteBatch
}

func newBatchRocksDB(db *grocksdb.DB, wo *grocksdb.WriteOptions) *batchRocksDB {
	return &batchRocksDB{
		db: db,
		wo: wo,
		b:  grocksdb.NewWriteBatch(),
	}
}

func (b *batchRocksDB) Put(t uint8, k []byte, v []byte) {
	mc := tableMapper[t]

	k = append(append(make([]byte, 0, len(k)+len(mc)), k...), mc...)

	b.b.Put(k, v)
}

func (b *batchRocksDB) Write() error {
	err := b.db.Write(b.wo, b.b)

	b.b.Destroy()

	return err
}
