package e2e

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/e2e/framework"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/stretchr/testify/require"
)

const (
	ibftTwoValidators   = 2
	ibftFiveValidators  = 5
	ibftSixValidators   = 6
	ibftSevenValidators = 7

	// settleAfterStop gives any block that was already mid-commit time to finalize (or fail)
	// before we sample the "stalled" reference height. Sampling immediately after stopping a
	// peer is racy: an in-flight block can seal in the gap and make a strict height comparison
	// flap.
	settleAfterStop = 5 * time.Second

	// settleAfterRestart lets libp2p and IBFT reconnect after a process restart before we start
	// polling for recovery.
	settleAfterRestart = 3 * time.Second
)

// bootIBFTCluster creates and starts an n-validator IBFT cluster of the given validator type and
// returns the started servers. It fails the test if startup does not complete within startTimeout.
func bootIBFTCluster(
	t *testing.T,
	count int,
	dirPrefix string,
	validatorType validators.ValidatorType,
	startTimeout time.Duration,
) []*framework.TestServer {
	t.Helper()

	ibftManager := framework.NewIBFTServersManager(t, count, dirPrefix,
		func(_ int, config *framework.TestServerConfig) {
			config.SetValidatorType(validatorType)
		})

	startCtx, startCancel := context.WithTimeout(context.Background(), startTimeout)
	defer startCancel()

	ibftManager.StartServers(startCtx)

	servers := make([]*framework.TestServer, count)
	for i := 0; i < count; i++ {
		servers[i] = ibftManager.GetServer(i)
	}

	return servers
}

// assertChainStalled samples the committed head twice — once after a short settle window and again
// after observe — and asserts the two samples are equal, i.e. the chain is not advancing. It
// returns the stalled height. Both samples are taken after the peer(s) have already been stopped so
// there is no stop-boundary race. This only inspects the committed head; internal IBFT round state
// is not exposed through the operator API and is therefore not asserted here.
func assertChainStalled(t *testing.T, live *framework.TestServer, observe time.Duration, msg string) uint64 {
	t.Helper()

	time.Sleep(settleAfterStop)

	before, err := live.GetLatestBlockHeight()
	require.NoError(t, err)

	time.Sleep(observe)

	after, err := live.GetLatestBlockHeight()
	require.NoError(t, err)

	require.Equal(t, before, after, msg)

	return after
}

// waitAllReach waits, in parallel, until every server reaches target height, failing the test if
// any server does not get there within timeout.
func waitAllReach(
	t *testing.T,
	servers []*framework.TestServer,
	target uint64,
	timeout time.Duration,
	failMsg string,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var wg sync.WaitGroup

	errs := make([]error, len(servers))

	for i := range servers {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			_, errs[idx] = framework.WaitUntilBlockMined(ctx, servers[idx], target)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "%s: server %d did not reach block %d", failMsg, i, target)
	}
}

// twoValStallCase parameterises the family of 2-validator "one down → chain stalls → restart →
// recovers" scenarios, which differ only in how the peer is stopped and how long it stays down.
type twoValStallCase struct {
	dirPrefix        string
	stop             func(*framework.TestServer)
	stallObservation time.Duration
	recoverWait      time.Duration
}

// runTwoValidatorStallThenRecover boots a 2-validator IBFT cluster (quorum = 2), stops one peer via
// the case's stop function, asserts the committed head does not advance for the observation window,
// then restarts the peer and asserts both nodes seal further blocks. With only two validators, a
// single fault removes quorum, so the chain MUST halt while one node is down and MUST resume once
// both are back.
func runTwoValidatorStallThenRecover(t *testing.T, validatorType validators.ValidatorType, c twoValStallCase) {
	t.Helper()

	const (
		initialSealHeight uint64 = 12
		startTimeout             = 2 * time.Minute
		recoverBlocks     uint64 = 4
	)

	servers := bootIBFTCluster(t, ibftTwoValidators, c.dirPrefix, validatorType, startTimeout)

	if errs := framework.WaitForServersToSeal(servers, initialSealHeight); len(errs) != 0 {
		t.Fatalf("initial seal (both validators up): %v", errs)
	}

	live := servers[1]

	c.stop(servers[0])

	stalledHeight := assertChainStalled(t, live, c.stallObservation,
		"with 2 validators IBFT needs both peers to finalize; committed head must not move while one is down")

	restartCtx, restartCancel := context.WithTimeout(context.Background(), startTimeout)
	defer restartCancel()

	require.NoError(t, servers[0].Start(restartCtx))

	time.Sleep(settleAfterRestart)

	target := stalledHeight + recoverBlocks
	waitAllReach(t, servers, target, c.recoverWait, "recovery after single-validator restart")
}

