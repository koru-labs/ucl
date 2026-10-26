package export

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/types/buildroot"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

const chainID = 100

var (
	gwei     = big.NewInt(1_000_000_000)
	coinbase = types.StringToAddress("0xc0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0")
	// store is a genesis contract: sstore(calldata[0], calldata[32]) and LOG1 with topic calldata[0].
	store = types.StringToAddress("0x5705e00000000000000000000000000000000000")
)

// storeRuntime: PUSH1 32 CALLDATALOAD PUSH1 0 CALLDATALOAD SSTORE
// PUSH1 0 CALLDATALOAD PUSH1 0 PUSH1 0 LOG1 STOP.
var storeRuntime = []byte{
	0x60, 0x20, 0x35, 0x60, 0x00, 0x35, 0x55,
	0x60, 0x00, 0x35, 0x60, 0x00, 0x60, 0x00, 0xa1, 0x00,
}

// storeInit returns storeRuntime: PUSH16 <runtime> PUSH1 0 MSTORE PUSH1 16 PUSH1 16 RETURN.
func storeInit() []byte {
	code := append([]byte{0x6f}, storeRuntime...)

	return append(code, 0x60, 0x00, 0x52, 0x60, 0x10, 0x60, 0x10, 0xf3)
}

func word(v uint64) []byte {
	return types.BytesToHash(new(big.Int).SetUint64(v).Bytes()).Bytes()
}

// testChain is an in-memory ucl chain: ucl's executor and trie state, blocks sealed by the test.
type testChain struct {
	t        *testing.T
	state    state.State
	executor *state.Executor
	signer   crypto.TxSigner
	keys     []*ecdsa.PrivateKey
	nonces   map[types.Address]uint64

	mu      sync.Mutex
	blocks  []*types.Block
	headers map[types.Hash]*types.Header
}

func newTestChain(t *testing.T) *testChain {
	t.Helper()

	// Pebble, as ucl nodes use: the export runs blocks concurrently, which ucl's in-memory storage
	// does not support (its batches share one map).
	storage, err := itrie.NewPebbleDBStorage(t.TempDir(), hclog.NewNullLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })

	st := itrie.NewState(storage)
	// No burn contract: as on ucl mainnet, the base fee goes to the block's proposer.
	params := &chain.Params{Forks: chain.AllForksEnabled, ChainID: chainID}
	executor := state.NewExecutor(params, st, hclog.NewNullLogger())

	c := &testChain{
		t:        t,
		state:    st,
		executor: executor,
		signer:   crypto.NewSigner(chain.AllForksEnabled.At(0), chainID),
		nonces:   map[types.Address]uint64{},
		headers:  map[types.Hash]*types.Header{},
	}

	executor.GetHash = func(*types.Header) state.GetHashByNumber {
		return func(i uint64) types.Hash {
			c.mu.Lock()
			defer c.mu.Unlock()

			if i < uint64(len(c.blocks)) {
				return c.blocks[i].Hash()
			}

			return types.ZeroHash
		}
	}

	alloc := map[types.Address]*chain.GenesisAccount{
		store: {Code: storeRuntime, Balance: big.NewInt(0)},
	}

	for i := 0; i < 3; i++ {
		// Fixed keys, so the records the vectors test writes are the same on every run.
		raw := make([]byte, 32)
		raw[0], raw[31] = 0x5e, byte(i+1)

		key, err := crypto.BytesToECDSAPrivateKey([]byte(hex.EncodeToString(raw)))
		require.NoError(t, err)

		c.keys = append(c.keys, key)
		alloc[crypto.PubKeyToAddress(&key.PublicKey)] = &chain.GenesisAccount{
			Balance: new(big.Int).Mul(big.NewInt(1_000_000), gwei),
		}
	}

	root, err := executor.WriteGenesis(alloc, types.EmptyRootHash)
	require.NoError(t, err)

	genesis := &types.Header{
		StateRoot:    root,
		GasLimit:     30_000_000,
		Sha3Uncles:   types.EmptyUncleHash,
		TxRoot:       types.EmptyRootHash,
		ReceiptsRoot: types.EmptyRootHash,
		BaseFee:      gwei.Uint64(),
	}
	genesis.ComputeHash()
	c.append(&types.Block{Header: genesis})

	return c
}

func (c *testChain) append(block *types.Block) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.blocks = append(c.blocks, block)
	c.headers[block.Hash()] = block.Header
}

func (c *testChain) address(i int) types.Address {
	return crypto.PubKeyToAddress(&c.keys[i].PublicKey)
}

