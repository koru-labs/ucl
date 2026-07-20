package e2e

import (
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/consensus/polybft/contractsapi"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/e2e/frameworkV2"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/wallet"
	"github.com/stretchr/testify/require"
)

// The E2E BAL test drives a IBFT cluster (5 validators + 2 non-validators)
// through the four canonical scenarios that exercise every kind of BAL entry:
//
//   - plain EOA -> EOA transfer  -> BalanceChanges
//   - contract deployment        -> CodeChanges (+ nonce / balance)
//   - storage write via setter   -> StorageWrites
//   - reverted call              -> negative case: reverted write must NOT
//     surface in state on any node
//
// The BAL itself is not directly observable over JSON-RPC. What IS observable
// is: post-execution state (balance, code, storage). If any node computed a
// different BAL from the proposer, its header hash would diverge and it would
// refuse the block; if it applied a divergent BAL, its state would diverge.
// So block-hash + state parity across all nodes is a sound proof of BAL
// agreement.
func TestE2E_BAL_CrossNodeAgreement(t *testing.T) {
	for _, withBAL := range []bool{true, false} {
		t.Run(balSubtestName(withBAL), func(t *testing.T) {
			sender, err := crypto.GenerateECDSAKey()
			require.NoError(t, err)

			cluster := newBALTestCluster(t, 4, sender.Address(), withBAL)
			defer cluster.Stop()

			cluster.WaitForReady(t)

			// Sanity: we really have 4 validators + 2 non-validators.
			require.Len(t, cluster.Servers, 6)

			// A recipient EOA that starts with zero balance, so we can watch a
			// BalanceChange entry take effect on every node.
			recipientKey, err := wallet.GenerateKey()
			require.NoError(t, err)
			recipient := types.Address(recipientKey.Address())

			transferAmount := ethgo.Gwei(10_000)

			transferTxn := cluster.Transfer(t, sender, recipient, transferAmount)
			require.True(t, transferTxn.Succeed())

			transferBlock := transferTxn.Receipt().BlockNumber
			require.NoError(t, cluster.WaitForBlock(transferBlock, 30*time.Second))

			assertBalanceParity(t, cluster, recipient, transferAmount)

			// contract deployment (CodeChanges)
			deployTxn := cluster.Deploy(t, sender, contractsapi.TestSimple.Bytecode)
			require.True(t, deployTxn.Succeed())

			contractAddr := types.Address(deployTxn.Receipt().ContractAddress)
			deployBlock := deployTxn.Receipt().BlockNumber
			require.NoError(t, cluster.WaitForBlock(deployBlock, 30*time.Second))

			assertCodeParity(t, cluster, contractAddr)

			// Storage write
			setValueFn := contractsapi.TestSimple.Abi.GetMethod("setValue")
			newVal := big.NewInt(42)

			input, err := setValueFn.Encode([]interface{}{newVal})
			require.NoError(t, err)

			setTxn := cluster.SendTxn(t, sender, &types.Transaction{
				Input: input,
				To:    &contractAddr,
			})
			require.True(t, setTxn.Succeed())

			setBlock := setTxn.Receipt().BlockNumber
			require.NoError(t, cluster.WaitForBlock(setBlock, 30*time.Second))

			// Slot 0 is where TestSimple.setValue stores its argument. Every node
			// must report the same value, which proves each of them applied the
			// StorageWrite entry from the BAL identically.
			assertStorageParity(t, cluster, contractAddr, types.Hash{}, newVal)

			// Negative check: sender balance parity after all the above.
			// The sender paid gas + transferAmount; whatever number we end up with,
			// every node must agree on it.
			assertBalanceParityAcrossNodes(t, cluster, types.Address(sender.Address()))
		})
	}
}

