package ibft

import (
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/hook"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	sth "github.com/0xPolygon/polygon-edge/state/statetesthelper"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------------------------
//  1. Cross-contract CALL: Router.routerInc / routerTransfer forward via CALL into Balances, so the
//     dirtied account is Balances even though tx.To is a Router. Two txs through *different* routers
//     that mutate the same balances[] slot must be serialized by the derived graph. The proposer has
//     to attribute the conflict to the Balances account, not the Router that was called.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_CrossContractCall(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12), parAddr(13)}
	addrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.BalancesInitHex)),
	})
	balAddr := addrs[0]

	routers := h.deployMore(t, [][]byte{
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.RouterInitHex), balAddr),
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.RouterInitHex), balAddr),
	})
	router1, router2 := routers[0], routers[1]

	t1, t2 := parAddr(101), parAddr(102)
	txs := []*types.Transaction{
		sth.CallTx(0x30, callers[0], router1, 0,
			sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(t1), sth.ContractPaddUint256(100))),
		sth.CallTx(0x31, callers[1], router2, 0,
			sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(t1), sth.ContractPaddUint256(50))),
		sth.CallTx(0x32, callers[2], router1, 0,
			sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(t2), sth.ContractPaddUint256(200))),
		sth.CallTx(0x33, callers[3], router2, 0,
			sth.CallData("routerTransfer(address,address,uint256)",
				sth.ContractPaddAddress(t1), sth.ContractPaddAddress(t2), sth.ContractPaddUint256(30))),
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 40)

	// sanity: the CALL forwarding really mutated Balances storage.
	assertStorageUint(t, h, balAddr, sth.ScBalanceSlot(t1, 1), 120) // 100+50-30
	assertStorageUint(t, h, balAddr, sth.ScBalanceSlot(t2, 1), 230) // 200+30

	// over-dense graph (extra spurious edges) must still verify.
	h.requireVerifiesWith(t, block, [][]uint64{{}, {0}, {0, 1}, {0, 1, 2}}, 20)

	// broken graph dropping tx1's real dependency on tx0 (both write balances[t1]) must diverge.
	h.requireCanDiverge(t, block, [][]uint64{{}, {}, {}, {0, 1, 2}}, 100)
}

// ---------------------------------------------------------------------------------------------
//  2. DELEGATECALL vs CALL storage attribution. Proxy.pinc DELEGATECALLs Balances.incBalance, so the
//     write lands in the *Proxy's own* storage; Router.routerInc CALLs, so its write lands in the
//     Balances contract. The proposer must attribute proxy writes to the proxy account and router
//     writes to the balances account.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_DelegateCallVsCall(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12), parAddr(13)}
	addrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.BalancesInitHex)),
	})
	balAddr := addrs[0]

	more := h.deployMore(t, [][]byte{
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.RouterInitHex), balAddr),
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.ProxyInitHex), balAddr),
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.ProxyInitHex), balAddr),
	})
	router, proxyA, proxyB := more[0], more[1], more[2]

	target := parAddr(101)
	txs := []*types.Transaction{
		sth.CallTx(0x40, callers[0], router, 0,
			sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(100))),
		sth.CallTx(0x41, callers[1], proxyA, 0,
			sth.CallData("pinc(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(7))),
		sth.CallTx(0x42, callers[2], proxyA, 0,
			sth.CallData("pinc(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(9))),
		sth.CallTx(0x43, callers[3], proxyB, 0,
			sth.CallData("pinc(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(11))),
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 40)

	// router write lands in Balances; proxy writes land in each proxy's own storage.
	assertStorageUint(t, h, balAddr, sth.ScBalanceSlot(target, 1), 100)
	assertStorageUint(t, h, proxyA, sth.ScBalanceSlot(target, 1), 16) // 7+9
	assertStorageUint(t, h, proxyB, sth.ScBalanceSlot(target, 1), 11)

	// dropping the tx1<-tx2 edge (both read-modify-write proxyA's slot) must diverge.
	h.requireCanDiverge(t, block, [][]uint64{{}, {}, {}, {}}, 100)
}

// ---------------------------------------------------------------------------------------------
//  3. Deep chain A -> B -> C: ATop.bump writes A.v then calls BMid.bump (writes B.v) which calls
//     CLeaf.bump (writes C.v). A single tx dirties three accounts, and the proposer must serialize
//     any two txs overlapping on *any* of them.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_DeepChainMultiAccount(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12), parAddr(13)}
	cAddrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.CLeafInitHex)),
	})
	cAddr := cAddrs[0]

	bAddr := h.deployMore(t, [][]byte{
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.BMidInitHex), cAddr),
	})[0]
	aAddr := h.deployMore(t, [][]byte{
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.ATopInitHex), bAddr),
	})[0]

	bump := func(x uint64) []byte {
		return sth.CallData("bump(uint256)", sth.ContractPaddUint256(x))
	}

	txs := []*types.Transaction{
		sth.CallTx(0x50, callers[0], aAddr, 0, bump(5)), // A,B,C
		sth.CallTx(0x51, callers[1], aAddr, 0, bump(7)), // A,B,C
		sth.CallTx(0x52, callers[2], bAddr, 0, bump(3)), // B,C
		sth.CallTx(0x53, callers[3], cAddr, 0, bump(2)), // C
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 40)

	// C.v accumulates every bump: 5+7+3+2.
	assertStorageUint(t, h, cAddr, types.ZeroHash, 17)

	// dropping tx3's dependency on the C writers must diverge (all read-modify-write C.v).
	h.requireCanDiverge(t, block, [][]uint64{{}, {0}, {0, 1}, {}}, 100)
}

