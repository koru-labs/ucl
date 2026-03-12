package tests

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"math/rand"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	_ "net/http/pprof"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

// Constants used for the stress test
const (
	// Number of transactions to include in each block
	numberOfTransactionsPerBlock = 10000

	// Number of blocks to process during the test
	blocksToProcess = 100

	// Time to sleep between processing blocks (set to 0 for no delay)
	sleepBetweenBlocks = 100 * time.Millisecond

	// Number of concurrent readers to simulate
	numReaders = 4

	// Source account address
	mainAccountAddress = "0xa94f5374fce5edbc8e2a8697c15331677e6ebf0b"

	// Path to the database directory
	dbPathPebbleDb = "/tmp/blockchain-disk-usage-test/pebbledb"
)

func BenchmarkTriePebbleDb(b *testing.B) {
	b.StopTimer()

	startProfiler()

	executeTrieDbTest(b, 7)
}

func executeTrieDbTest(b *testing.B, writeTime float64) {
	forks, baseFee, currentForks, coinbase, txBytes := getConstants()

	s, snapshot, pastRoot, err := buildStateWithHelperAccounts(numberOfTransactionsPerBlock)
	if err != nil {
		b.Logf("Error building state with helper accounts: %v", err)
		return
	}

	txs, err := generateSetOfTransactions(baseFee, numberOfTransactionsPerBlock)
	if err != nil {
		b.Logf("Error generating transactions: %v", err)
		return
	}

	err = setupSigner(txBytes, currentForks)
	if err != nil {
		b.Logf("Error setting up signer: %v", err)
		return
	}

	executor := setupExecutor(forks, s)

	currentRoot := pastRoot

	ch := make(chan types.Hash)

	// Start the readers if numReaders is greater than 0
	for i := 1; i <= numReaders; i++ {
		go reader(ch, *executor, txs[0].From, *getHeader(b), coinbase)
	}

	// Process blocks in a loop
	// This loop will run for the specified number of blocks
	// and will process each block sequentially.
	// The sleepBetweenBlocks variable controls the delay between processing each block.
	for index := range blocksToProcess {

		start := time.Now()

		err, currentRoot = processBlock(b, currentRoot, s, index, executor, coinbase, txs, currentForks, snapshot, ch)
		if err != nil {
			b.Logf("Error processing block %d: %v", index, err)
			return
		}

		b.Logf("Iteration %d done in %ss", index, time.Since(start))

		time.Sleep(sleepBetweenBlocks)
	}

	b.Logf("\ttotal write time %f s", b.Elapsed().Seconds())
	require.LessOrEqual(b, b.Elapsed().Seconds(), writeTime)
}

func processBlock(b *testing.B, currentRoot types.Hash, s state.State, index int, executor *state.Executor, coinbase types.Address, txs []*types.Transaction, currentForks chain.ForksInTime, snapshot state.Snapshot, ch chan types.Hash) (error, types.Hash) {
	// Add funds to the source account
	_, _, currentRoot, _ = refill(s, currentRoot, uint64(index*numberOfTransactionsPerBlock+1))

	transition, err := executor.BeginTxn(currentRoot, getHeader(b), coinbase)
	if err != nil {
		fmt.Printf("Error in executor.BeginTxn: %v\n", err)
		return err, types.ZeroHash
	}

	objs, shouldReturn, _ := applyTransactions(txs, err, transition, numberOfTransactionsPerBlock, coinbase, currentForks, 0, 0)
	if shouldReturn {
		return err, types.ZeroHash
	}

	if objs == nil {
		return fmt.Errorf("objects to commit are nil"), types.ZeroHash
	}

	b.StartTimer()
	_, newRoot, err := snapshot.Commit(objs)
	b.StopTimer()
	if err != nil {
		return err, types.ZeroHash
	}

	currentRoot = types.Hash(newRoot)

	for i := 1; i <= numReaders; i++ {
		ch <- currentRoot
	}
	return nil, currentRoot
}