// TestIBFT_TwoValidators_OneDownStallsChain covers a 2-validator IBFT set (quorum is 2).
// With one validator gracefully stopped, the remaining node may still run and serve RPC, but the
// committed head must not advance. If blocks keep sealing, that matches the faulty behaviour
// observed in production (apparent liveness without quorum). After restart the chain must recover.
func TestIBFT_TwoValidators_OneDownStallsChain(t *testing.T) {
	run := func(t *testing.T, validatorType validators.ValidatorType) {
		runTwoValidatorStallThenRecover(t, validatorType, twoValStallCase{
			dirPrefix:        "e2e-ibft-2val-",
			stop:             func(s *framework.TestServer) { s.Stop() },
			stallObservation: 45 * time.Second,
			recoverWait:      3 * time.Minute,
		})
	}

	t.Run("ECDSA", func(t *testing.T) {
		run(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		run(t, validators.BLSValidatorType)
	})
}

// TestIBFT_TwoValidators_OneDownKill9StallsChainThenRecovers matches
// TestIBFT_TwoValidators_OneDownStallsChain, but the stopped validator is terminated with
// `kill -9 <pid>` (framework.TestServer.StopViaKill9) — an unclean crash rather than a graceful
// shutdown — then started again. Recovery behaviour should match the graceful-stop test.
func TestIBFT_TwoValidators_OneDownKill9StallsChainThenRecovers(t *testing.T) {
	run := func(t *testing.T, validatorType validators.ValidatorType) {
		runTwoValidatorStallThenRecover(t, validatorType, twoValStallCase{
			dirPrefix:        "e2e-ibft-2val-kill9-",
			stop:             func(s *framework.TestServer) { s.StopViaKill9() },
			stallObservation: 45 * time.Second,
			recoverWait:      3 * time.Minute,
		})
	}

	t.Run("ECDSA", func(t *testing.T) {
		run(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		run(t, validators.BLSValidatorType)
	})
}

// TestIBFT_TwoValidators_LongPartitionThenRecovers extends TestIBFT_TwoValidators_OneDownStallsChain
// by keeping one validator down for much longer, so the surviving peer spends an extended period
// unable to finalize the pending height. After restart, both validators must seal further blocks
// without requiring a simultaneous restart of the whole cluster — if they do not, IBFT recovery
// after a prolonged partition is broken for the minimal 2-validator configuration.
func TestIBFT_TwoValidators_LongPartitionThenRecovers(t *testing.T) {
	run := func(t *testing.T, validatorType validators.ValidatorType) {
		runTwoValidatorStallThenRecover(t, validatorType, twoValStallCase{
			dirPrefix:        "e2e-ibft-2val-longpart-",
			stop:             func(s *framework.TestServer) { s.Stop() },
			stallObservation: 90 * time.Second,
			recoverWait:      5 * time.Minute,
		})
	}

	t.Run("ECDSA", func(t *testing.T) {
		run(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		run(t, validators.BLSValidatorType)
	})
}

// TestIBFT_MinorityValidatorRestartLiveness stops one of four validators (quorum remains),
// lets the other three advance the chain for several blocks plus a wall-clock window, then restarts
// the stopped node and asserts all four eventually reach the same height again.
//
// If the restarted node fails to rejoin consensus, this test fails: the live subset keeps growing
// while the restarted node never reaches the common target.
func TestIBFT_MinorityValidatorRestartLiveness(t *testing.T) {
	const (
		initialSealHeight  uint64 = 15
		blocksWhileDown    uint64 = 8
		blocksAfterRestart uint64 = 5
		partitionWallTime         = 35 * time.Second
		startTimeout              = 2 * time.Minute
		recoverWait               = 3 * time.Minute
	)

	run := func(t *testing.T, validatorType validators.ValidatorType) {
		t.Helper()

		servers := bootIBFTCluster(t, IBFTMinNodes, IBFTDirPrefix, validatorType, startTimeout)

		if errs := framework.WaitForServersToSeal(servers, initialSealHeight); len(errs) != 0 {
			t.Fatalf("initial seal: %v", errs)
		}

		down := servers[0]
		live := servers[1:]

		heightBefore, err := live[0].GetLatestBlockHeight()
		require.NoError(t, err)

		down.Stop()

		targetWhileDown := heightBefore + blocksWhileDown
		if errs := framework.WaitForServersToSeal(live, targetWhileDown); len(errs) != 0 {
			t.Fatalf("live validators should advance while one is down (quorum retained): %v", errs)
		}

		time.Sleep(partitionWallTime)

		restartCtx, restartCancel := context.WithTimeout(context.Background(), startTimeout)
		defer restartCancel()

		require.NoError(t, down.Start(restartCtx))

		heightAfterRestart, err := live[0].GetLatestBlockHeight()
		require.NoError(t, err)

		finalTarget := heightAfterRestart + blocksAfterRestart
		waitAllReach(t, servers, finalTarget, recoverWait, "cluster should recover after minority restart")
	}

	t.Run("ECDSA", func(t *testing.T) {
		run(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		run(t, validators.BLSValidatorType)
	})
}

// TestIBFT_SixValidators_OneDownMajorityStillSeals checks that with six validators (quorum 4),
// stopping a single validator leaves a five-node majority that can still finalize new blocks.
// This is not an edge case: one fault is within IBFT fault tolerance f=floor((n-1)/3)=1 for n=6.
func TestIBFT_SixValidators_OneDownMajorityStillSeals(t *testing.T) {
	const (
		initialSealHeight uint64 = 14
		blocksWithOneDown uint64 = 12
		startTimeout             = 3 * time.Minute
	)

	run := func(t *testing.T, validatorType validators.ValidatorType) {
		t.Helper()

		servers := bootIBFTCluster(t, ibftSixValidators, "e2e-ibft-6val-", validatorType, startTimeout)

		if errs := framework.WaitForServersToSeal(servers, initialSealHeight); len(errs) != 0 {
			t.Fatalf("initial seal (six validators): %v", errs)
		}

		heightBefore, err := servers[1].GetLatestBlockHeight()
		require.NoError(t, err)

		servers[0].Stop()

		live := servers[1:]
		target := heightBefore + blocksWithOneDown
		if errs := framework.WaitForServersToSeal(live, target); len(errs) != 0 {
			t.Fatalf("with 6 validators and 1 stopped, remaining 5 should still seal (quorum=4): %v", errs)
		}
	}

	t.Run("ECDSA", func(t *testing.T) {
		run(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		run(t, validators.BLSValidatorType)
	})
}

// TestIBFT_SuperminorityPartition_RecoversAfterQuorumRestored stops more than n/3 validators so the
// remainder lacks quorum: the committed head must not move while the partition holds. After a
// wall-clock window the stopped validators are restarted, full quorum returns, and this test
// asserts every validator observes further sealed blocks (allowing several minutes for the first
// post-partition block).
//
// Layout: 7 validators ⇒ quorum = floor(2n/3)+1 = 5. Stopping 3 leaves 4 (< quorum); 3/7 > 1/3.
func TestIBFT_SuperminorityPartition_RecoversAfterQuorumRestored(t *testing.T) {
	const (
		initialSealHeight     uint64 = 14
		blocksAfterQuorumBack uint64 = 5
		stopCount                    = 3
		partitionWallTime            = 40 * time.Second
		startTimeout                 = 4 * time.Minute
		recoverWait                  = 4 * time.Minute
	)

	run := func(t *testing.T, validatorType validators.ValidatorType) {
		t.Helper()

		servers := bootIBFTCluster(t, ibftSevenValidators, "e2e-ibft-7val-part-", validatorType, startTimeout)

		if errs := framework.WaitForServersToSeal(servers, initialSealHeight); len(errs) != 0 {
			t.Fatalf("initial seal (seven validators): %v", errs)
		}

		// Stop strictly more than n/3 validators (3 > 7/3); remaining 4 cannot form quorum (5).
		for i := 0; i < stopCount; i++ {
			servers[i].Stop()
		}

		// servers[3] is one of the surviving (but sub-quorum) validators.
		stalledHeight := assertChainStalled(t, servers[3], partitionWallTime,
			"without quorum the committed head must not advance")

		restartCtx, restartCancel := context.WithTimeout(context.Background(), startTimeout)
		defer restartCancel()

		for i := 0; i < stopCount; i++ {
			require.NoError(t, servers[i].Start(restartCtx))
		}

		time.Sleep(settleAfterRestart)

		finalTarget := stalledHeight + blocksAfterQuorumBack
		waitAllReach(t, servers, finalTarget, recoverWait, "recovery after quorum is restored")
	}

	t.Run("ECDSA", func(t *testing.T) {
		run(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		run(t, validators.BLSValidatorType)
	})
}

// TestIBFT_DuplicateValidatorKey_ChainRemainsHealthy starts a 5-validator IBFT cluster in which
// validator[4]'s signing key is replaced with validator[0]'s key before any node is started.
//
// # Background — Double Signing
//
// "Double signing" occurs when two (or more) independent nodes submit IBFT consensus messages
// (PREPARE / COMMIT) for the same sequence using the same validator identity (address / key
// pair). In a correctly-implemented BFT protocol this is treated as Byzantine behaviour: every
// other validator that receives a second message from the same address for the same sequence
// MUST discard it (only one vote per validator per round is valid). The malicious / misconfigured
// node can therefore do no useful work and its extra votes are simply ignored.
//
// # Why the chain should stay healthy
//
// With n=5 validators the quorum threshold is floor(2*5/3)+1 = 4. After the key copy:
//
//   - genesis validator set: [addr0, addr1, addr2, addr3, addr4_original]
//   - nodes 0 and 4 both sign as addr0
//   - nobody signs as addr4_original
//
// Four unique validator addresses (addr0, addr1, addr2, addr3) provide valid, distinct votes.
// That is exactly the quorum required, so every round can be completed and the chain must keep
// producing blocks. The redundant messages from node[4] are silently dropped by peers.
//
// The test asserts:
//  1. The chain continues to seal blocks (no halt due to double-signing).
//  2. No validator node process crashes or becomes unreachable.
func TestIBFT_DuplicateValidatorKey_ChainRemainsHealthy(t *testing.T) {
	const (
		initialSealHeight uint64 = 12
		blocksAfterDup    uint64 = 10
		startTimeout             = 3 * time.Minute
		recoverWait              = 3 * time.Minute
	)

	run := func(t *testing.T, validatorType validators.ValidatorType) {
		t.Helper()

		ibftManager := framework.NewIBFTServersManager(
			t,
			ibftFiveValidators,
			"e2e-ibft-5val-dupkey-",
			func(_ int, config *framework.TestServerConfig) {
				config.SetValidatorType(validatorType)
			},
		)

		// ── Inject duplicate key ──────────────────────────────────────────────────────────
		// After NewIBFTServersManager has initialised all secrets and written genesis.json,
		// overwrite node[4]'s validator signing key(s) with node[0]'s.  Both nodes will
		// subsequently sign IBFT messages as addr0 — a double-signing scenario.
		node0DataDir := ibftManager.GetServer(0).Config.DataDir()
		node4DataDir := ibftManager.GetServer(4).Config.DataDir()

		copyKey := func(filename string) {
			t.Helper()

			src := filepath.Join(node0DataDir, secrets.ConsensusFolderLocal, filename)
			dst := filepath.Join(node4DataDir, secrets.ConsensusFolderLocal, filename)

			data, err := os.ReadFile(src)
			require.NoError(t, err, "read node-0 key file %s", filename)

			// SecretsInit writes key files as 0440 (read-only). Remove the existing
			// file first so os.WriteFile can create a fresh writable copy.
			require.NoError(t, os.Remove(dst), "remove node-4 key file %s before overwrite", filename)
			require.NoError(t, os.WriteFile(dst, data, 0440), "overwrite node-4 key file %s", filename)
		}

		copyKey(secrets.ValidatorKeyLocal) // always copy ECDSA key

		if validatorType == validators.BLSValidatorType {
			copyKey(secrets.ValidatorBLSKeyLocal) // also copy BLS key for BLS validators
		}

		// ── Start all five nodes ──────────────────────────────────────────────────────────
		startCtx, startCancel := context.WithTimeout(context.Background(), startTimeout)
		defer startCancel()

		ibftManager.StartServers(startCtx)

		servers := make([]*framework.TestServer, ibftFiveValidators)
		for i := 0; i < ibftFiveValidators; i++ {
			servers[i] = ibftManager.GetServer(i)
		}

		// ── Assert chain liveness ────────────────────────────────────────────────────────
		// The four unique signers (addr0, addr1, addr2, addr3) meet quorum=4, so the chain
		// must keep producing blocks. We check the first four (the ones with valid, distinct
		// signing identities in the genesis validator set).
		legitServers := servers[:4]

		if errs := framework.WaitForServersToSeal(legitServers, initialSealHeight); len(errs) != 0 {
			t.Fatalf("chain should seal despite duplicate signing key (4 unique signers meet quorum=4): %v", errs)
		}

		// Continue sealing past the initial height to confirm the chain is not just lucky on
		// the first few blocks.
		target := initialSealHeight + blocksAfterDup
		waitAllReach(t, legitServers, target, recoverWait,
			"validators should keep sealing with a duplicate-key node present")

		// Node[4] (the double-signer) should still be reachable via JSON-RPC: it must not
		// have crashed. We only check that its best block is at least the initial seal height
		// (it may lag slightly because its signing key is redundant, but it should sync).
		node4Height, err := servers[4].GetLatestBlockHeight()
		require.NoError(t, err, "node-4 (double-signer) JSON-RPC must remain responsive")
		require.GreaterOrEqual(t, node4Height, initialSealHeight,
			"node-4 (double-signer) should sync at least to initialSealHeight=%d via block propagation", initialSealHeight)
	}

	t.Run("ECDSA", func(t *testing.T) {
		run(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		run(t, validators.BLSValidatorType)
	})
}
