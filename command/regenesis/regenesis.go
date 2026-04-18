package regenesis

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/0xPolygon/polygon-edge/command"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/cockroachdb/pebble"
	"github.com/spf13/cobra"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

const besuWorldRootHashKey = "0x776f726c64526f6f74"

/*
./polygon-edge regenesis --target-path ./trie_new \
--stateRoot 0xf5ef1a28c82226effb90f4465180ec3469226747818579673f4be929f1cd8663  \
--source-path ./test-chain-1/trie
*/
func RegenesisCMD() *cobra.Command {
	genesisCmd := &cobra.Command{
		Use:   "regenesis",
		Short: "Copies trie for specific block to a separate folder",
	}

	genesisCmd.Flags().StringVar(
		&params.DstDBPath,
		"target-path",
		"",
		"the directory of trie data of trie copy",
	)
	genesisCmd.Flags().StringVar(
		&params.SrcDBPath,
		"source-path",
		"",
		"the directory of trie data of old chain",
	)
	genesisCmd.Flags().StringVar(
		&params.TrieRoot,
		"stateRoot",
		"",
		"block state root of old chain",
	)
	genesisCmd.Flags().StringVar(
		&params.SrcDBType,
		"source-db-type",
		"",
		"source database type",
	)
	genesisCmd.Flags().StringVar(
		&params.DstDBType,
		"target-db-type",
		"",
		"destination database type",
	)

	genesisCmd.PreRun = func(cmd *cobra.Command, args []string) {
		outputter := command.InitializeOutputter(cmd)
		defer outputter.WriteOutput()

		if params.DstDBPath == "" || params.SrcDBPath == "" ||
			(params.TrieRoot == "" && strings.ToLower(params.SrcDBType) != "rocksdb") {
			outputter.SetError(fmt.Errorf("not enough arguments"))

			return
		}
	}

	genesisCmd.Run = func(cmd *cobra.Command, args []string) {
		outputter := command.InitializeOutputter(cmd)
		defer outputter.WriteOutput()

		srcSTorage, err := getDBInstance(params.SrcDBType, params.SrcDBPath, true)
		if err != nil {
			outputter.SetError(fmt.Errorf("open source trie trieDB error:%w", err))

			return
		}

		defer srcSTorage.Close() //nolint:errcheck

		dstStorage, err := getDBInstance(params.DstDBType, params.DstDBPath, false)
		if err != nil {
			outputter.SetError(fmt.Errorf("open destination trie trieDB error:%w", err))

			return
		}

		defer dstStorage.Close() //nolint:errcheck

		var trieRoot types.Hash

		if strings.ToLower(params.SrcDBType) == "rocksdb" && params.TrieRoot == "" {
			rootHashBytes, exists, err := srcSTorage.Get(types.StringToBytes(besuWorldRootHashKey))
			if err != nil {
				outputter.SetError(fmt.Errorf("get besu world root hash error:%w", err))

				return
			} else if !exists {
				outputter.SetError(fmt.Errorf("besu world root hash not found"))

				return
			}

			trieRoot = types.BytesToHash(rootHashBytes)

			outputter.Write(fmt.Appendf(nil, "Besu trie root: %s\n", trieRoot))
		} else {
			trieRoot = types.StringToHash(params.TrieRoot)
		}

		err = itrie.CopyTrie(trieRoot.Bytes(), srcSTorage, dstStorage, nil, false)
		if err != nil {
			outputter.SetError(fmt.Errorf("copy trie error: %w", err))

			return
		}

		checkedHash, err := itrie.HashChecker(trieRoot.Bytes(), dstStorage)
		if err != nil {
			outputter.SetError(fmt.Errorf("copy trie error: %w", err))

			return
		}

		if checkedHash != trieRoot {
			outputter.SetError(fmt.Errorf("incorrect trie root error: expected %s, got %s", trieRoot, checkedHash))

			return
		}

		outputter.WriteCommandResult(&ReGenesisResult{})
	}

	return genesisCmd
}

type ReGenesisResult struct {
	Message string `json:"message"`
}

func (r *ReGenesisResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString("\n[Trie copy SUCCESS]\n")
	buffer.WriteString(r.Message)

	return buffer.String()
}

func getDBInstance(
	dbType string, path string, isReadOnly bool,
) (itrie.Storage, error) {
	switch strings.ToLower(dbType) {
	case "pebble", "":
		return itrie.NewPebbleDBStorageWithOpts(
			path, &pebble.Options{Logger: itrie.PebbleLogger{}, ReadOnly: isReadOnly})
	case "leveldb":
		return itrie.NewLevelDBStorageWithOpts(path, &opt.Options{ReadOnly: isReadOnly})
	case "rocksdb":
		return itrie.NewRocksDBStorage(path, isReadOnly)
	case "memory":
		return itrie.NewMemoryStorage(), nil
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}
}