// applyTransactions processes a list of transactions, applies them
// to the given state transition, and optionally populates storage
// objects with random values.
func applyTransactions(transactions []*types.Transaction,
	err error, transition *state.Transition,
	numberOfTransactions int,
	Coinbase types.Address,
	currentForks chain.ForksInTime,
	numberOfObjectsToAddStorage int,
	storageLen int) ([]*state.Object, bool, error) {
	for _, m := range transactions {

		_, err = transition.Apply(m)
		if err != nil {
			return nil, true, err
		}
		m.Nonce = m.Nonce + uint64(numberOfTransactions)
		m.Value = big.NewInt(1)
	}

	txn := transition.Txn()

	// mining rewards
	txn.AddSealingReward(Coinbase, big.NewInt(0))

	objs, err := txn.Commit(currentForks.EIP155)
	if err != nil {
		return nil, true, err
	}

	// Populate storage with random values
	maxRandomValue := new(big.Int).Exp(big.NewInt(2), big.NewInt(256), nil)
	for i := 0; i < numberOfObjectsToAddStorage && i < len(objs); i++ {
		o := objs[i]
		o.Storage = make([]*state.StorageObject, storageLen)
		for i := range o.Storage {
			key := new(big.Int).Rand(rand.New(rand.NewSource(time.Now().UnixNano())), maxRandomValue)
			value := new(big.Int).Rand(rand.New(rand.NewSource(time.Now().UnixNano())), maxRandomValue)
			obj := &state.StorageObject{}
			obj.Key = key.Bytes()
			obj.Val = value.Bytes()
			o.Storage[i] = obj
		}
	}

	return objs, false, nil
}

func reader(ch_root_address chan types.Hash, executor state.Executor, starting_address types.Address, header types.Header, coinbase types.Address) {
	for val := range ch_root_address { // Receive values continuously
		//fmt.Printf("Receiver: received %s\n", val.String())

		transition, _ := executor.BeginTxn(val, &header, coinbase)

		//for j := range numberOfTransactionsPerBlock {
		for j := range numberOfTransactionsPerBlock {
			_ = j
			ad := addressToBigInt(starting_address)
			ad = ad.Add(ad, big.NewInt(int64(j)))
			incrementedAddress := bigIntToAddress(ad)
			_, exists := transition.Txn().GetAccount(incrementedAddress)
			if exists != true {
				fmt.Printf("Unexisting account %s, %d\n", incrementedAddress.String(), j)
			}
		}
	}
}

func refill(s state.State, currentRoot types.Hash, nonce uint64) (state.State, state.Snapshot, types.Hash, error) {

	snap, err := s.NewSnapshotAt(currentRoot)
	if err != nil {
		return nil, nil, types.ZeroHash, err
	}

	txn := state.NewTxn(snap)

	addr := types.StringToAddress(mainAccountAddress)
	balance := big.NewInt(0x256000000)
	code := []uint8{}

	//txn.CreateAccount(addr)
	txn.SetNonce(addr, nonce)
	txn.SetBalance(addr, balance)
	txn.SetCode(addr, code)

	objs, err := txn.Commit(false)
	if err != nil {
		return nil, nil, types.ZeroHash, err
	}

	snap, root, err := snap.Commit(objs)

	return s, snap, types.BytesToHash(root), err
}

func generateSetOfTransactions(baseFee *big.Int, count int) ([]*types.Transaction, error) {
	txs := []*types.Transaction{}
	from := types.StringToAddress(mainAccountAddress)
	to := types.StringToAddress("0xec0e71ad0a90ffe1909d27dac207f7680abba42d")
	gasLimit := uint64(10000000)
	value := big.NewInt(0x01)

	for j := 0; j < count; j++ {
		// if tx is not dynamic and accessList is not nil, create an access list transaction
		ad := addressToBigInt(to)
		ad = ad.Add(ad, big.NewInt(int64(j)))
		incrementedAddress := bigIntToAddress(ad)

		tx := &types.Transaction{
			Type:      types.DynamicFeeTx,
			GasFeeCap: big.NewInt(0x01),
			GasTipCap: big.NewInt(0x01),
			From:      from,
			To:        &incrementedAddress,
			Nonce:     uint64(1) + uint64(j),
			Value:     value,
			Gas:       gasLimit,
		}

		txs = append(txs, tx)
	}

	return txs, nil
}

func setupExecutor(forks *chain.Forks, s state.State) *state.Executor {
	executor := state.NewExecutor(&chain.Params{
		Forks:   forks,
		ChainID: 1,
		BurnContract: map[uint64]types.Address{
			0: types.ZeroAddress,
		},
	}, s, hclog.NewNullLogger())

	executor.GetHash = func(*types.Header) func(i uint64) types.Hash {
		return vmTestBlockHash
	}
	return executor
}