// tx signs a transaction from key i; legacy when legacy is set, else dynamic-fee.
func (c *testChain) tx(i int, to *types.Address, value *big.Int, input []byte, legacy bool) *types.Transaction {
	from := c.address(i)
	tx := &types.Transaction{
		Nonce: c.nonces[from],
		Gas:   200_000,
		To:    to,
		Value: value,
		Input: input,
		From:  from,
	}

	if legacy {
		tx.Type = types.LegacyTx
		tx.GasPrice = new(big.Int).Mul(big.NewInt(2), gwei)
	} else {
		tx.Type = types.DynamicFeeTx
		tx.ChainID = big.NewInt(chainID)
		tx.GasFeeCap = new(big.Int).Mul(big.NewInt(2), gwei)
		tx.GasTipCap = gwei
	}

	signed, err := c.signer.SignTx(tx, c.keys[i])
	require.NoError(c.t, err)

	signed.From = from
	signed.ComputeHash(0)
	c.nonces[from]++

	return signed
}

// seal executes txs on the head as ucl's block producer does and appends the block.
func (c *testChain) seal(txs ...*types.Transaction) *types.Block {
	parent := c.Header()
	header := &types.Header{
		ParentHash: parent.Hash,
		Number:     parent.Number + 1,
		Miner:      coinbase.Bytes(),
		GasLimit:   parent.GasLimit,
		Timestamp:  parent.Timestamp + 1,
		Difficulty: parent.Number + 1,
		BaseFee:    gwei.Uint64(),
		Sha3Uncles: types.EmptyUncleHash,
		ExtraData:  make([]byte, 97),
	}
	block := &types.Block{Header: header, Transactions: txs}

	txn, err := c.executor.ProcessBlock(parent.StateRoot, block, coinbase)
	require.NoError(c.t, err)

	_, root, err := txn.Commit()
	require.NoError(c.t, err)

	header.StateRoot = root
	header.GasUsed = txn.TotalGas()
	header.TxRoot = buildroot.CalculateTransactionsRoot(txs, header.Number)
	header.ReceiptsRoot = buildroot.CalculateReceiptsRoot(txn.Receipts())
	header.LogsBloom = types.CreateBloom(txn.Receipts())
	header.ComputeHash()
	c.append(block)

	return block
}

func (c *testChain) Header() *types.Header {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.blocks[len(c.blocks)-1].Header
}

func (c *testChain) GetHeaderByHash(hash types.Hash) (*types.Header, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	h, ok := c.headers[hash]

	return h, ok
}

func (c *testChain) GetBlockByNumber(number uint64, _ bool) (*types.Block, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if number >= uint64(len(c.blocks)) {
		return nil, false
	}

	return c.blocks[number], true
}

// proposerConsensus reads the creator from the header, as the test seals blocks.
type proposerConsensus struct{}

func (proposerConsensus) GetBlockCreator(h *types.Header) (types.Address, error) {
	return types.BytesToAddress(h.Miner), nil
}

func (proposerConsensus) PreCommitState(*types.Block, *state.Transition) error { return nil }

// countingExecutor counts executions per block.
type countingExecutor struct {
	inner *state.Executor
	mu    sync.Mutex
	runs  map[uint64]int
	total atomic.Int64
}

func (e *countingExecutor) ProcessBlock(
	parentRoot types.Hash, block *types.Block, creator types.Address,
) (*state.Transition, error) {
	e.mu.Lock()
	e.runs[block.Number()]++
	e.mu.Unlock()
	e.total.Add(1)

	return e.inner.ProcessBlock(parentRoot, block, creator)
}

// mixedChain builds blocks with value transfers, legacy and dynamic-fee transactions, a contract
// creation, storage writes and deletes, logs, a failing transaction and an empty block.
func mixedChain(t *testing.T) *testChain {
	t.Helper()

	c := newTestChain(t)
	a1, a2 := c.address(1), c.address(2)
	fresh := types.StringToAddress("0xf7e5000000000000000000000000000000000001")
	input := func(slot, value uint64) []byte { return append(word(slot), word(value)...) }

	c.seal(
		c.tx(0, &a1, big.NewInt(1_000), nil, true),
		c.tx(1, &fresh, big.NewInt(7), nil, false),
	)
	c.seal(
		c.tx(0, &store, big.NewInt(0), input(1, 11), false),
		c.tx(1, &store, big.NewInt(0), input(2, 22), true),
		c.tx(2, nil, big.NewInt(0), storeInit(), false),
	)
	c.seal()
	c.seal(
		c.tx(0, &store, big.NewInt(0), input(1, 0), false),  // delete slot 1
		c.tx(2, &store, big.NewInt(0), input(2, 23), false), // overwrite slot 2
		c.tx(1, &a2, big.NewInt(5), []byte{0xfe}, false),    // plain transfer with input to an EOA
	)

	return c
}

