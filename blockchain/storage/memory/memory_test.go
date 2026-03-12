package memory

import (
	"testing"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
)

func TestStorage(t *testing.T) {
	t.Helper()

	f := func(t *testing.T) (*storage.Storage, func(), string) {
		t.Helper()

		s, err := NewMemoryStorage()
		if err != nil {
			t.Logf("\t Error opening MemoryDB -> %s", err.Error())

			return nil, func() {}, ""
		}

		return s, func() {}, ""
	}
	storage.TestStorage(t, f)
}