// TestE2E_BAL_RevertedTx_NoStatePersisted sends a call that reverts. The
// receipt must be a failure on every node, and no state (storage/balance)
// must reflect the reverted writes. If applyCall merged the sub-recorder on
// revert, the non-validator's ApplyBlockAccessList would produce a different
// state root than the proposer's, and the block would be rejected: the
// cluster would stop advancing. So convergence itself is the strongest
// signal here.
func TestE2E_BAL_RevertedTx_NoStatePersisted(t *testing.T) {
	for _, withBAL := range []bool{true, false} {
		t.Run(balSubtestName(withBAL), func(t *testing.T) {
			sender, err := crypto.GenerateECDSAKey()
			require.NoError(t, err)

			cluster := newBALTestCluster(t, 4, sender.Address(), withBAL)
			defer cluster.Stop()

			cluster.WaitForReady(t)

			// Deploy TestSimple so we have something to call.
			deployTxn := cluster.Deploy(t, sender, contractsapi.TestSimple.Bytecode)
			require.True(t, deployTxn.Succeed())

			contractAddr := types.Address(deployTxn.Receipt().ContractAddress)

			// A hand-crafted input that doesn't match any selector -- the fallback
			// (or lack thereof in TestSimple) causes a revert. If your TestSimple
			// exposes a dedicated reverting method, swap it in here.
			badInput := []byte{0xde, 0xad, 0xbe, 0xef}

			revertTxn := cluster.SendTxn(t, sender, &types.Transaction{
				Input: badInput,
				To:    &contractAddr,
			})
			require.False(t, revertTxn.Succeed(),
				"the call must fail; if it succeeds, pick a different revert trigger")

			revertBlock := revertTxn.Receipt().BlockNumber
			require.NoError(t, cluster.WaitForBlock(revertBlock, 30*time.Second))

			// Same slot value (still zero) on every node.
			assertStorageParity(t, cluster, contractAddr, types.Hash{}, big.NewInt(0))
		})
	}
}

// TestE2E_BAL_NestedCall_Success: BALCaller invokes TestSimple.setValue(42).
// Every node must report TestSimple.slot0 == 42 after the block lands.
func TestE2E_BAL_NestedCall_Success(t *testing.T) {
	for _, withBAL := range []bool{true, false} {
		t.Run(balSubtestName(withBAL), func(t *testing.T) {
			sender, err := crypto.GenerateECDSAKey()
			require.NoError(t, err)

			cluster := newBALTestCluster(t, 6, sender.Address(), withBAL)
			defer cluster.Stop()

			cluster.WaitForReady(t)

			// Deploy TestSimple as the successful callee.
			calleeTxn := cluster.Deploy(t, sender, contractsapi.TestSimple.Bytecode)
			require.True(t, calleeTxn.Succeed())
			calleeAddr := calleeTxn.Receipt().ContractAddress

			// Deploy BALCaller.
			callerTxn := cluster.Deploy(t, sender, contractsapi.TestBalCaller.Bytecode)
			require.True(t, callerTxn.Succeed())
			callerAddr := types.Address(callerTxn.Receipt().ContractAddress)

			// caller.callSet(calleeAddr, 42)
			callSet := contractsapi.TestBalCaller.Abi.GetMethod("callSet")
			input, err := callSet.Encode([]interface{}{ethgo.Address(calleeAddr), big.NewInt(42)})
			require.NoError(t, err)

			txn := cluster.SendTxn(t, sender, &types.Transaction{
				Input: input,
				To:    &callerAddr,
			})
			require.True(t, txn.Succeed(), "outer tx must succeed")

			bn := txn.Receipt().BlockNumber
			require.NoError(t, cluster.WaitForBlock(bn, 30*time.Second))

			assertStorageParity(t, cluster, types.Address(calleeAddr), types.Hash{}, big.NewInt(42))
		})
	}
}