// applied is a plain view of the state, built by applying records the way pitau does.
type applied struct {
	accounts map[types.Address]Account
	storage  map[types.Address]map[types.Hash]types.Hash
}

func (a *applied) apply(t *testing.T, r *Record) {
	t.Helper()

	for _, change := range r.Accounts {
		before, existed := a.accounts[change.Address]
		if change.Before == nil {
			require.False(t, existed, "block %d: %s existed before", r.Number, change.Address)
		} else {
			require.True(t, existed, "block %d: %s did not exist before", r.Number, change.Address)
			require.Equal(t, before.Nonce, change.Before.Nonce)
			require.Zero(t, before.Balance.Cmp(change.Before.Balance), "block %d: %s balance before", r.Number, change.Address)
			require.Equal(t, before.CodeHash, change.Before.CodeHash)
		}

		if change.After == nil {
			delete(a.accounts, change.Address)
			delete(a.storage, change.Address)

			continue
		}

		a.accounts[change.Address] = *change.After

		for _, slot := range change.Storage {
			slots := a.storage[change.Address]
			if slots == nil {
				slots = map[types.Hash]types.Hash{}
				a.storage[change.Address] = slots
			}

			require.Equal(t, slots[slot.Slot], slot.Before, "block %d: %s slot %s before", r.Number, change.Address, slot.Slot)
			slots[slot.Slot] = slot.After
		}
	}
}

func newExporter(c *testChain) *Exporter {
	return NewExporter(c, c.executor, proposerConsensus{})
}

// T3: every block re-executes to its stored roots, records survive encoding, and applying the
// records in order from genesis, checking every value before the block, ends in ucl's state.
func TestRecordsReproduceTheChain(t *testing.T) {
	c := mixedChain(t)
	exporter := newExporter(c)

	view := &applied{accounts: map[types.Address]Account{}, storage: map[types.Address]map[types.Hash]types.Hash{}}
	genesis, err := c.state.NewSnapshot(c.blocks[0].Header.StateRoot)
	require.NoError(t, err)

	for _, address := range []types.Address{store, c.address(0), c.address(1), c.address(2)} {
		account, err := genesis.GetAccount(address)
		require.NoError(t, err)
		view.accounts[address] = Account{
			Nonce: account.Nonce, Balance: account.Balance, CodeHash: types.BytesToHash(account.CodeHash),
		}
	}

	head := c.Header().Number
	sawCode, sawLogs, sawDelete := false, false, false

	for n := uint64(1); n <= head; n++ {
		record, err := exporter.Export(n)
		require.NoError(t, err, "block %d", n)

		// The rest of the test reads the decoded record, so decoding is checked by what follows.
		var decoded Record
		require.NoError(t, decoded.UnmarshalRLP(record.MarshalRLP()))
		require.Equal(t, record.MarshalRLP(), decoded.MarshalRLP(), "block %d round trip", n)
		record = &decoded

		frame := EncodeFrame(record.MarshalRLP())
		rlp, err := DecodeFrame(frame)
		require.NoError(t, err)
		require.Equal(t, record.MarshalRLP(), rlp)

		block := c.blocks[n]
		require.Equal(t, block.Hash(), record.Hash)
		require.Equal(t, block.Header.MarshalRLP(), record.Header)
		require.Len(t, record.Transactions, len(block.Transactions))

		for _, change := range record.Accounts {
			sawCode = sawCode || len(change.Code) > 0

			for _, slot := range change.Storage {
				sawDelete = sawDelete || slot.After == types.ZeroHash
			}
		}

		for _, receipt := range record.Receipts {
			sawLogs = sawLogs || len(receipt.Logs) > 0
		}

		view.apply(t, record)
	}

	require.True(t, sawCode && sawLogs && sawDelete, "the chain exercises code, logs and deletes")

	final, err := c.state.NewSnapshot(c.Header().StateRoot)
	require.NoError(t, err)

	for address, account := range view.accounts {
		got, err := final.GetAccount(address)
		require.NoError(t, err)
		require.NotNil(t, got, "%s exists in ucl's state", address)
		require.Equal(t, got.Nonce, account.Nonce)
		require.Zero(t, got.Balance.Cmp(account.Balance), "%s balance", address)
		require.Equal(t, types.BytesToHash(got.CodeHash), account.CodeHash)

		for slot, value := range view.storage[address] {
			require.Equal(t, final.GetStorage(address, got.Root, slot), value, "%s slot %s", address, slot)
		}
	}
}

