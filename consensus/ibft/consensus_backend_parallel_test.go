package ibft

import (
	"math/big"
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

// parHarness wires the minimal backendIBFT needed to (1) build a block via the real proposer path,
// which derives the transaction dependency DAG from actual read/write sets and packs it into the
// header, and (2) re-execute that block through the parallel verifier and assert it reproduces the
// builder's committed state root. This mirrors production: the proposer computes the DAG, the
// verifier reads it back out of the header (see server.go) and runs ProcessBlock in parallel.
type parHarness struct {
	executor      *state.Executor
	buildBlockFn  func(parent *types.Header, txs []*types.Transaction) (*types.Block, []*types.Receipt, error)
	proposer      types.Address
	parentHeader  *types.Header
	deployerNonce uint64
	lastBlock     *types.Block
}

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
			// short block time: txs are processed instantly, then writeTransactions waits out the
			// remaining window, so this bounds how long each build takes.
			blockTime: 250 * time.Millisecond,
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

	return block
}

// createDeps parses the tx dependency DAG the proposer packed into the block header.
func createDeps(t *testing.T, block *types.Block) [][]uint64 {
	t.Helper()

	var ed signer.IstanbulExtra

	require.NoError(t, ed.UnmarshalRLPForTxDependecies(block.Header.ExtraData[signer.IstanbulExtraVanity:]))

	return ed.TxDependency
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

// assertStorageUint reads a storage slot from the built block's post-state and asserts its value.
func assertStorageUint(t *testing.T, h *parHarness, addr types.Address, slot types.Hash, want int64) {
	t.Helper()

	tran, err := h.executor.BeginTxn(h.lastBlock.Header.StateRoot, h.lastBlock.Header, types.ZeroAddress)
	require.NoError(t, err)

	got := new(big.Int).SetBytes(tran.GetStorage(addr, slot).Bytes())
	require.Equal(t, big.NewInt(want), got, "storage slot %s on %s", slot, addr)
}

// ---------------------------------------------------------------------------------------------

var parDeployer = types.Address{0xD0}

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

func eoaTransfer(seed byte, from, to types.Address, nonce uint64, value int64) *types.Transaction {
	dst := to

	return &types.Transaction{
		Hash: types.Hash{seed, 0xEE}, From: from, To: &dst, Value: big.NewInt(value),
		Gas: 21000, GasPrice: ethgo.Gwei(2), Nonce: nonce, Type: types.LegacyTx, Input: []byte{},
	}
}

// ---------------------------------------------------------------------------------------------
// 1. Cross-contract CALL: Router.routerInc / routerTransfer forward via CALL into Balances, so the
//    dirtied account is Balances even though tx.To is a Router. Two txs through *different* routers
//    that mutate the same balances[] slot must be serialized by the derived graph. The proposer has
//    to attribute the conflict to the Balances account, not the Router that was called.
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
// 2. DELEGATECALL vs CALL storage attribution. Proxy.pinc DELEGATECALLs Balances.incBalance, so the
//    write lands in the *Proxy's own* storage; Router.routerInc CALLs, so its write lands in the
//    Balances contract. The proposer must attribute proxy writes to the proxy account and router
//    writes to the balances account.
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
// 3. Deep chain A -> B -> C: ATop.bump writes A.v then calls BMid.bump (writes B.v) which calls
//    CLeaf.bump (writes C.v). A single tx dirties three accounts, and the proposer must serialize
//    any two txs overlapping on *any* of them.
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
// 4. CREATE dependency: tx0 calls Factory.make(), which CREATEs a fresh Balances child; later txs
//    call that child. The child does not exist at parent state, so its callers have a hard
//    dependency on tx0's flush. Exercises the proposer + verifier for within-block contract creation.
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
// 5. SELFDESTRUCT + re-touch: tx0 writes storage on Killable, tx1 selfdestructs it, tx2 sends value
//    to the (now empty) address. All three touch the same account and must be serialized.
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
// 6. Graph-shape stress via EOA transfers: wide fan-out/fan-in, long serial chain, and a diamond.
//    The proposer derives the graph from the conflict pattern; the verifier must reproduce the root
//    across many worker interleavings.
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
// 7. Vary worker count: the same block and derived graph must yield the builder's root at every
//    worker count, since merge bugs often only appear at a specific level of parallelism.
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
// 8. Concurrent block-creator credit: n independent transfers (no conflicts between txs) all credit
//    the same block creator's balance via the special AddBalanceDoNotTrack path, with no dependency
//    edges between them. That path is supposed to be safe without serialization; verify it stays
//    consistent under parallel execution.
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