func setupSigner(txBytes []byte, currentForks chain.ForksInTime) error {
	if len(txBytes) != 0 {
		ttx := &types.Transaction{}
		err := ttx.UnmarshalRLP(txBytes)
		if err != nil {
			return err
		}

		signer := crypto.NewSigner(currentForks, 1)

		if _, err := signer.Sender(ttx); err != nil {
			return err
		}
	}
	return nil
}

func buildStateWithHelperAccounts(numberOfTransactionsPerBlock uint64) (state.State, state.Snapshot, types.Hash, error) {

	storage_object, err := initializeStorageObject()
	if err != nil {
		return nil, nil, types.ZeroHash, err
	}

	s := itrie.NewState(storage_object)

	snap := s.NewSnapshot()

	txn := state.NewTxn(snap)

	addr := types.StringToAddress(mainAccountAddress)
	nonce := uint64(0x01)
	balance := big.NewInt(0x256000000)
	code := []uint8{}

	txn.CreateAccount(addr)
	txn.SetNonce(addr, nonce)
	txn.SetBalance(addr, balance)
	txn.SetCode(addr, code)

	for i := range numberOfTransactionsPerBlock {
		ad := addressToBigInt(addr)
		ad = ad.Add(ad, big.NewInt(int64(i)))
		incrementedAddress := bigIntToAddress(ad)

		txn.CreateAccount(incrementedAddress)
		txn.SetNonce(incrementedAddress, nonce)
		txn.SetBalance(incrementedAddress, balance)
		txn.SetCode(incrementedAddress, code)
	}

	objs, err := txn.Commit(false)
	if err != nil {
		return nil, nil, types.ZeroHash, err
	}

	snap, root, err := snap.Commit(objs)

	return s, snap, types.BytesToHash(root), err
}

func initializeStorageObject() (itrie.Storage, error) {

	var storage_object itrie.Storage
	err := common.CreateDirSafe(dbPathPebbleDb, 0755)
	if err != nil {
		return nil, err
	}
	storage_object, err = itrie.NewPebbleDBStorage(filepath.Join(dbPathPebbleDb, "trie"), hclog.NewNullLogger())
	if err != nil {
		fmt.Printf("Error creating storage object: %v\n", err)
		return nil, err
	}

	return storage_object, nil
}

// startProfiler starts a goroutine to enable pprof profiling on port 6060.
func startProfiler() {
	go func() {
		fmt.Println("Starting pprof on :6060")
		err := http.ListenAndServe("0.0.0.0:6060", nil)
		if err != nil {
			fmt.Println("ERROR")
		}
	}()
}

func getHeader(b *testing.B) *types.Header {
	b.Helper()

	baseFee := uint64(0x01)

	return &types.Header{
		Miner:      stringToAddressT(b, "0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba").Bytes(),
		BaseFee:    baseFee,
		Difficulty: stringToUint64T(b, "0x020000"),
		GasLimit:   stringToUint64T(b, "0x05f5e100000"),
		Number:     stringToUint64T(b, "0x01"),
		Timestamp:  stringToUint64T(b, "0x03e8"),
	}
}

func getConstants() (*chain.Forks, *big.Int, chain.ForksInTime, types.Address, []byte) {
	forks, _ := Forks["Istanbul"]
	baseFee := big.NewInt(0x01)

	currentForks := chain.ForksInTime{
		Homestead:      true,
		Byzantium:      true,
		Constantinople: true,
		Petersburg:     true,
		Istanbul:       true,
		London:         true,
		EIP150:         true,
		EIP158:         true,
		EIP155:         true,
		EIP3607:        true,
	}
	coinbase := types.StringToAddress("0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba")
	txBytesStr := "f860010a8398968094ec0e71ad0a90ffe1909d27dac207f7680abba42d64801ba0407569d12673df33920126d01c291b81463e44470098ae127649d0f907c6a580a03896a4dbaf7c15b1fc74c51388be7939480192c68bd9dd4569507f29f8ee94c7"

	// Decode the hex string
	txBytes, _ := hex.DecodeString(txBytesStr)
	return forks, baseFee, currentForks, coinbase, txBytes
}

func addressToBigInt(addr types.Address) *big.Int {
	return new(big.Int).SetBytes(addr[:])
}

func bigIntToAddress(num *big.Int) types.Address {
	var addr types.Address
	bytes := num.Bytes() // Convert big.Int to a byte slice

	// Ensure that we copy only the last `AddressLength` bytes if it's larger,
	// or zero-pad the left side if it's smaller.
	copy(addr[20-len(bytes):], bytes)

	return addr
}
