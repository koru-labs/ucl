package state_test

import (
	"errors"
	"math/big"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
)

var (
	depExecAddrA = types.StringToAddress("a")
	depExecAddrB = types.StringToAddress("b")
	depExecAddrC = types.StringToAddress("c")
	depExecAddrR = types.StringToAddress("d")

	// distinct recipients, used when transactions must not conflict with one another
	depExecAddrR1 = types.StringToAddress("d1")
	depExecAddrR2 = types.StringToAddress("d2")
	depExecAddrR3 = types.StringToAddress("d3")
)

// newDepExecExecutor builds an Executor backed by an in-memory trie, with the given
// pre-funded accounts written into genesis. It returns the executor and the genesis root.
func newDepExecExecutor(t *testing.T, alloc map[types.Address]*chain.GenesisAccount) (*state.Executor, types.Hash) {
	t.Helper()

	mstate := itrie.NewState(itrie.NewMemoryStorage())
	executor := state.NewExecutor(&chain.Params{
		ChainID:      100,
		Forks:        &chain.Forks{},
		BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
	}, mstate, hclog.NewNullLogger())

	executor.GetHash = func(*types.Header) func(uint64) types.Hash {
		return func(uint64) types.Hash { return types.Hash{} }
	}

	root, err := executor.WriteGenesis(alloc, types.ZeroHash)
	require.NoError(t, err)

	return executor, root
}

func depExecTransferTx(hashByte byte, from, to types.Address, nonce uint64, value, gasPrice int64, gas uint64) *types.Transaction {
	return &types.Transaction{
		Hash:     types.Hash{hashByte},
		From:     from,
		To:       &to,
		Value:    big.NewInt(value),
		Gas:      gas,
		GasPrice: big.NewInt(gasPrice),
		Nonce:    nonce,
		Type:     types.LegacyTx,
	}
}

func TestTxDependancyExecutor_Execute_IndependentTransfers(t *testing.T) {
	t.Parallel()

	alloc := map[types.Address]*chain.GenesisAccount{
		depExecAddrA: {Balance: big.NewInt(1_000_000)},
		depExecAddrB: {Balance: big.NewInt(1_000_000)},
		depExecAddrC: {Balance: big.NewInt(1_000_000)},
	}
	executor, root := newDepExecExecutor(t, alloc)

	// non-conflicting transfers: each sender pays a distinct recipient, so the dependency
	// builder would legitimately mark these as independent (no shared write set)
	txs := []*types.Transaction{
		depExecTransferTx(1, depExecAddrA, depExecAddrR1, 0, 100, 1, 21000),
		depExecTransferTx(2, depExecAddrB, depExecAddrR2, 0, 200, 1, 21000),
		depExecTransferTx(3, depExecAddrC, depExecAddrR3, 0, 300, 1, 21000),
	}

	pool := state.NewTxDependancyPool(txs, [][]uint64{{}, {}, {}})
	exec := state.NewTxDependancyExecutor(3, hclog.NewNullLogger())

	header := &types.Header{Number: 1, GasLimit: 1_000_000, Timestamp: 1}

	tran, receipts, err := exec.Execute(pool, executor, root, header, types.ZeroAddress)

	require.NoError(t, err)
	require.Len(t, receipts, 3)

	for i, receipt := range receipts {
		require.NotNil(t, receipt, "receipt %d must not be nil", i)
		require.Equal(t, txs[i].Hash, receipt.TxHash)
		require.Equal(t, types.ReceiptSuccess, *receipt.Status)
		require.Equal(t, uint64(21000), receipt.GasUsed)
	}

	// each recipient must have received exactly its own transfer
	require.Equal(t, big.NewInt(100), tran.GetBalance(depExecAddrR1))
	require.Equal(t, big.NewInt(200), tran.GetBalance(depExecAddrR2))
	require.Equal(t, big.NewInt(300), tran.GetBalance(depExecAddrR3))

	// every sender's nonce must have been incremented exactly once
	require.Equal(t, uint64(1), tran.GetNonce(depExecAddrA))
	require.Equal(t, uint64(1), tran.GetNonce(depExecAddrB))
	require.Equal(t, uint64(1), tran.GetNonce(depExecAddrC))
}

