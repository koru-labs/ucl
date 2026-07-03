package state_test

import (
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/contracts/staking"
	stakingHelper "github.com/0xPolygon/polygon-edge/helper/staking"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
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

func depExecTransferTx(
	hashByte byte, from, to types.Address, nonce uint64, value, gasPrice int64, gas uint64,
) *types.Transaction {
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

// Regression test: every tx implicitly credits Coinbase with its gas fee, invisible to the
// dependency graph, so many independent txs legitimately run concurrently while all crediting
// it. Under -race this used to intermittently lose fee credits: the publish to the shared
// blockRadix was an unconditional overwrite of an earlier snapshot read, not an atomic RMW.
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

// Regression test: AddBalanceDoNotTrack used to treat a miss in the per-block blockRadix as
// zero balance instead of falling back to the account's real prior-block balance. blockRadix is
// recreated empty every block, so an address only ever touched via AddBalanceDoNotTrack (e.g.
// Coinbase) had its carried-over balance silently dropped from the second block onward.
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

// Regression test: consensus hooks (e.g. IBFT PoS's PreCommitStateFunc, staking contract
// deploy) mutate the *Transition directly via SetAccountDirectly after Execute has returned.
// Those writes only ever land in txLocalMap; Commit used to read only the shared blockRadix, so
// they were silently dropped from the committed state instead of persisted.
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

func TestTxDependancyExecutor_StakingChain_RootMatchesSequential(t *testing.T) {
	const numStakers = 4

	senders := make([]types.Address, numStakers)
	alloc := map[types.Address]*chain.GenesisAccount{}

	for i := range numStakers {
		senders[i] = types.BytesToAddress([]byte{0x50, byte(i + 1)})
		alloc[senders[i]] = &chain.GenesisAccount{Balance: big.NewInt(0).Mul(big.NewInt(100), big.NewInt(1e18))}
	}

	header1 := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}
	header2 := &types.Header{Number: 2, GasLimit: 30_000_000, Timestamp: 2}

	newExecutor := func(t *testing.T) *state.Executor {
		t.Helper()

		mstate := itrie.NewState(itrie.NewMemoryStorage())
		executor := state.NewExecutor(&chain.Params{
			ChainID:      100,
			Forks:        chain.AllForksEnabled,
			BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
		}, mstate, hclog.NewNullLogger())
		executor.GetHash = func(*types.Header) func(uint64) types.Hash {
			return func(uint64) types.Hash { return types.Hash{} }
		}

		return executor
	}

	genesisValidators := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(types.BytesToAddress([]byte{0x60, 1})),
		validators.NewECDSAValidator(types.BytesToAddress([]byte{0x60, 2})),
		validators.NewECDSAValidator(types.BytesToAddress([]byte{0x60, 3})),
		validators.NewECDSAValidator(types.BytesToAddress([]byte{0x60, 4})),
	)

	stakingAccount, err := stakingHelper.PredeployStakingSC(genesisValidators, stakingHelper.PredeployParams{
		MinValidatorCount: 1,
		MaxValidatorCount: 20,
	})
	require.NoError(t, err)

	stakeTxs := make([]*types.Transaction, numStakers)
	deps := make([][]uint64, numStakers)

	for i := range numStakers {
		stakeTxs[i] = &types.Transaction{
			Hash:     types.Hash{byte(i + 1)},
			From:     senders[i],
			To:       &staking.AddrStakingContract,
			Value:    big.NewInt(0).Mul(big.NewInt(1), big.NewInt(1e18)),
			Gas:      1_000_000,
			GasPrice: big.NewInt(1),
			Nonce:    0,
			Type:     types.LegacyTx,
		}

		if i == 0 {
			deps[i] = nil
		} else {
			deps[i] = []uint64{uint64(i - 1)}
		}
	}

	// --- reference: everything sequential ---
	refExecutor := newExecutor(t)
	genesisRoot, err := refExecutor.WriteGenesis(alloc, types.ZeroHash)
	require.NoError(t, err)

	refTxn1, err := refExecutor.BeginTxn(genesisRoot, header1, types.ZeroAddress)
	require.NoError(t, err)
	require.NoError(t, refTxn1.SetAccountDirectly(staking.AddrStakingContract, stakingAccount))
	_, block1Root, err := refTxn1.Commit()
	require.NoError(t, err)

	refTxn2, err := refExecutor.BeginTxn(block1Root, header2, types.ZeroAddress)
	require.NoError(t, err)

	for i, tx := range stakeTxs {
		receipt, err := refTxn2.Write(tx)
		require.NoError(t, err)
		require.Equal(t, types.ReceiptSuccess, *receipt.Status, "sequential: stake tx %d must succeed", i)
	}

	_, block2RootSequential, err := refTxn2.Commit()
	require.NoError(t, err)

	// --- block 2 via the parallel dependency executor, matching the real log's dep chain ---
	parExecutor := newExecutor(t)
	parGenesisRoot, err := parExecutor.WriteGenesis(alloc, types.ZeroHash)
	require.NoError(t, err)
	require.Equal(t, genesisRoot, parGenesisRoot)

	parTxn1, err := parExecutor.BeginTxn(parGenesisRoot, header1, types.ZeroAddress)
	require.NoError(t, err)
	require.NoError(t, parTxn1.SetAccountDirectly(staking.AddrStakingContract, stakingAccount))
	_, parBlock1Root, err := parTxn1.Commit()
	require.NoError(t, err)
	require.Equal(t, block1Root, parBlock1Root)

	pool := state.NewTxDependancyPool(stakeTxs, deps)
	exec := state.NewTxDependancyExecutor(4, hclog.NewNullLogger())
	parTran, parReceipts, err := exec.Execute(pool, parExecutor, parBlock1Root, header2, types.ZeroAddress)
	require.NoError(t, err)
	require.Len(t, parReceipts, numStakers)

	for i, r := range parReceipts {
		require.Equal(t, types.ReceiptSuccess, *r.Status, "parallel: stake tx %d must succeed", i)
	}

	_, block2RootParallel, err := parTran.Commit()
	require.NoError(t, err)

	t.Logf("sequential root: %s", block2RootSequential)
	t.Logf("parallel   root: %s", block2RootParallel)

	require.Equal(t, block2RootSequential, block2RootParallel,
		"parallel verifier must compute the same state root as sequential execution for a chain of stake() calls")
}