// ---------------------------------------------------------------------------------------------
//  4. CREATE dependency: tx0 calls Factory.make(), which CREATEs a fresh Balances child; later txs
//     call that child. The child does not exist at parent state, so its callers have a hard
//     dependency on tx0's flush. Exercises the proposer + verifier for within-block contract creation.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_CreateFactoryDependency(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12)}
	fAddrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.FactoryInitHex)),
	})
	factoryAddr := fAddrs[0]
	// a freshly deployed contract starts at nonce 1 (EIP-161), so its first CREATE uses nonce 1.
	childAddr := crypto.CreateAddress(factoryAddr, 1)

	target := parAddr(101)
	txs := []*types.Transaction{
		sth.CallTx(0x60, callers[0], factoryAddr, 0, sth.CallData("make()")),
		sth.CallTx(0x61, callers[1], childAddr, 0,
			sth.CallData("incBalance(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(100))),
		sth.CallTx(0x62, callers[2], childAddr, 0,
			sth.CallData("incBalance(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(50))),
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 40)

	assertStorageUint(t, h, childAddr, sth.ScBalanceSlot(target, 1), 150)
}

// ---------------------------------------------------------------------------------------------
//  5. SELFDESTRUCT + re-touch: tx0 writes storage on Killable, tx1 selfdestructs it, tx2 sends value
//     to the (now empty) address. All three touch the same account and must be serialized.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_SelfDestructThenTouch(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12)}
	kAddrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.KillableInitHex)),
	})
	kAddr := kAddrs[0]
	beneficiary := parAddr(150)

	valueTx := &types.Transaction{
		Hash: types.Hash{0x73}, From: callers[2], To: &kAddr, Value: big.NewInt(1000),
		Gas: 100_000, GasPrice: ethgo.Gwei(2), Nonce: 0, Type: types.LegacyTx, Input: []byte{},
	}

	txs := []*types.Transaction{
		sth.CallTx(0x71, callers[0], kAddr, 0, sth.CallData("setX(uint256)", sth.ContractPaddUint256(42))),
		sth.CallTx(0x72, callers[1], kAddr, 0, sth.CallData("boom(address)", sth.ContractPaddAddress(beneficiary))),
		valueTx,
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 40)
}

// ---------------------------------------------------------------------------------------------
//  6. Graph-shape stress via EOA transfers (this test plus LongSerialChain and Diamond below):
//     wide fan-out/fan-in, long serial chain, and a diamond. The proposer derives the graph from
//     the conflict pattern; the verifier must reproduce the root across many worker interleavings.
//     Here: one seed tx credits r0, then n fully independent transfers (maximum parallelism), and
//     a final tx credits r0 again, fanning back in across the whole block.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_WideFanOutFanIn(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	const n = 25

	senders := make([]types.Address, n+2)
	balances := map[types.Address]*big.Int{}
	receiverZero := parAddr(200)

	for i := range senders {
		senders[i] = parAddr(byte(20 + i))
		balances[senders[i]] = ethgo.Ether(100)
	}

	h.setupParent(t, balances, nil)

	txs := make([]*types.Transaction, 0, n+2)
	txs = append(txs, eoaTransfer(1, senders[0], receiverZero, 0, 100)) // seed r0

	for i := range n {
		txs = append(txs,
			eoaTransfer(byte(i+2), senders[i+1], parAddr(byte(100+i)), 0, int64(10+i))) // independent
	}

	txs = append(txs, eoaTransfer(0xF0, senders[n+1], receiverZero, 0, 250)) // fan-in on r0

	block := h.build(t, txs)

	h.requireVerifies(t, block, 30)
}

// ---------------------------------------------------------------------------------------------
//
//	6b. Long serial chain: every one of the n transfers credits the same receiver, so the derived
//	   graph degenerates into a single maximal dependency chain with zero exploitable parallelism.
//	   The verifier must respect the full ordering and still reproduce the builder's root.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_LongSerialChain(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	const n = 15

	senders := make([]types.Address, n)
	balances := map[types.Address]*big.Int{}
	r0 := parAddr(200)

	for i := range senders {
		senders[i] = parAddr(byte(20 + i))
		balances[senders[i]] = ethgo.Ether(100)
	}

	h.setupParent(t, balances, nil)

	txs := make([]*types.Transaction, n)
	for i := range n {
		txs[i] = eoaTransfer(byte(i+1), senders[i], r0, 0, int64(i+1)) // every tx credits the same receiver
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 30)
}