func TestTxDependancyExecutor_Execute_RespectsDependencyOrder(t *testing.T) {
	t.Parallel()

	// addrB starts with zero balance: tx1 (B -> C) needs both a value credit and enough
	// balance to cover its own gas cost, both of which only exist once tx0 (A -> B) has
	// run. The dependency graph must force tx0 to execute before tx1.
	alloc := map[types.Address]*chain.GenesisAccount{
		depExecAddrA: {Balance: big.NewInt(2_000_000)},
		depExecAddrB: {Balance: big.NewInt(0)},
	}
	executor, root := newDepExecExecutor(t, alloc)

	txs := []*types.Transaction{
		depExecTransferTx(1, depExecAddrA, depExecAddrB, 0, 1_000_000, 1, 21000),
		depExecTransferTx(2, depExecAddrB, depExecAddrC, 0, 500_000, 1, 21000),
	}

	pool := state.NewTxDependancyPool(txs, [][]uint64{{}, {0}})
	exec := state.NewTxDependancyExecutor(4, hclog.NewNullLogger())

	header := &types.Header{Number: 1, GasLimit: 1_000_000, Timestamp: 1}

	tran, receipts, err := exec.Execute(pool, executor, root, header, types.ZeroAddress)

	require.NoError(t, err)
	require.Len(t, receipts, 2)
	require.Equal(t, types.ReceiptSuccess, *receipts[0].Status)
	require.Equal(t, types.ReceiptSuccess, *receipts[1].Status)

	require.Equal(t, big.NewInt(500_000), tran.GetBalance(depExecAddrC))
	// B: +1,000,000 (tx0) - 500,000 (value sent) - 21,000 (gas spent on tx1)
	require.Equal(t, big.NewInt(479_000), tran.GetBalance(depExecAddrB))
}

func TestTxDependancyExecutor_Execute_GasAboveBlockLimitReturnsError(t *testing.T) {
	t.Parallel()

	alloc := map[types.Address]*chain.GenesisAccount{
		depExecAddrA: {Balance: big.NewInt(1_000_000)},
	}
	executor, root := newDepExecExecutor(t, alloc)

	txs := []*types.Transaction{
		depExecTransferTx(1, depExecAddrA, depExecAddrR, 0, 100, 1, 50_000),
	}

	pool := state.NewTxDependancyPool(txs, [][]uint64{{}})
	exec := state.NewTxDependancyExecutor(1, hclog.NewNullLogger())

	header := &types.Header{Number: 1, GasLimit: 21_000, Timestamp: 1}

	tran, receipts, err := exec.Execute(pool, executor, root, header, types.ZeroAddress)

	require.Error(t, err)
	require.True(t, errors.Is(err, runtime.ErrOutOfGas))
	require.Nil(t, tran)
	require.Nil(t, receipts)
}

func TestTxDependancyExecutor_Execute_InvalidTxReturnsError(t *testing.T) {
	t.Parallel()

	alloc := map[types.Address]*chain.GenesisAccount{
		depExecAddrA: {Balance: big.NewInt(1_000_000)},
	}
	executor, root := newDepExecExecutor(t, alloc)

	// nonce 5 does not match the account's actual nonce of 0
	txs := []*types.Transaction{
		depExecTransferTx(1, depExecAddrA, depExecAddrR, 5, 100, 1, 21000),
	}

	pool := state.NewTxDependancyPool(txs, [][]uint64{{}})
	exec := state.NewTxDependancyExecutor(1, hclog.NewNullLogger())

	header := &types.Header{Number: 1, GasLimit: 1_000_000, Timestamp: 1}

	tran, receipts, err := exec.Execute(pool, executor, root, header, types.ZeroAddress)

	require.Error(t, err)
	require.ErrorContains(t, err, "nonce too high")
	require.Nil(t, tran)
	require.Nil(t, receipts)
}

func TestTxDependancyExecutor_Execute_MoreWorkersThanTxs(t *testing.T) {
	t.Parallel()

	alloc := map[types.Address]*chain.GenesisAccount{
		depExecAddrA: {Balance: big.NewInt(1_000_000)},
	}
	executor, root := newDepExecExecutor(t, alloc)

	txs := []*types.Transaction{
		depExecTransferTx(1, depExecAddrA, depExecAddrR, 0, 100, 1, 21000),
	}

	pool := state.NewTxDependancyPool(txs, [][]uint64{{}})
	exec := state.NewTxDependancyExecutor(8, hclog.NewNullLogger())

	header := &types.Header{Number: 1, GasLimit: 1_000_000, Timestamp: 1}

	tran, receipts, err := exec.Execute(pool, executor, root, header, types.ZeroAddress)

	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.NotNil(t, tran)
	require.Equal(t, types.ReceiptSuccess, *receipts[0].Status)
}