// Regression test: TxnVerifier.Snapshot()/RevertToSnapshot() used to be no-ops, so a reverted
// call (any require()/revert, a normal outcome, not just a malformed tx) kept its partial
// writes instead of discarding them - producing a different root than the sequential path for
// a perfectly valid, included transaction (ReceiptStatus: Failed, not rejected).
func TestTxDependancyExecutor_Execute_RevertedCallDoesNotPersistWrites(t *testing.T) {
	t.Parallel()

	sender := types.StringToAddress("5ec")
	alloc := map[types.Address]*chain.GenesisAccount{
		sender: {Balance: big.NewInt(1_000_000_000)},
	}

	header1 := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}
	header2 := &types.Header{Number: 2, GasLimit: 5_000_000, Timestamp: 2}

	// init code: SSTORE(slot0, 5) - baseline value set once during construction.
	// runtime code: SSTORE(slot0, 1); REVERT(0,0) - every call writes then reverts.
	runtimeCode := []byte{
		0x60, 0x01, 0x60, 0x00, 0x55, // SSTORE(0, 1)
		0x60, 0x00, 0x60, 0x00, 0xfd, // REVERT(0,0)
	}
	initCode := append([]byte{
		0x60, 0x05, 0x60, 0x00, 0x55, // SSTORE(0, 5)
		0x60, byte(len(runtimeCode)), // PUSH1 len(runtime)
		0x60, 0x11, // PUSH1 offset=17 (start of runtime code below)
		0x60, 0x00, // PUSH1 destOffset=0
		0x39,                         // CODECOPY
		0x60, byte(len(runtimeCode)), // PUSH1 len
		0x60, 0x00, // PUSH1 offset=0
		0xf3, // RETURN
	}, runtimeCode...)

	deployTx := &types.Transaction{
		Hash: types.Hash{1}, From: sender, To: nil, Value: big.NewInt(0),
		Gas: 1_000_000, GasPrice: big.NewInt(1), Nonce: 0, Type: types.LegacyTx,
		Input: initCode,
	}

	// --- reference: sequential ---
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
		Hash: types.Hash{2}, From: sender, To: &contractAddr, Value: big.NewInt(0),
		Gas: 1_000_000, GasPrice: big.NewInt(1), Nonce: 1, Type: types.LegacyTx,
	}

	refTxn2, err := refExecutor.BeginTxn(block1Root, header2, types.ZeroAddress)
	require.NoError(t, err)
	callReceipt, err := refTxn2.Write(callTx)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptFailed, *callReceipt.Status, "call must revert")
	require.Equal(t, types.Hash{31: 5}, refTxn2.GetStorage(contractAddr, types.Hash{}),
		"sequential: slot0 must remain at its pre-call value of 5 since the call reverted")
	_, block2RootSequential, err := refTxn2.Commit()
	require.NoError(t, err)

	// --- block2 via parallel executor ---
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
	require.Equal(t, types.ReceiptFailed, *parReceipts[0].Status, "call must revert")
	require.Equal(t, types.Hash{31: 5}, parTran.GetStorage(contractAddr, types.Hash{}),
		"parallel: slot0 must remain at its pre-call value of 5 since the call reverted")

	_, block2RootParallel, err := parTran.Commit()
	require.NoError(t, err)

	require.Equal(t, block2RootSequential, block2RootParallel,
		"parallel verifier must compute the same state root as sequential execution for a reverted call")
}