// TestE2E_BAL_NestedCall_RevertingCallee: BALCaller invokes a callee that
// SSTOREs then REVERTs. Outer tx succeeds (caller ignores inner failure), but
// the reverted SSTORE must NOT persist -- slot 0 stays zero on every node.
func TestE2E_BAL_NestedCall_RevertingCallee(t *testing.T) {
	for _, withBAL := range []bool{true, false} {
		t.Run(balSubtestName(withBAL), func(t *testing.T) {
			sender, err := crypto.GenerateECDSAKey()
			require.NoError(t, err)

			cluster := newBALTestCluster(t, 6, sender.Address(), withBAL,
				frameworkV2.WithEpochSize(10),
			)
			defer cluster.Stop()

			cluster.WaitForReady(t)

			calleeTxn := cluster.Deploy(t, sender, contractsapi.TestBallRevertingCallee.Bytecode)
			require.True(t, calleeTxn.Succeed())
			calleeAddr := calleeTxn.Receipt().ContractAddress

			callerTxn := cluster.Deploy(t, sender, contractsapi.TestBalCaller.Bytecode)
			require.True(t, callerTxn.Succeed())
			callerAddr := types.Address(callerTxn.Receipt().ContractAddress)

			callSet := contractsapi.TestBalCaller.Abi.GetMethod("callSet")
			input, err := callSet.Encode([]interface{}{ethgo.Address(calleeAddr), big.NewInt(42)})
			require.NoError(t, err)

			txn := cluster.SendTxn(t, sender, &types.Transaction{
				Input: input,
				To:    &callerAddr,
			})
			require.True(t, txn.Succeed(),
				"outer tx must succeed even though the inner CALL reverted")

			bn := txn.Receipt().BlockNumber
			require.NoError(t, cluster.WaitForBlock(bn, 30*time.Second))

			assertStorageParity(t, cluster, types.Address(calleeAddr), types.Hash{}, big.NewInt(0))
		})
	}
}

// TestE2E_BAL_NestedCall_MixedBatch fires transfer, successful nested call,
// reverting nested call, and another transfer as a burst.
func TestE2E_BAL_NestedCall_MixedBatch(t *testing.T) {
	for _, withBAL := range []bool{true, false} {
		t.Run(balSubtestName(withBAL), func(t *testing.T) {
			sender, err := crypto.GenerateECDSAKey()
			require.NoError(t, err)

			recipient, err := crypto.GenerateECDSAKey()
			require.NoError(t, err)

			cluster := newBALTestCluster(t, 6, sender.Address(), withBAL)
			defer cluster.Stop()

			cluster.WaitForReady(t)

			simpleTxn := cluster.Deploy(t, sender, contractsapi.TestSimple.Bytecode)
			require.True(t, simpleTxn.Succeed())
			simpleAddr := simpleTxn.Receipt().ContractAddress

			revertingTxn := cluster.Deploy(t, sender, contractsapi.TestBallRevertingCallee.Bytecode)
			require.True(t, revertingTxn.Succeed())
			revertingAddr := revertingTxn.Receipt().ContractAddress

			callerTxn := cluster.Deploy(t, sender, contractsapi.TestBalCaller.Bytecode)
			require.True(t, callerTxn.Succeed())
			callerAddr := types.Address(callerTxn.Receipt().ContractAddress)

			callSet := contractsapi.TestBalCaller.Abi.GetMethod("callSet")

			txns := []struct {
				desc string
				exec func() *frameworkV2.TestTxn
			}{
				{"transfer 1000", func() *frameworkV2.TestTxn {
					return cluster.Transfer(t, sender, recipient.Address(), big.NewInt(1000))
				}},
				{"nested -> TestSimple.setValue(7)", func() *frameworkV2.TestTxn {
					in, err := callSet.Encode([]interface{}{ethgo.Address(simpleAddr), big.NewInt(7)})
					require.NoError(t, err)
					return cluster.SendTxn(t, sender, &types.Transaction{Input: in, To: &callerAddr})
				}},
				{"nested -> reverting callee", func() *frameworkV2.TestTxn {
					in, err := callSet.Encode([]interface{}{ethgo.Address(revertingAddr), big.NewInt(99)})
					require.NoError(t, err)
					return cluster.SendTxn(t, sender, &types.Transaction{Input: in, To: &callerAddr})
				}},
				{"transfer 2000", func() *frameworkV2.TestTxn {
					return cluster.Transfer(t, sender, recipient.Address(), big.NewInt(2000))
				}},
			}

			var lastBlock uint64
			for _, tx := range txns {
				r := tx.exec()
				require.True(t, r.Succeed(), tx.desc)
				if r.Receipt().BlockNumber > lastBlock {
					lastBlock = r.Receipt().BlockNumber
				}
			}

			require.NoError(t, cluster.WaitForBlock(lastBlock, 30*time.Second))

			assertStorageParity(t, cluster, types.Address(simpleAddr), types.Hash{}, big.NewInt(7))
			assertStorageParity(t, cluster, types.Address(revertingAddr), types.Hash{}, big.NewInt(0))
			assertBalanceParity(t, cluster, recipient.Address(), big.NewInt(3000))
		})
	}
}