// TestTxDependancyExecutor_Execute_CoinbaseFeesFromConcurrentTxsAllLand is a regression test
// for a lost-update race: every transaction implicitly credits the block's Coinbase address
// with its gas fee (executor.go's apply()), a side effect the tx-dependency graph has no
// visibility into. Many dependency-graph-independent transactions therefore legitimately run
// concurrently while all crediting the same address. Run repeatedly under -race, this used to
// intermittently lose fee credits because the publish to the shared blockRadix was an
// unconditional overwrite of a snapshot read earlier, rather than an atomic read-modify-write.
func TestTxDependancyExecutor_Execute_CoinbaseFeesFromConcurrentTxsAllLand(t *testing.T) {
	t.Parallel()

	const (
		numTxs    = 40
		gasUsed   = int64(21000)
		gasPrice  = int64(5)
		feePerTx  = gasUsed * gasPrice
		senderBal = 1_000_000
	)

	coinbase := types.StringToAddress("fee")

	for iter := range 5 {
		alloc := map[types.Address]*chain.GenesisAccount{}
		txs := make([]*types.Transaction, numTxs)
		deps := make([][]uint64, numTxs)

		for i := range numTxs {
			sender := types.BytesToAddress([]byte{byte(i + 1)})
			recipient := types.BytesToAddress([]byte{byte(i + 1), byte(i + 1)})
			alloc[sender] = &chain.GenesisAccount{Balance: big.NewInt(senderBal)}
			txs[i] = depExecTransferTx(byte(i+1), sender, recipient, 0, 1, gasPrice, uint64(gasUsed))
			deps[i] = nil
		}

		executor, root := newDepExecExecutor(t, alloc)
		pool := state.NewTxDependancyPool(txs, deps)
		exec := state.NewTxDependancyExecutor(8, hclog.NewNullLogger())
		header := &types.Header{Number: 1, GasLimit: 10_000_000, Timestamp: 1}

		tran, receipts, err := exec.Execute(pool, executor, root, header, coinbase)

		require.NoError(t, err)
		require.Len(t, receipts, numTxs)
		require.Equal(t, big.NewInt(feePerTx*numTxs), tran.GetBalance(coinbase),
			"%d: coinbase must receive every transaction's fee, none lost to concurrent updates", iter)
	}
}

// TestTxDependancyExecutor_Execute_SecondBlockMatchesSequential is a regression test for a bug
// where AddBalanceDoNotTrack (coinbase fee / burn credit) treated a miss in the per-block
// blockRadix as "this account has zero balance", instead of falling back to the account's real
// balance persisted by prior blocks. blockRadix is recreated empty for every block, so any
// address whose *only* touch in a given block goes through AddBalanceDoNotTrack (e.g. Coinbase,
// once nothing else in that block happens to touch it) had its carried-over balance silently
// discarded, corrupting the resulting state root starting from the second block onward - which
// is exactly what a validator verifying a real chain (not just a single isolated block) would
// hit. A single transaction touching an already-deployed contract's storage is enough to
// reproduce it; no concurrency is required.
func TestTxDependancyExecutor_Execute_SecondBlockMatchesSequential(t *testing.T) {
	t.Parallel()

	// init code SSTOREs slot 0 = 1 during construction, then deploys runtime code that SSTOREs
	// slot 1 = 2 on every call.
	var (
		depExecRuntimeCode = []byte{0x60, 0x02, 0x60, 0x01, 0x55, 0x00} // SSTORE(1,2); STOP
		depExecInitCode    = append([]byte{
			0x60, 0x01, 0x60, 0x00, 0x55, // SSTORE(0,1)
			0x60, 0x06, // PUSH1 len(runtime)=6
			0x60, 0x11, // PUSH1 offset=17 (start of runtime code below)
			0x60, 0x00, // PUSH1 destOffset=0
			0x39,       // CODECOPY
			0x60, 0x06, // PUSH1 len=6
			0x60, 0x00, // PUSH1 offset=0
			0xf3, // RETURN
		}, depExecRuntimeCode...)
	)

	sender := types.StringToAddress("5ec")
	alloc := map[types.Address]*chain.GenesisAccount{
		sender: {Balance: big.NewInt(1_000_000_000)},
	}

	header1 := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}
	header2 := &types.Header{Number: 2, GasLimit: 5_000_000, Timestamp: 2}

	deployTx := &types.Transaction{
		Hash:     types.Hash{1},
		From:     sender,
		To:       nil,
		Value:    big.NewInt(0),
		Gas:      1_000_000,
		GasPrice: big.NewInt(1),
		Nonce:    0,
		Type:     types.LegacyTx,
		Input:    depExecInitCode,
	}

	// --- reference: both blocks executed sequentially ---
	refExecutor, genesisRoot := newDepExecExecutor(t, alloc)

	refTxn1, err := refExecutor.BeginTxn(genesisRoot, header1, types.ZeroAddress)
	require.NoError(t, err)
	deployReceipt, err := refTxn1.Write(deployTx)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptSuccess, *deployReceipt.Status)
	contractAddr := *deployReceipt.ContractAddress
	_, block1Root, err := refTxn1.Commit()
	require.NoError(t, err)

	callTx := &types.Transaction{
		Hash:     types.Hash{2},
		From:     sender,
		To:       &contractAddr,
		Value:    big.NewInt(0),
		Gas:      1_000_000,
		GasPrice: big.NewInt(1),
		Nonce:    1,
		Type:     types.LegacyTx,
	}

	refTxn2, err := refExecutor.BeginTxn(block1Root, header2, types.ZeroAddress)
	require.NoError(t, err)
	callReceipt, err := refTxn2.Write(callTx)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptSuccess, *callReceipt.Status)

	_, block2RootSequential, err := refTxn2.Commit()
	require.NoError(t, err)

	// --- second block executed via the parallel dependency executor, on top of a
	// separately-built but identical first block ---
	parExecutor, parGenesisRoot := newDepExecExecutor(t, alloc)
	require.Equal(t, genesisRoot, parGenesisRoot)

	parTxn1, err := parExecutor.BeginTxn(parGenesisRoot, header1, types.ZeroAddress)
	require.NoError(t, err)
	_, err = parTxn1.Write(deployTx)
	require.NoError(t, err)
	_, parBlock1Root, err := parTxn1.Commit()
	require.NoError(t, err)
	require.Equal(t, block1Root, parBlock1Root)

	pool := state.NewTxDependancyPool([]*types.Transaction{callTx}, [][]uint64{{}})
	exec := state.NewTxDependancyExecutor(1, hclog.NewNullLogger())

	parTran, parReceipts, err := exec.Execute(pool, parExecutor, parBlock1Root, header2, types.ZeroAddress)

	require.NoError(t, err)
	require.Len(t, parReceipts, 1)
	require.Equal(t, types.ReceiptSuccess, *parReceipts[0].Status)

	_, block2RootParallel, err := parTran.Commit()
	require.NoError(t, err)

	require.Equal(t, block2RootSequential, block2RootParallel,
		"parallel verifier must compute the same state root as sequential execution on the second block of a chain")
}

