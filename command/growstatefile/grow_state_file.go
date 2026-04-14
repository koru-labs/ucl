package growstatefile

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/0xPolygon/polygon-edge/blockchain/storage/pebble"
	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
)

func GetCommand() *cobra.Command {
	growStateFileCmd := &cobra.Command{
		Use:   "grow-state-file",
		Short: "Grows trie state by iteratively writing mapping entries and optionally recreates genesis block",
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return params.validate()
		},
		Run: runCommand,
	}

	setFlags(growStateFileCmd)

	return growStateFileCmd
}

func runCommand(cmd *cobra.Command, _ []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	result, err := runGrowStateFile(outputter)
	if err != nil {
		outputter.SetError(err)

		return
	}

	outputter.SetCommandResult(result)
}

func runGrowStateFile(outputter command.OutputFormatter) (*growStateResult, error) {
	var (
		bigOne          = big.NewInt(1)
		logger          = hclog.NewNullLogger()
		contractBalance = big.NewInt(1_000_000)
		rootHash        []byte
		genesisHeader   *types.Header
		totalSize       int64
	)

	snapshotRootHash := types.StringToHash(params.rootHash)
	if snapshotRootHash == types.ZeroHash && params.blockchainDirPath != "" {
		lb, err := readLatestBlockHeader(params.blockchainDirPath, logger)
		if err != nil {
			return nil, err
		}

		snapshotRootHash = lb.StateRoot

		_, _ = outputter.Write(fmt.Appendf(nil, "Update from head block (%d, %s) state root: %s\n",
			lb.Number, lb.Hash, snapshotRootHash))
	} else {
		_, _ = outputter.Write(fmt.Appendf(nil, "Update from provided state root: %s\n", snapshotRootHash))
	}

	storage, err := itrie.NewPebbleDBStorage(params.trieDirPath, logger)
	if err != nil {
		return nil, err
	}

	defer storage.Close() //nolint:errcheck

	chainState := itrie.NewState(storage)

	snap, err := chainState.NewSnapshot(snapshotRootHash)
	if err != nil {
		return nil, err
	}

	contractCode, err := hex.DecodeHex(contractCodeHex)
	if err != nil {
		return nil, err
	}

	contractAddrs := make([]types.Address, params.contractsCounts)
	accounts := make([]*state.Account, params.contractsCounts)
	codeHash := types.BytesToHash(crypto.Keccak256(contractCode))

	if err := chainState.SetCode(codeHash, contractCode); err != nil {
		return nil, err
	}

	for i := range contractAddrs {
		addr := types.StringToAddress(fmt.Sprintf(contractAddrPrefix, i+1))
		contractAddrs[i] = addr

		existingAcc, getErr := snap.GetAccount(addr)
		if getErr != nil {
			return nil, getErr
		}

		if existingAcc != nil {
			accounts[i] = existingAcc

			_, _ = outputter.Write(fmt.Appendf(nil, "Contract already exists, skipping deployment: %s\n", addr))

			continue
		}

		initObj := &state.Object{
			Address:   addr,
			CodeHash:  codeHash,
			Balance:   contractBalance,
			Root:      types.EmptyRootHash,
			Nonce:     1,
			DirtyCode: true,
			Code:      contractCode,
			Storage:   []*state.StorageObject{},
		}

		snap, rootHash, err = snap.Commit([]*state.Object{initObj})
		if err != nil {
			return nil, err
		}

		accounts[i], err = snap.GetAccount(addr)
		if err != nil {
			return nil, err
		} else if accounts[i] == nil {
			return nil, fmt.Errorf("contract account is nil after deployment: %s", addr)
		}

		_, _ = outputter.Write(fmt.Appendf(nil, "Contract deployed: %s\n", addr))
	}

	contractCountEntries := make([]*big.Int, len(contractAddrs))
	objects := make([]*state.Object, params.contractsCounts)
	countVal := make([]byte, 32)
	preimage := make([]byte, 64)
	countEntriesSlot := make([]byte, 32)
	// using slot 1
	countEntriesSlot[31] = 1

	for ci, addr := range contractAddrs {
		account := accounts[ci]
		stored := snap.GetStorage(addr, account.Root, types.BytesToHash(countEntriesSlot))
		contractCountEntries[ci] = new(big.Int).SetBytes(stored.Bytes())

		_, _ = outputter.Write(fmt.Appendf(nil, "Contract %s start countEntries: %s\n",
			addr, contractCountEntries[ci]))
	}

	for j := range params.hashChangesPerContract {
		for ci, addr := range contractAddrs {
			account := accounts[ci]
			cntEntries := contractCountEntries[ci]

			rndVal := make([]byte, 32)
			_, _ = rand.Read(rndVal)

			cntEntries.Add(cntEntries, bigOne)
			cntEntries.FillBytes(countVal)
			copy(preimage, countVal)

			account.Nonce++

			objects[ci] = &state.Object{
				Address:  addr,
				CodeHash: codeHash,
				Balance:  account.Balance,
				Root:     account.Root,
				Nonce:    account.Nonce,
				Storage: []*state.StorageObject{
					{Key: crypto.Keccak256(preimage), Val: rndVal},
					{Key: countEntriesSlot, Val: countVal},
				},
			}
		}

		snap, rootHash, err = snap.Commit(objects)
		if err != nil {
			return nil, err
		}

		for ci, addr := range contractAddrs {
			accounts[ci], err = snap.GetAccount(addr)
			if err != nil {
				return nil, err
			} else if accounts[ci] == nil {
				return nil, fmt.Errorf("contract account is nil after commit: %s", addr)
			}
		}

		if (j+1)%outputIterationsModuo == 0 {
			_, _ = outputter.Write(fmt.Appendf(nil, "Iteration %d committed, new root: %s\n",
				j+1, types.Hash(rootHash)))
		}
	}

	err = filepath.Walk(params.trieDirPath, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if !info.IsDir() {
			totalSize += info.Size()
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, addr := range contractAddrs {
		finalAcct, getErr := snap.GetAccount(addr)
		if getErr != nil {
			return nil, getErr
		} else if finalAcct == nil {
			return nil, fmt.Errorf("final account is nil: %s", addr)
		}
	}

	if params.createGenesisBlock {
		types.HeaderHash = func(h *types.Header) types.Hash {
			km := &signer.BLSKeyManager{}
			sgn := signer.NewSigner(km, km)

			hash, calcErr := sgn.CalculateHeaderHash(h)
			if calcErr != nil {
				return types.ZeroHash
			}

			return hash
		}

		chainConfig, importErr := chain.ImportFromFile(params.genesisPath)
		if importErr != nil {
			return nil, fmt.Errorf("failed to load genesis.json: %w", importErr)
		}

		newRootHash, newRootErr := writeNewGenesis(chainConfig, chainState, types.BytesToHash(rootHash))
		if newRootErr != nil {
			return nil, newRootErr
		}

		genesisHeader, err = writeFakeBlock(params.blockchainDirPath, chainConfig, newRootHash, logger)
		if err != nil {
			return nil, err
		}
	}

	return &growStateResult{
		StateRootHash: types.BytesToHash(rootHash),
		TotalSize:     totalSize,
		GenesisHeader: genesisHeader,
	}, nil
}

func readLatestBlockHeader(blockchainPath string, logger hclog.Logger) (*types.Header, error) {
	db, err := pebble.NewPebbleDBStorage(blockchainPath, logger)
	if err != nil {
		return nil, err
	}

	defer db.Close() //nolint:errcheck

	blockHeadHash, hasHead := db.ReadHeadHash()
	if !hasHead {
		return nil, fmt.Errorf("failed to read head hash")
	}

	blockHeadNumber, hasHead := db.ReadHeadNumber()
	if !hasHead {
		return nil, fmt.Errorf("failed to read head number")
	}

	blockHeader, err := db.ReadHeader(blockHeadNumber, blockHeadHash)
	if err != nil {
		return nil, err
	}

	return blockHeader, nil
}

func writeFakeBlock(
	blockchainPath string,
	chainConfig *chain.Chain,
	stateRoot types.Hash,
	logger hclog.Logger,
) (*types.Header, error) {
	if err := os.RemoveAll(blockchainPath); err != nil {
		return nil, err
	}

	db, err := pebble.NewPebbleDBStorage(blockchainPath, logger)
	if err != nil {
		return nil, err
	}

	defer db.Close() //nolint:errcheck

	_, hasHead := db.ReadHeadHash()
	if hasHead {
		return nil, fmt.Errorf("storage should be empty after clearing")
	}

	chainConfig.Genesis.StateRoot = stateRoot
	genesisHeader := chainConfig.Genesis.GenesisHeader()
	genesisHeader.ComputeHash()

	newTD := new(big.Int).SetUint64(genesisHeader.Difficulty)

	batchWriter := db.NewWriter()
	batchWriter.PutCanonicalHeader(genesisHeader, newTD)
	batchWriter.PutBody(genesisHeader.Number, genesisHeader.Hash, &types.Body{
		Transactions: []*types.Transaction{},
		Uncles:       []*types.Header{},
	})
	batchWriter.PutReceipts(genesisHeader.Number, genesisHeader.Hash, []*types.Receipt{})

	if err := batchWriter.WriteBatch(); err != nil {
		return nil, err
	}

	return genesisHeader, nil
}

func writeNewGenesis(
	chainConfig *chain.Chain,
	chainState *itrie.State,
	initialStateRoot types.Hash,
) (types.Hash, error) {
	snap, err := chainState.NewSnapshot(initialStateRoot)
	if err != nil {
		return types.ZeroHash, err
	}

	txn := state.NewTxn(snap)

	for addr, account := range chainConfig.Genesis.Alloc {
		if account.Balance != nil {
			txn.AddBalance(addr, account.Balance)
		}

		if account.Nonce != 0 {
			txn.SetNonce(addr, account.Nonce)
		}

		if len(account.Code) != 0 {
			txn.SetCode(addr, account.Code)
		}

		for key, value := range account.Storage {
			txn.SetState(addr, key, value)
		}
	}

	objs, err := txn.Commit(false)
	if err != nil {
		return types.ZeroHash, err
	}

	_, root, err := snap.Commit(objs)
	if err != nil {
		return types.ZeroHash, err
	}

	return types.BytesToHash(root), nil
}
