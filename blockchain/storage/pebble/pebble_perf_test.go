package pebble

import (
	"os"
	"testing"

	"github.com/0xPolygon/polygon-edge/blockchain/storage"
	storagev2 "github.com/0xPolygon/polygon-edge/blockchain/storage"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func openStorage(b *testing.B, p string) (*storage.Storage, func(), string) {
	b.Helper()

	s, err := NewPebbleDBStorage(p, hclog.NewNullLogger())
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

	s, cleanUpFn, path := openStorage(b, "/tmp/pebbledbV2-test-perf")

	defer func() {
		s.Close()
		cleanUpFn()
	}()

	blockCount := 1000
	storagev2.BenchmarkStorage(b, blockCount, s, 42, 25) // CI times

	size, err := dbSize(path)
	require.NoError(b, err)
	b.Logf("\tsst file count: %d", countSstFilesInPath(path))
	b.Logf("\tdb size %d MB", size/(1024*1024))
}