// TestTxDependancyExecutor_Execute_DirectMutationAfterExecuteIsCommitted is a regression test
// for consensus hooks - most notably IBFT PoS's PreCommitStateFunc, which deploys the staking
// contract via Transition.SetAccountDirectly - that mutate the *Transition returned by Execute
// directly, after Execute has already returned and its per-tx PopulateBlockRadix calls are done
// (see blockchain.go's executeBlockTransactions: ProcessBlock, then consensus.PreCommitState,
// then txn.Commit). Those direct writes only ever land in TxnVerifier's local txLocalMap; Commit
// used to read only from the shared blockRadix, so anything written this way was silently
// dropped from the committed state instead of persisted - producing a state root that omits it
// entirely, which is exactly the shape of bug that shows up as "invalid block state root" only
// once a hook like this actually deploys something.
func TestTxDependancyExecutor_Execute_DirectMutationAfterExecuteIsCommitted(t *testing.T) {
	t.Parallel()

	alloc := map[types.Address]*chain.GenesisAccount{
		depExecAddrA: {Balance: big.NewInt(1_000_000)},
	}
	executor, root := newDepExecExecutor(t, alloc)

	txs := []*types.Transaction{
		depExecTransferTx(1, depExecAddrA, depExecAddrR, 0, 100, 1, 21000),
	}

	pool := state.NewTxDependancyPool(txs, [][]uint64{{}})
	exec := state.NewTxDependancyExecutor(1, hclog.NewNullLogger())

	header := &types.Header{Number: 1, GasLimit: 1_000_000, Timestamp: 1}

	tran, receipts, err := exec.Execute(pool, executor, root, header, types.ZeroAddress)
	require.NoError(t, err)
	require.Len(t, receipts, 1)

	// mirrors registerStakingContractDeploymentHooks' PreCommitStateFunc: mutate the returned
	// Transition directly, after Execute() has already returned.
	deployedAddr := types.StringToAddress("dep10y")
	require.NoError(t, tran.SetAccountDirectly(deployedAddr, &chain.GenesisAccount{
		Code:    []byte{0x60, 0x01},
		Balance: big.NewInt(500),
		Nonce:   1,
		Storage: map[types.Hash]types.Hash{{1}: {2}},
	}))

	snap, _, err := tran.Commit()
	require.NoError(t, err)

	// verify against the freshly committed snapshot, not the live Transition - a live read
	// would still succeed via txLocalMap even if the write never made it into blockRadix/commit.
	account, err := snap.GetAccount(deployedAddr)
	require.NoError(t, err)
	require.NotNil(t, account, "directly-deployed account must survive Commit")
	require.Equal(t, big.NewInt(500), account.Balance)
	require.Equal(t, uint64(1), account.Nonce)

	code, ok := snap.GetCode(types.BytesToHash(account.CodeHash))
	require.True(t, ok)
	require.Equal(t, []byte{0x60, 0x01}, code)

	require.Equal(t, types.Hash{2}, snap.GetStorage(deployedAddr, account.Root, types.Hash{1}))
}