// A block whose stored roots its re-execution does not reproduce is refused, and a range stops at
// it with the records before it, caching nothing for it.
func TestIrreproducibleBlockStopsTheRange(t *testing.T) {
	c := mixedChain(t)

	// Block 3 claims another state root.
	bad := c.blocks[3]
	bad.Header.StateRoot = types.StringToHash("0x01")
	bad.Header.ComputeHash()
	c.headers[bad.Hash()] = bad.Header

	_, err := newExporter(c).Export(3)
	require.ErrorIs(t, err, ErrIrreproducible)

	cache, err := NewCache(t.TempDir())
	require.NoError(t, err)

	service := NewService(newExporter(c), c, cache, DefaultServiceConfig, hclog.NewNullLogger())
	result, err := service.Range(1, 10)
	require.NoError(t, err)
	require.Len(t, result.Frames, 2)
	require.Equal(t, uint64(3), result.Next)
	require.NotNil(t, result.Err)
	require.Equal(t, uint64(3), result.Err.Number)
	require.True(t, result.Err.Irreproducible)

	_, cached := cache.Get(3, bad.Hash())
	require.False(t, cached)
}

// Concurrent and overlapping ranges execute each block once; later requests, and a new service on
// the same cache, execute nothing.
func TestEachBlockExecutesOnce(t *testing.T) {
	c := mixedChain(t)
	counting := &countingExecutor{inner: c.executor, runs: map[uint64]int{}}
	dir := t.TempDir()

	cache, err := NewCache(dir)
	require.NoError(t, err)

	service := NewService(NewExporter(c, counting, proposerConsensus{}), c, cache, DefaultServiceConfig, hclog.NewNullLogger())

	var wg sync.WaitGroup
	for _, r := range [][2]uint64{{1, 4}, {2, 4}, {1, 2}, {3, 4}, {1, 4}, {2, 3}} {
		wg.Add(1)

		go func(from, to uint64) {
			defer wg.Done()

			result, err := service.Range(from, to)
			require.NoError(t, err)
			require.Nil(t, result.Err)
			require.Len(t, result.Frames, int(to-from+1))
		}(r[0], r[1])
	}

	wg.Wait()

	for n := uint64(1); n <= 4; n++ {
		require.Equal(t, 1, counting.runs[n], "block %d executions", n)
	}

	result, err := service.Range(1, 4)
	require.NoError(t, err)
	require.Len(t, result.Frames, 4)
	require.Equal(t, int64(4), counting.total.Load(), "a repeated range is served from the cache")

	reopened, err := NewCache(dir)
	require.NoError(t, err)

	again := NewService(NewExporter(c, counting, proposerConsensus{}), c, reopened, DefaultServiceConfig, hclog.NewNullLogger())
	second, err := again.Range(1, 4)
	require.NoError(t, err)
	require.Equal(t, result.Frames, second.Frames)
	require.Equal(t, int64(4), counting.total.Load(), "a new service reads the same cache")
}

// A range stops at the head, at MaxBlocks and at MaxBytes, always returning its first record.
func TestRangeLimits(t *testing.T) {
	c := mixedChain(t)

	cache, err := NewCache(t.TempDir())
	require.NoError(t, err)

	config := ServiceConfig{Workers: 2, MaxBlocks: 2, MaxBytes: 1}
	service := NewService(newExporter(c), c, cache, config, hclog.NewNullLogger())

	result, err := service.Range(1, 100)
	require.NoError(t, err)
	require.Len(t, result.Frames, 1, "MaxBytes still returns one record")
	require.Equal(t, uint64(2), result.Next)

	config.MaxBytes = DefaultServiceConfig.MaxBytes
	service = NewService(newExporter(c), c, cache, config, hclog.NewNullLogger())
	result, err = service.Range(1, 100)
	require.NoError(t, err)
	require.Len(t, result.Frames, 2, "MaxBlocks")

	config.MaxBlocks = 100
	service = NewService(newExporter(c), c, cache, config, hclog.NewNullLogger())
	result, err = service.Range(3, 100)
	require.NoError(t, err)
	require.Equal(t, c.Header().Number+1, result.Next, "a range past the head ends at the head")
	require.Equal(t, c.Header().Number, result.Head)

	result, err = service.Range(c.Header().Number+1, 100)
	require.NoError(t, err)
	require.Empty(t, result.Frames)
	require.Equal(t, c.Header().Number+1, result.Next)

	_, err = service.Range(0, 3)
	require.Error(t, err)
}