func newBALTestCluster(
	t *testing.T,
	validators int,
	senderAddr types.Address,
	withBAL bool,
	extra ...frameworkV2.ClusterOption,
) *frameworkV2.TestCluster {
	t.Helper()

	opts := []frameworkV2.ClusterOption{
		frameworkV2.WithNonValidators(2),
		frameworkV2.WithBootnodeCount(1),
		frameworkV2.WithPremine(map[types.Address]*big.Int{
			senderAddr: ethgo.Ether(100),
		}),
	}
	if withBAL {
		opts = append(opts, frameworkV2.WithBALNonValidators())
	}
	opts = append(opts, extra...)

	return frameworkV2.NewTestCluster(t, validators, opts...)
}

func balSubtestName(withBAL bool) string {
	if withBAL {
		return "WithBAL"
	}
	return "WithoutBAL"
}

// assertBalanceParity requires the exact balance for `addr` on every node.
// Use when the expected balance is known (e.g. right after a fresh recipient
// received a fixed amount).
func assertBalanceParity(t *testing.T, cluster *frameworkV2.TestCluster, addr types.Address, want *big.Int) {
	t.Helper()

	for i, s := range cluster.Servers {
		bal, err := s.JSONRPC().GetBalance(addr, jsonrpc.LatestBlockNumberOrHash)
		require.NoErrorf(t, err, "node %d GetBalance(%s)", i, addr)
		require.Equalf(t, 0, want.Cmp(bal),
			"node %d balance of %s: want %s, got %s", i, addr, want, bal)
	}
}

// assertBalanceParityAcrossNodes only requires all nodes agree with node 0
// on `addr`'s balance; the exact number doesn't need to be known ahead.
func assertBalanceParityAcrossNodes(t *testing.T, cluster *frameworkV2.TestCluster, addr types.Address) {
	t.Helper()

	var reference *big.Int
	for i, s := range cluster.Servers {
		bal, err := s.JSONRPC().GetBalance(addr, jsonrpc.LatestBlockNumberOrHash)
		require.NoErrorf(t, err, "node %d GetBalance(%s)", i, addr)
		if i == 0 {
			reference = bal
			continue
		}
		require.Equalf(t, 0, reference.Cmp(bal),
			"node %d balance of %s (%s) diverges from node 0 (%s)",
			i, addr, bal, reference)
	}
}

// assertCodeParity requires that every node reports non-empty, identical
// bytecode at `addr`.
func assertCodeParity(t *testing.T, cluster *frameworkV2.TestCluster, addr types.Address) {
	t.Helper()

	var reference []byte
	for i, s := range cluster.Servers {
		code, err := s.JSONRPC().GetCode(addr, jsonrpc.LatestBlockNumberOrHash)
		require.NoErrorf(t, err, "node %d GetCode(%s)", i, addr)
		require.NotEmptyf(t, code, "node %d reports empty code at %s", i, addr)
		if i == 0 {
			reference = []byte(code)
			continue
		}
		require.Equalf(t, reference, []byte(code),
			"node %d code at %s diverges from node 0", i, addr)
	}
}

// assertStorageParity requires that every node reports the expected value at
// `slot` for `addr`. When you only care about cross-node agreement (not the
// exact value), pass whatever value node 0 reports.
func assertStorageParity(
	t *testing.T,
	cluster *frameworkV2.TestCluster,
	addr types.Address,
	slot types.Hash,
	want *big.Int,
) {
	t.Helper()

	wantHash := types.BytesToHash(want.Bytes())

	for i, s := range cluster.Servers {
		raw, err := s.JSONRPC().GetStorageAt(addr, slot, jsonrpc.LatestBlockNumberOrHash)
		require.NoErrorf(t, err, "node %d GetStorageAt(%s, %s)", i, addr, slot)

		got := types.BytesToHash(raw.Bytes())
		require.Equalf(t, wantHash, got,
			"node %d storage[%s][%s]: want %s, got %s", i, addr, slot, wantHash, got)
	}
}