// Builds a 50-tx block with a deliberate (non-random) dependency graph: independent transfers,
// a same-sender nonce chain, a relay chain, fan-out/fan-in "diamond" gadgets, and a two-level
// join tree. Every edge reflects a real address conflict, since PopulateBlockRadix's
// last-writer-wins merge can silently drop an update if two same-address txs aren't ordered.
func TestTxDependancyExecutor_Execute_LargeCleverDependencyGraph(t *testing.T) {
	t.Parallel()

	const (
		gasPrice = int64(1)
		gasLimit = uint64(21000)
	)

	// types.StringToAddress hex-decodes its argument, so labels with non-hex characters (like
	// the descriptive ones below) would all silently collapse to the zero address; BytesToAddress
	// copies the raw label bytes instead, keeping each one distinct.
	addr := func(label string) types.Address { return types.BytesToAddress([]byte(label)) }
	alloc := map[types.Address]*chain.GenesisAccount{}

	fund := func(a types.Address, balance int64) types.Address {
		alloc[a] = &chain.GenesisAccount{Balance: big.NewInt(balance)}

		return a
	}

	var (
		txs  []*types.Transaction
		deps [][]uint64
		hash byte
	)

	nonces := map[types.Address]uint64{}

	// add appends a transfer from `from` to `to`, auto-assigning from's next nonce, and returns
	// the new tx's index so callers can wire it up as a dependency of later txs.
	add := func(from, to types.Address, value int64, dependsOn ...uint64) uint64 {
		hash++
		nonce := nonces[from]
		nonces[from] = nonce + 1

		txs = append(txs, depExecTransferTx(hash, from, to, nonce, value, gasPrice, gasLimit))
		deps = append(deps, dependsOn)

		return uint64(len(txs) - 1)
	}

	// --- group A: 13 fully independent transfers (disjoint addresses, no deps at all) plus one
	// same-sender two-hop chain, so "no dependency" and "ordered purely by nonce" sit side by
	// side.
	for i := range 13 {
		from := fund(addr(fmt.Sprintf("ind%d_from", i)), 1_000_000)
		to := addr(fmt.Sprintf("ind%d_to", i))
		add(from, to, 1_000)
	}

	ncA := fund(addr("nc_a"), 1_000_000)
	ncFirst := add(ncA, addr("nc_to0"), 500)
	add(ncA, addr("nc_to1"), 500, ncFirst)

	// --- group B: an 8-hop relay chain of distinct senders, each hop strictly depending on the
	// one before it.
	relay := make([]types.Address, 9)
	for i := range relay {
		relay[i] = addr(fmt.Sprintf("relay%d", i))
	}

	fund(relay[0], 10_000_000)

	relayValues := []int64{5_000_000, 2_000_000, 1_000_000, 500_000, 200_000, 100_000, 50_000, 20_000}

	var prevHop uint64

	for i, v := range relayValues {
		if i == 0 {
			prevHop = add(relay[i], relay[i+1], v)
		} else {
			prevHop = add(relay[i], relay[i+1], v, prevHop)
		}
	}

	// --- group C: 4 "diamond" gadgets. A root funds two disjoint descendants - one via its own
	// next nonce, one via the balance it just credited - which run fully in parallel since they
	// share no address; a join transaction then depends directly on both branch tips.
	var diamondU3 [4]types.Address

	for g := range 4 {
		u0 := fund(addr(fmt.Sprintf("dia%d_u0", g)), 5_000_000)
		u1 := addr(fmt.Sprintf("dia%d_u1", g))
		u2 := addr(fmt.Sprintf("dia%d_u2", g))
		u3 := addr(fmt.Sprintf("dia%d_u3", g))

		root := add(u0, u1, 2_000_000)
		childA := add(u1, u2, 500_000, root)   // spends the balance the root just credited
		childB := add(u0, u3, 1_000_000, root) // root's own next nonce
		add(u2, u3, 100_000, childA, childB)

		diamondU3[g] = u3
	}

	// --- group D: 4 independent 2-hop leaf chains, reduced pairwise into a 2-level join tree (a
	// "diamond of diamonds"): each level-1 join depends directly on two leaf-chain tips, and the
	// final join depends directly on both level-1 joins.
	leaf := make([][3]types.Address, 4)
	leafTip := make([]uint64, 4)

	for k := range 4 {
		leaf[k][0] = fund(addr(fmt.Sprintf("leaf%d_0", k)), 2_000_000)
		leaf[k][1] = addr(fmt.Sprintf("leaf%d_1", k))
		leaf[k][2] = addr(fmt.Sprintf("leaf%d_2", k))

		hop0 := add(leaf[k][0], leaf[k][1], 1_000_000)
		leafTip[k] = add(leaf[k][1], leaf[k][2], 500_000, hop0)
	}

	join10 := add(leaf[0][2], leaf[1][2], 100_000, leafTip[0], leafTip[1])
	join11 := add(leaf[2][2], leaf[3][2], 100_000, leafTip[2], leafTip[3])
	add(leaf[1][2], leaf[3][2], 50_000, join10, join11)

	require.Len(t, txs, 50)

	header := &types.Header{Number: 1, GasLimit: 5_000_000, Timestamp: 1}

	// --- reference: the identical 50 txs executed sequentially, in the same topological order
	// they were built in (every dependency only ever points at an earlier index).
	seqExecutor, genesisRoot := newDepExecExecutor(t, alloc)

	seqTxn, err := seqExecutor.BeginTxn(genesisRoot, header, types.ZeroAddress)
	require.NoError(t, err)

	for i, tx := range txs {
		receipt, err := seqTxn.Write(tx)
		require.NoError(t, err)
		require.Equal(t, types.ReceiptSuccess, *receipt.Status, "sequential: tx %d must succeed", i)
	}

	_, seqRoot, err := seqTxn.Commit()
	require.NoError(t, err)

	// --- the same 50 txs through the parallel dependency executor.
	parExecutor, parGenesisRoot := newDepExecExecutor(t, alloc)
	require.Equal(t, genesisRoot, parGenesisRoot)

	pool := state.NewTxDependancyPool(txs, deps)
	exec := state.NewTxDependancyExecutor(8, hclog.NewNullLogger())

	parTran, receipts, err := exec.Execute(pool, parExecutor, parGenesisRoot, header, types.ZeroAddress)
	require.NoError(t, err)
	require.Len(t, receipts, 50)

	for i, r := range receipts {
		require.Equal(t, types.ReceiptSuccess, *r.Status, "parallel: tx %d must succeed", i)
	}

	// spot-check a few representative nodes before committing, for a more localized failure
	// signal than a root mismatch alone would give.
	require.Equal(t, big.NewInt(20_000), parTran.GetBalance(relay[8]),
		"relay chain tail must receive only the last hop's value")
	require.Equal(t, big.NewInt(1_100_000), parTran.GetBalance(diamondU3[0]),
		"diamond join recipient must reflect both the fan-out credit and the join transfer")
	require.Equal(t, big.NewInt(650_000), parTran.GetBalance(leaf[3][2]),
		"final join recipient must reflect both levels of the reduction tree")

	_, parRoot, err := parTran.Commit()
	require.NoError(t, err)

	require.Equal(t, seqRoot, parRoot,
		"parallel dependency executor must match sequential execution for a large mixed dependency graph")
}