// ---------------------------------------------------------------------------------------------
//
//	6c. Diamond: the smallest fully-conflicting block - four transfers from distinct senders all
//	   credit the same receiver, so every tx conflicts with every earlier one and the graph must
//	   serialize the whole block despite the four independent senders.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_Diamond(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	senders := []types.Address{parAddr(20), parAddr(21), parAddr(22), parAddr(23)}
	balances := map[types.Address]*big.Int{}
	receiverZero := parAddr(200)

	for _, s := range senders {
		balances[s] = ethgo.Ether(100)
	}

	h.setupParent(t, balances, nil)

	txs := []*types.Transaction{
		eoaTransfer(1, senders[0], receiverZero, 0, 100),
		eoaTransfer(2, senders[1], receiverZero, 0, 50),
		eoaTransfer(3, senders[2], receiverZero, 0, 25),
		eoaTransfer(4, senders[3], receiverZero, 0, 10),
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 30)
}

// ---------------------------------------------------------------------------------------------
//  7. Vary worker count: the same block and derived graph must yield the builder's root at every
//     worker count, since merge bugs often only appear at a specific level of parallelism.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_VaryWorkerCount(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12), parAddr(13)}
	addrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.BalancesInitHex)),
	})
	balAddr := addrs[0]

	routers := h.deployMore(t, [][]byte{
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.RouterInitHex), balAddr),
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.RouterInitHex), balAddr),
	})
	router1, router2 := routers[0], routers[1]

	t1, t2 := parAddr(101), parAddr(102)
	txs := []*types.Transaction{
		sth.CallTx(0x30, callers[0], router1, 0,
			sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(t1), sth.ContractPaddUint256(100))),
		sth.CallTx(0x31, callers[1], router2, 0,
			sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(t1), sth.ContractPaddUint256(50))),
		sth.CallTx(0x32, callers[2], router1, 0,
			sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(t2), sth.ContractPaddUint256(200))),
		sth.CallTx(0x33, callers[3], router2, 0,
			sth.CallData("routerTransfer(address,address,uint256)",
				sth.ContractPaddAddress(t1), sth.ContractPaddAddress(t2), sth.ContractPaddUint256(30))),
	}

	const iterations = 8

	block := h.build(t, txs)
	graph := createDeps(t, block)

	for _, workers := range []int{1, 2, 3, 4, 8, 16} {
		for iter := range iterations {
			pool := state.NewTxDependancyPool(block.Transactions, graph)
			exc := state.NewTxDependancyExecutor(workers, hclog.NewNullLogger())

			tran, _, err := exc.Execute(pool, h.executor, h.parentHeader.StateRoot, block.Header, h.proposer)
			require.NoError(t, err)

			_, root, err := tran.Commit()
			require.NoError(t, err)

			require.Equal(t, block.Header.StateRoot, root,
				"workers=%d iter=%d must match builder root", workers, iter)
		}
	}
}

// ---------------------------------------------------------------------------------------------
//  8. Concurrent block-creator credit: n independent transfers (no conflicts between txs) all credit
//     the same block creator's balance via the special AddBalanceDoNotTrack path, with no dependency
//     edges between them. That path is supposed to be safe without serialization; verify it stays
//     consistent under parallel execution.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_ConcurrentBlockCreatorCredit(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	const (
		n          = 20
		iterations = 40
	)

	senders := make([]types.Address, n)
	balances := map[types.Address]*big.Int{}

	for i := range senders {
		senders[i] = parAddr(byte(20 + i))
		balances[senders[i]] = ethgo.Ether(100)
	}

	h.setupParent(t, balances, nil)

	txs := make([]*types.Transaction, n)
	for i := range n {
		txs[i] = eoaTransfer(byte(i+1), senders[i], parAddr(byte(100+i)), 0, int64(i+1)) // all independent
	}

	block := h.build(t, txs)

	// every tx credits h.proposer's balance concurrently through globalAddBalances; roots must match.
	graph := createDeps(t, block)
	for i := range iterations {
		require.Equal(t, block.Header.StateRoot, h.processBlockInParallel(t, block, graph),
			"iteration %d: concurrent block-creator credit must be consistent", i)
	}
}

// ---------------------------------------------------------------------------------------------
//  9. Reverted tx mid-block: tx1's decBalance underflows its require() and reverts, but stays in the
//     block (failed receipt, gas consumed, sender nonce bumped). The parallel verifier must reproduce
//     not just the state root but the exact receipt statuses and gas accounting.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_RevertedTxMidBlock(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12), parAddr(13)}
	addrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.BalancesInitHex)),
	})
	balAddr := addrs[0]

	t1 := parAddr(101)
	txs := []*types.Transaction{
		sth.CallTx(0x81, callers[0], balAddr, 0,
			sth.CallData("incBalance(address,uint256)", sth.ContractPaddAddress(t1), sth.ContractPaddUint256(500))),
		// dec 600 > 500: require fails, tx reverts but is still included
		sth.CallTx(0x82, callers[1], balAddr, 0,
			sth.CallData("decBalance(address,uint256)", sth.ContractPaddAddress(t1), sth.ContractPaddUint256(600))),
		sth.CallTx(0x83, callers[2], balAddr, 0,
			sth.CallData("incBalance(address,uint256)", sth.ContractPaddAddress(t1), sth.ContractPaddUint256(100))),
		sth.CallTx(0x84, callers[3], balAddr, 0,
			sth.CallData("decBalance(address,uint256)", sth.ContractPaddAddress(t1), sth.ContractPaddUint256(550))),
	}

	block := h.build(t, txs)

	require.Equal(t, uint64(types.ReceiptSuccess), uint64(*h.lastBuildReceipts[0].Status))
	require.Equal(t, uint64(types.ReceiptFailed), uint64(*h.lastBuildReceipts[1].Status),
		"underflowing decBalance must produce a failed receipt")
	require.Equal(t, uint64(types.ReceiptSuccess), uint64(*h.lastBuildReceipts[2].Status))
	require.Equal(t, uint64(types.ReceiptSuccess), uint64(*h.lastBuildReceipts[3].Status))

	h.requireVerifies(t, block, 40)

	// receipt equivalence: status, per-tx gas and cumulative gas must match the builder's
	graph := createDeps(t, block)
	for iter := range 10 {
		root, receipts := h.processBlockInParallelReceipts(t, block, graph)
		require.Equal(t, block.Header.StateRoot, root, "iteration %d", iter)
		require.Len(t, receipts, len(h.lastBuildReceipts))

		for i, r := range receipts {
			require.Equal(t, *h.lastBuildReceipts[i].Status, *r.Status, "iter %d: tx %d receipt status", iter, i)
			require.Equal(t, h.lastBuildReceipts[i].GasUsed, r.GasUsed, "iter %d: tx %d gas used", iter, i)
			require.Equal(t, h.lastBuildReceipts[i].CumulativeGasUsed, r.CumulativeGasUsed,
				"iter %d: tx %d cumulative gas", iter, i)
		}
	}

	// 500 - 0 (reverted) + 100 - 550
	assertStorageUint(t, h, balAddr, sth.ScBalanceSlot(t1, 1), 50)
}

