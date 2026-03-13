package pebble

import (
	"github.com/cockroachdb/pebble"
)

type batchPebbleDB struct {
	db *pebble.DB // Underlying pebble storage engine
	b  *pebble.Batch
}

func newBatchPebbleDB(db *pebble.DB) *batchPebbleDB {
	return &batchPebbleDB{
		db: db,
		b:  db.NewBatch(),
	}
}

func (b *batchPebbleDB) Put(t uint8, k []byte, v []byte) {
	mc := tableMapper[t]
	k = append(append(make([]byte, 0, len(k)+len(mc)), k...), mc...)
	_ = b.b.Set(k, v, nil)
}

func (b *batchPebbleDB) Write() error {
	return b.b.Commit(nil)
}
