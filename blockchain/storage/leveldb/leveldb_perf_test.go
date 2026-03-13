package leveldb

import (
	"os"
	"testing"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

func openStorage(b *testing.B, p string) (*storage.Storage, func(), string) {
	b.Helper()

	s, err := NewLevelDBStorage(p, hclog.NewNullLogger())
	require.NoError(b, err)

	closeFn := func() {
		require.NoError(b, s.Close())

		if err := s.Close(); err != nil {
			b.Fatal(err)
		}

		require.NoError(b, os.RemoveAll(p))
	}

	return s, closeFn, p
}

func Benchmark(b *testing.B) {
	b.StopTimer()

	s, cleanUpFn, path := openStorage(b, "/tmp/leveldb-test-perf")

	defer func() {
		s.Close()
		cleanUpFn()
	}()

	blockCount := 1000
	storage.BenchmarkStorage(b, blockCount, s, 27, 19) // CI times

	size, err := dbSize(path)
	require.NoError(b, err)
	b.Logf("\tldb file count: %d", countLdbFilesInPath(path))
	b.Logf("\tdb size %d MB", size/(1*opt.MiB))
}