// ---------------------------------------------------------------------------------------------
//  10. SELFDESTRUCT with a funded contract: tx0 funds Killable, tx1 selfdestructs it sweeping the
//     balance to the beneficiary, tx2 credits the beneficiary directly. The tx0<-tx1 edge exists
//     only through the contract's *balance* (tx1 reads it to sweep); dropping it must diverge
//     (the sweep would race the funding and the beneficiary could receive 0).
//
// ---------------------------------------------------------------------------------------------
func TestParallel_SelfDestructFundedBeneficiary(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12)}
	kAddrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.KillableInitHex)),
	})
	kAddr := kAddrs[0]
	beneficiary := parAddr(150)

	fundTx := &types.Transaction{
		Hash: types.Hash{0x91}, From: callers[0], To: &kAddr, Value: big.NewInt(5000),
		Gas: 100_000, GasPrice: ethgo.Gwei(2), Nonce: 0, Type: types.LegacyTx, Input: []byte{},
	}
	tipTx := &types.Transaction{
		Hash: types.Hash{0x93}, From: callers[2], To: &beneficiary, Value: big.NewInt(1),
		Gas: 100_000, GasPrice: ethgo.Gwei(2), Nonce: 0, Type: types.LegacyTx, Input: []byte{},
	}

	txs := []*types.Transaction{
		fundTx, // receive() payable
		sth.CallTx(0x92, callers[1], kAddr, 0, sth.CallData("boom(address)", sth.ContractPaddAddress(beneficiary))),
		tipTx,
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 40)

	// the sweep and the tip both landed; the contract is gone
	assertBalance(t, h, beneficiary, 5001)
	assertBalance(t, h, kAddr, 0)

	tran, err := h.executor.BeginTxn(block.Header.StateRoot, block.Header, types.ZeroAddress)
	require.NoError(t, err)
	require.Empty(t, tran.GetCode(kAddr), "selfdestructed contract must have no code")

	// drop tx1's dependency on the funding tx: the sweep races the funding and must diverge
	h.requireCanDiverge(t, block, [][]uint64{{}, {}, {1}}, 100)
}

// ---------------------------------------------------------------------------------------------
//
//  11. Braided chains: three senders each send two txs (nonce chains), and those txs cross three
//     independent conflict chains (contract slot X, contract slot Y, EOA receiver R) - including one
//     via a Router CALL. The DAG is a braid of nonce ordering x state conflicts, so scheduling has
//     many wrong orders available.
//
//     S0: tx0 inc(X,100)          tx3 EOA->R 50
//     S1: tx1 routerInc(X,200)    tx4 inc(Y,10)
//     S2: tx2 EOA->R 70           tx5 transfer(X->Y,30)
//
// ---------------------------------------------------------------------------------------------
func TestParallel_SenderNonceBraid(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	s0, s1, s2 := parAddr(10), parAddr(11), parAddr(12)
	addrs := h.setupParent(t, fundAll(s0, s1, s2), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.BalancesInitHex)),
	})
	balAddr := addrs[0]

	router := h.deployMore(t, [][]byte{
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.RouterInitHex), balAddr),
	})[0]

	x, y, r := parAddr(201), parAddr(202), parAddr(120)

	txs := []*types.Transaction{
		sth.CallTx(0xA0, s0, balAddr, 0,
			sth.CallData("incBalance(address,uint256)", sth.ContractPaddAddress(x), sth.ContractPaddUint256(100))),
		sth.CallTx(0xA1, s1, router, 0,
			sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(x), sth.ContractPaddUint256(200))),
		eoaTransfer(0xA2, s2, r, 0, 70),
		eoaTransfer(0xA3, s0, r, 1, 50),
		sth.CallTx(0xA4, s1, balAddr, 1,
			sth.CallData("incBalance(address,uint256)", sth.ContractPaddAddress(y), sth.ContractPaddUint256(10))),
		sth.CallTx(0xA5, s2, balAddr, 1,
			sth.CallData("transfer(address,address,uint256)",
				sth.ContractPaddAddress(x), sth.ContractPaddAddress(y), sth.ContractPaddUint256(30))),
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 40)

	assertStorageUint(t, h, balAddr, sth.ScBalanceSlot(x, 1), 270) // 100+200-30
	assertStorageUint(t, h, balAddr, sth.ScBalanceSlot(y, 1), 40)  // 10+30
	assertBalance(t, h, r, 120)                                    // 70+50
}