// A damaged cache entry is dropped and the block executed again.
func TestDamagedCacheEntryIsRecomputed(t *testing.T) {
	c := mixedChain(t)
	counting := &countingExecutor{inner: c.executor, runs: map[uint64]int{}}
	dir := t.TempDir()

	cache, err := NewCache(dir)
	require.NoError(t, err)

	service := NewService(NewExporter(c, counting, proposerConsensus{}), c, cache, DefaultServiceConfig, hclog.NewNullLogger())
	first, err := service.Frame(2)
	require.NoError(t, err)

	path := filepath.Join(dir, "v1", "0", "2.rec")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	data[len(data)-1] ^= 0xff
	require.NoError(t, os.WriteFile(path, data, 0o600))

	second, err := service.Frame(2)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 2, counting.runs[2])
}

func TestFrameChecksum(t *testing.T) {
	frame := EncodeFrame([]byte{0xc0})
	frame[0] ^= 1

	_, err := DecodeFrame(frame)
	require.True(t, errors.Is(err, errChecksum))
}

// recordVector is a record as pitau's decoder must read it, beside its frame.
type recordVector struct {
	Frame        string          `json:"frame"`
	Number       uint64          `json:"number"`
	Hash         string          `json:"hash"`
	Header       string          `json:"header"`
	Transactions []string        `json:"transactions"`
	Receipts     []receiptVector `json:"receipts"`
	Accounts     []accountVector `json:"accounts"`
}

type receiptVector struct {
	Type              uint8       `json:"type"`
	Success           bool        `json:"success"`
	CumulativeGasUsed uint64      `json:"cumulative_gas_used"`
	Logs              []logVector `json:"logs"`
}

type logVector struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type accountVector struct {
	Address string         `json:"address"`
	Before  *accountValues `json:"before"`
	After   *accountValues `json:"after"`
	Code    string         `json:"code"`
	Storage [][3]string    `json:"storage"`
}

type accountValues struct {
	Nonce    uint64 `json:"nonce"`
	Balance  string `json:"balance"`
	CodeHash string `json:"code_hash"`
}

func values(a *Account) *accountValues {
	if a == nil {
		return nil
	}

	return &accountValues{Nonce: a.Nonce, Balance: "0x" + a.Balance.Text(16), CodeHash: a.CodeHash.String()}
}

// TestRecordVectors writes ../testdata/records.json, the records of mixedChain's blocks, with
// UPDATE_MIGRATION_VECTORS=1, and otherwise checks the file still holds what the exporter produces.
func TestRecordVectors(t *testing.T) {
	c := mixedChain(t)
	exporter := newExporter(c)

	var vectors []recordVector

	for n := uint64(1); n <= c.Header().Number; n++ {
		r, err := exporter.Export(n)
		require.NoError(t, err)

		v := recordVector{
			Frame:  hex.EncodeToString(EncodeFrame(r.MarshalRLP())),
			Number: r.Number,
			Hash:   r.Hash.String(),
			Header: hex.EncodeToString(r.Header),
		}

		for _, tx := range r.Transactions {
			v.Transactions = append(v.Transactions, hex.EncodeToString(tx))
		}

		for _, receipt := range r.Receipts {
			rv := receiptVector{Type: receipt.Type, Success: receipt.Success, CumulativeGasUsed: receipt.CumulativeGasUsed}
			for _, log := range receipt.Logs {
				lv := logVector{Address: log.Address.String(), Data: hex.EncodeToString(log.Data)}
				for _, topic := range log.Topics {
					lv.Topics = append(lv.Topics, topic.String())
				}

				rv.Logs = append(rv.Logs, lv)
			}

			v.Receipts = append(v.Receipts, rv)
		}

		for _, a := range r.Accounts {
			av := accountVector{Address: a.Address.String(), Before: values(a.Before), After: values(a.After), Code: hex.EncodeToString(a.Code)}
			for _, slot := range a.Storage {
				av.Storage = append(av.Storage, [3]string{slot.Slot.String(), slot.Before.String(), slot.After.String()})
			}

			v.Accounts = append(v.Accounts, av)
		}

		vectors = append(vectors, v)
	}

	path := filepath.Join("..", "testdata", "records.json")
	if os.Getenv("UPDATE_MIGRATION_VECTORS") == "1" {
		data, err := json.MarshalIndent(vectors, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))

		return
	}

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var stored []recordVector
	require.NoError(t, json.Unmarshal(data, &stored))
	require.Equal(t, vectors, stored)
}