// ---------------------------------------------------------------------------------------------
//  12. EIP-158 touched-empty accounts: a zero-value transfer to a nonexistent address creates a
//     touched empty account that the sequential path deletes at the tx boundary; a later transfer
//     resurrects it with a real balance; a second zero-value touch must then NOT delete it. This
//     targets the Empty()/Deleted handling in the verifier's PopulateBlockRadix merge.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_TouchedEmptyAccountResurrection(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12)}
	h.setupParent(t, fundAll(callers...), nil)

	empty := parAddr(222) // never funded

	txs := []*types.Transaction{
		eoaTransfer(0xB0, callers[0], empty, 0, 0), // touch: created empty, deleted at tx boundary
		eoaTransfer(0xB1, callers[1], empty, 0, 5), // resurrect with real balance
		eoaTransfer(0xB2, callers[2], empty, 0, 0), // touch again: now non-empty, must survive
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 50)

	assertBalance(t, h, empty, 5)
}

// ---------------------------------------------------------------------------------------------
//  13. Randomized block (seeded, reproducible): 40 txs mixing direct calls, Router CALLs, Proxy
//     DELEGATECALLs, in-contract transfers, EOA transfers and zero-value touches, from senders that
//     reuse nonces across the block. Explores conflict combinations the hand-written scenarios
//     don't; the state root is the oracle.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_RandomizedBlock(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	const (
		numSenders = 12
		numTxs     = 40
	)

	senders := make([]types.Address, numSenders)
	for i := range senders {
		senders[i] = parAddr(byte(10 + i))
	}

	addrs := h.setupParent(t, fundAll(senders...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.BalancesInitHex)),
	})
	balAddr := addrs[0]

	more := h.deployMore(t, [][]byte{
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.RouterInitHex), balAddr),
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.RouterInitHex), balAddr),
		sth.AppendCtorAddr(sth.MustDecodeHex(t, sth.ProxyInitHex), balAddr),
	})
	router1, router2, proxy := more[0], more[1], more[2]

	targets := make([]types.Address, 6)
	for i := range targets {
		targets[i] = parAddr(byte(200 + i))
	}

	receivers := make([]types.Address, 6)
	for i := range receivers {
		receivers[i] = parAddr(byte(100 + i))
	}

	rnd := rand.New(rand.NewSource(1337)) // deterministic test data
	nonces := map[types.Address]uint64{}

	nextSender := func() (types.Address, uint64) {
		s := senders[rnd.Intn(numSenders)]
		n := nonces[s]
		nonces[s]++

		return s, n
	}

	txs := make([]*types.Transaction, 0, numTxs)

	// seed targets[0] first so in-contract transfers below can never underflow
	s, n := nextSender()
	txs = append(txs, sth.CallTx(0xC0, s, balAddr, n,
		sth.CallData("incBalance(address,uint256)", sth.ContractPaddAddress(targets[0]), sth.ContractPaddUint256(50_000))))

	for i := 1; i < numTxs; i++ {
		s, n := nextSender()
		seed := byte(0xC0 + i)
		target := targets[rnd.Intn(len(targets))]
		amount := uint64(rnd.Intn(100) + 1)

		switch rnd.Intn(7) {
		case 0: // direct incBalance
			txs = append(txs, sth.CallTx(seed, s, balAddr, n,
				sth.CallData("incBalance(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(amount))))
		case 1: // incBalance through router1 (CALL)
			txs = append(txs, sth.CallTx(seed, s, router1, n,
				sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(amount))))
		case 2: // incBalance through router2 (CALL)
			txs = append(txs, sth.CallTx(seed, s, router2, n,
				sth.CallData("routerInc(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(amount))))
		case 3: // incBalance through proxy (DELEGATECALL - writes proxy's own storage)
			txs = append(txs, sth.CallTx(seed, s, proxy, n,
				sth.CallData("pinc(address,uint256)", sth.ContractPaddAddress(target), sth.ContractPaddUint256(amount))))
		case 4: // in-contract transfer from the seeded target (max 39*10 < 50_000, never underflows)
			txs = append(txs, sth.CallTx(seed, s, balAddr, n,
				sth.CallData("transfer(address,address,uint256)",
					sth.ContractPaddAddress(targets[0]), sth.ContractPaddAddress(target), sth.ContractPaddUint256(uint64(rnd.Intn(10)+1)))))
		case 5: // EOA transfer
			txs = append(txs, eoaTransfer(seed, s, receivers[rnd.Intn(len(receivers))], n, int64(amount)))
		case 6: // zero-value touch of a receiver (EIP-158 empty-account path)
			txs = append(txs, eoaTransfer(seed, s, receivers[rnd.Intn(len(receivers))], n, 0))
		}
	}

	block := h.build(t, txs)

	h.requireVerifies(t, block, 15)
}

// ---------------------------------------------------------------------------------------------
//  14. Metamorphic contract (SELFDESTRUCT + CREATE2 redeploy at the same address, in one block):
//     tx0 deploys Killable at C via Create2Factory, tx1 writes its storage, tx2 selfdestructs it,
//     tx3 redeploys at the SAME address C with the same salt (only legal because the account died
//     at tx2's boundary), tx4 writes the fresh instance. The verifier must reproduce the full
//     lifecycle: old storage wiped, same address, new storage. Racing the redeploy against the
//     suicide (or the first deploy) makes CREATE2 hit an address collision and revert, so a graph
//     without tx3's edges must diverge.
//
// ---------------------------------------------------------------------------------------------
func TestParallel_MetamorphicCreate2Redeploy(t *testing.T) {
	t.Parallel()

	h := newParHarness(t)

	callers := []types.Address{parAddr(10), parAddr(11), parAddr(12), parAddr(13), parAddr(14)}
	fAddrs := h.setupParent(t, fundAll(callers...), []*types.Transaction{
		sth.DeployTx(parDeployer, 0, sth.MustDecodeHex(t, sth.Create2FactoryInitHex)),
	})
	factoryAddr := fAddrs[0]

	salt := [32]byte{0x01}
	childAddr := crypto.CreateAddress2(factoryAddr, salt, sth.MustDecodeHex(t, sth.KillableInitHex))
	beneficiary := parAddr(150)

	txs := []*types.Transaction{
		sth.CallTx(0xD0, callers[0], factoryAddr, 0, sth.CallData("make2(bytes32)", salt[:])),
		sth.CallTx(0xD1, callers[1], childAddr, 0, sth.CallData("setX(uint256)", sth.ContractPaddUint256(42))),
		sth.CallTx(0xD2, callers[2], childAddr, 0, sth.CallData("boom(address)", sth.ContractPaddAddress(beneficiary))),
		sth.CallTx(0xD3, callers[3], factoryAddr, 0, sth.CallData("make2(bytes32)", salt[:])),
		sth.CallTx(0xD4, callers[4], childAddr, 0, sth.CallData("setX(uint256)", sth.ContractPaddUint256(7))),
	}

	block := h.build(t, txs)

	// every step must have succeeded in-order, or the scenario proves nothing
	for i, r := range h.lastBuildReceipts {
		require.Equal(t, uint64(types.ReceiptSuccess), uint64(*r.Status), "tx %d must succeed", i)
	}

	h.requireVerifies(t, block, 40)

	// the reincarnated contract lives at the same address with fresh storage: x == 7, not 42
	assertStorageUint(t, h, childAddr, types.ZeroHash, 7)

	tran, err := h.executor.BeginTxn(block.Header.StateRoot, block.Header, types.ZeroAddress)
	require.NoError(t, err)
	require.NotEmpty(t, tran.GetCode(childAddr), "redeployed child must have code")

	// unleash the redeploy: without tx3's edges CREATE2 races the suicide (collision -> revert)
	graph := createDeps(t, block)
	broken := make([][]uint64, len(graph))
	copy(broken, graph)
	broken[3] = nil

	h.requireCanDiverge(t, block, broken, 100)
}

// ---------------------------------------------------------------------------------------------
//  Test harness and helpers
// ---------------------------------------------------------------------------------------------

// parHarness wires the minimal backendIBFT needed to (1) build a block via the real proposer path,
// which derives the transaction dependency DAG from actual read/write sets and packs it into the
// header, and (2) re-execute that block through the parallel verifier and assert it reproduces the
// builder's committed state root. This mirrors production: the proposer computes the DAG, the
// verifier reads it back out of the header (see server.go) and runs ProcessBlock in parallel.
type parHarness struct {
	executor          *state.Executor
	buildBlockFn      func(parent *types.Header, txs []*types.Transaction) (*types.Block, []*types.Receipt, error)
	proposer          types.Address
	parentHeader      *types.Header
	deployerNonce     uint64
	lastBlock         *types.Block
	lastBuildReceipts []*types.Receipt
}

// newParHarness builds the harness: a single-proposer validator set, a two-header test chain, a
// fresh in-memory state executor, and a buildBlockFn that drives the real backendIBFT.buildBlock
// proposer path over a caller-supplied tx list (fed through a mocked txpool).
func newParHarness(t *testing.T) *parHarness {
	t.Helper()

	proposerKey, err := crypto.GenerateECDSAKey()
	require.NoError(t, err)

	mySigner := signer.NewSigner(
		signer.NewECDSAKeyManagerFromKey(proposerKey.PrivateKey()),
		signer.NewECDSAKeyManagerFromKey(proposerKey.PrivateKey()),
	)
	round := uint64(0)
	validatorsSet := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(proposerKey.Address()),
		validators.NewECDSAValidator(types.StringToAddress("1")),
	)
	parentExtraData := &signer.IstanbulExtra{
		Validators:           validatorsSet,
		ParentCommittedSeals: &signer.SerializedSeal{},
		CommittedSeals:       &signer.AggregatedSeal{},
		RoundNumber:          &round,
		TxDependency:         [][]uint64{{}},
	}

	forkManagerMock := &forkManagerMock{}
	forkManagerMock.On("GetValidators", mock.Anything).Return(validatorsSet)
	forkManagerMock.On("GetSigner", mock.Anything).Return(mySigner)
	forkManagerMock.On("GetHooks", mock.Anything).Return(&hook.Hooks{})

	chainParams := &chain.Params{
		ChainID:      100,
		Forks:        chain.AllForksEnabled,
		BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
	}

	parentHeader := &types.Header{
		Number:     2,
		Hash:       types.Hash{0, 1, 2, 3, 4, 5},
		ParentHash: types.Hash{1, 3},
		GasLimit:   1_000_000_000_000,
		ExtraData:  append(make([]byte, signer.IstanbulExtraVanity), parentExtraData.MarshalRLPTo(nil)...),
	}

	bc := blockchain.NewTestBlockchain(t, []*types.Header{
		{Number: 1, Hash: types.Hash{1, 3}}, parentHeader,
	})

	executor := state.NewExecutor(chainParams, itrie.NewState(itrie.NewMemoryStorage()), hclog.NewNullLogger())
	executor.GetHash = bc.GetHashHelper

	buildBlockFn := func(parent *types.Header, txs []*types.Transaction) (*types.Block, []*types.Receipt, error) {
		txPool := &txPoolMock{}
		txPool.On("Prepare", mock.Anything).Run(func(mock.Arguments) {})
		txPool.On("Length", mock.Anything).Return(uint64(0))

		for _, tx := range txs {
			txPool.On("Peek").Return(tx).Once()
			txPool.On("Pop", mock.Anything).Run(func(mock.Arguments) {}).Once()
		}

		txPool.On("Peek").Return((*types.Transaction)(nil)).Once()

		i := &backendIBFT{
			forkManager: forkManagerMock,
			blockchain:  bc,
			executor:    executor,
			logger:      hclog.NewNullLogger(),
			txpool:      txPool,
			// writeTransactions always waits out the full window, so blockTime is the fixed wall
			// cost of every build. It must still be long enough to execute the largest test block
			// under `-race` (5-10x slower), where 250ms was not enough for a 40-tx block.
			blockTime: 2 * time.Second,
			config: &consensus.Config{
				Params: chainParams,
			},
		}

		return i.buildBlock(parent)
	}

	return &parHarness{
		executor:     executor,
		buildBlockFn: buildBlockFn,
		proposer:     proposerKey.Address(),
		parentHeader: parentHeader,
	}
}

// setupParent funds the given accounts and applies the deploy txs directly against a fresh genesis
// state, then commits and pins the resulting root onto the parent header. Call once, first. Returns
// the created contract addresses in deploy order.
func (h *parHarness) setupParent(
	t *testing.T, balances map[types.Address]*big.Int, deploys []*types.Transaction,
) []types.Address {
	t.Helper()

	tran, err := h.executor.BeginTxn(types.ZeroHash, h.parentHeader, types.ZeroAddress)
	require.NoError(t, err)

	for addr, bal := range balances {
		require.NoError(t, tran.SetAccountDirectly(addr, &chain.GenesisAccount{Balance: bal, Nonce: 0}))
	}

	addrs := make([]types.Address, 0, len(deploys))

	for _, d := range deploys {
		res, err := tran.Apply(d)
		require.NoError(t, err)
		require.NoError(t, res.Err, "deploy tx must succeed")

		addrs = append(addrs, res.Address)
	}

	_, root, err := tran.Commit()
	require.NoError(t, err)

	h.parentHeader.StateRoot = root
	h.deployerNonce = uint64(len(deploys))

	return addrs
}

// deployMore applies additional deploys from parDeployer on top of the current parent state,
// continuing the deployer nonce, and returns the created addresses.
func (h *parHarness) deployMore(t *testing.T, initCodes [][]byte) []types.Address {
	t.Helper()

	tran, err := h.executor.BeginTxn(h.parentHeader.StateRoot, h.parentHeader, types.ZeroAddress)
	require.NoError(t, err)

	addrs := make([]types.Address, 0, len(initCodes))

	for _, code := range initCodes {
		res, err := tran.Apply(sth.DeployTx(parDeployer, h.deployerNonce, code))
		require.NoError(t, err)
		require.NoError(t, res.Err, "deploy tx must succeed")

		addrs = append(addrs, res.Address)
		h.deployerNonce++
	}

	_, root, err := tran.Commit()
	require.NoError(t, err)

	h.parentHeader.StateRoot = root

	return addrs
}

// build runs the proposer path over txs and returns the built block. It sanity-checks that every tx
// executed successfully so the scenario really exercises what it claims.
func (h *parHarness) build(t *testing.T, txs []*types.Transaction) *types.Block {
	t.Helper()

	block, receipts, err := h.buildBlockFn(h.parentHeader, txs)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.Len(t, receipts, len(txs), "every tx must have executed successfully in the proposer")
	require.Len(t, block.Transactions, len(txs))

	h.lastBlock = block
	h.lastBuildReceipts = receipts

	return block
}

// processBlockInParallel executes the block through the parallel verifier
func (h *parHarness) processBlockInParallel(t *testing.T, block *types.Block, graph [][]uint64) types.Hash {
	t.Helper()

	h.executor.GetTxDependencyHook = func(*types.Header) [][]uint64 { return graph }

	txn, _, err := h.executor.ProcessBlock(h.parentHeader.StateRoot, block, h.proposer)
	require.NoError(t, err)

	_, root, err := txn.Commit()
	require.NoError(t, err)

	return root
}

// processBlockInParallelReceipts is processBlockInParallel but also returns the receipts the
// parallel verifier produced, for equivalence checks against the builder's.
func (h *parHarness) processBlockInParallelReceipts(
	t *testing.T, block *types.Block, graph [][]uint64,
) (types.Hash, []*types.Receipt) {
	t.Helper()

	h.executor.GetTxDependencyHook = func(*types.Header) [][]uint64 { return graph }

	txn, receipts, err := h.executor.ProcessBlock(h.parentHeader.StateRoot, block, h.proposer)
	require.NoError(t, err)

	_, root, err := txn.Commit()
	require.NoError(t, err)

	return root, receipts
}

// requireVerifies asserts the parallel verifier reproduces the builder's state root over many
// iterations (so scheduling-dependent divergence has a chance to surface), using the DAG the
// proposer actually derived.
func (h *parHarness) requireVerifies(t *testing.T, block *types.Block, iterations int) {
	t.Helper()

	graph := createDeps(t, block)
	require.NotEmpty(t, graph, "proposer must derive a dependency graph (else the verifier runs sequentially)")

	for i := range iterations {
		require.Equal(t, block.Header.StateRoot, h.processBlockInParallel(t, block, graph),
			"iteration %d: parallel verification must reproduce the builder's state root", i)
	}
}

// requireVerifiesWith asserts the parallel verifier reproduces the builder's root using a caller
// supplied graph (e.g. an over-dense one).
func (h *parHarness) requireVerifiesWith(t *testing.T, block *types.Block, graph [][]uint64, iterations int) {
	t.Helper()

	for i := range iterations {
		require.Equal(t, block.Header.StateRoot, h.processBlockInParallel(t, block, graph),
			"iteration %d: parallel verification must reproduce the builder's state root", i)
	}
}

// requireCanDiverge asserts a deliberately-broken graph (missing a real conflict edge) makes the
// verifier nondeterministic: across many iterations at least one root differs from the builder's.
func (h *parHarness) requireCanDiverge(t *testing.T, block *types.Block, graph [][]uint64, iterations int) {
	t.Helper()

	diverged := false

	for i := 0; i < iterations && !diverged; i++ {
		if h.processBlockInParallel(t, block, graph) != block.Header.StateRoot {
			diverged = true
		}
	}

	require.True(t, diverged,
		"a graph missing a real conflict edge must be able to diverge from the builder's state root")
}

// createDeps parses the tx dependency DAG the proposer packed into the block header.
func createDeps(t *testing.T, block *types.Block) [][]uint64 {
	t.Helper()

	var ed signer.IstanbulExtra

	require.NoError(t, ed.UnmarshalRLPForTxDependecies(block.Header.ExtraData[signer.IstanbulExtraVanity:]))

	return ed.TxDependency
}

// assertStorageUint reads a storage slot from the built block's post-state and asserts its value.
func assertStorageUint(t *testing.T, h *parHarness, addr types.Address, slot types.Hash, want int64) {
	t.Helper()

	tran, err := h.executor.BeginTxn(h.lastBlock.Header.StateRoot, h.lastBlock.Header, types.ZeroAddress)
	require.NoError(t, err)

	got := new(big.Int).SetBytes(tran.GetStorage(addr, slot).Bytes())
	require.Equal(t, big.NewInt(want), got, "storage slot %s on %s", slot, addr)
}

// assertBalance reads an account balance from the built block's post-state and asserts its value.
func assertBalance(t *testing.T, h *parHarness, addr types.Address, want int64) {
	t.Helper()

	tran, err := h.executor.BeginTxn(h.lastBlock.Header.StateRoot, h.lastBlock.Header, types.ZeroAddress)
	require.NoError(t, err)

	require.Equal(t, big.NewInt(want), tran.GetBalance(addr), "balance of %s", addr)
}

// parDeployer is the account all test contracts are deployed from.
var parDeployer = types.Address{0xD0}

// parAddr builds a deterministic test address whose last byte is b.
func parAddr(b byte) types.Address {
	var a types.Address

	a[19] = b

	return a
}

// fundAll funds the deployer plus the given callers with 100 ether each.
func fundAll(callers ...types.Address) map[types.Address]*big.Int {
	m := map[types.Address]*big.Int{parDeployer: ethgo.Ether(100)}
	for _, c := range callers {
		m[c] = ethgo.Ether(100)
	}

	return m
}

// eoaTransfer builds a plain value-transfer tx with a hash derived from seed.
func eoaTransfer(seed byte, from, to types.Address, nonce uint64, value int64) *types.Transaction {
	dst := to

	return &types.Transaction{
		Hash: types.Hash{seed, 0xEE}, From: from, To: &dst, Value: big.NewInt(value),
		Gas: 21000, GasPrice: ethgo.Gwei(2), Nonce: nonce, Type: types.LegacyTx, Input: []byte{},
	}
}
