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

	// livenessEnvVar gates the long-running IBFT liveness/recovery tests below; they are skipped
	// unless it is "true" (set by the `test-e2e-liveness` Makefile target / nightly CI job).
	livenessEnvVar = "E2E_LIVENESS_TESTS"

	// settleAfterStop lets an in-flight block finalize before we sample the "stalled" reference
	// height, so a strict height comparison doesn't flap on the stop boundary.
	settleAfterStop = 5 * time.Second

	// settleAfterRestart lets libp2p/IBFT reconnect after a restart before we poll for recovery.
	settleAfterRestart = 3 * time.Second
)

// requireLivenessEnabled skips the calling test unless the liveness env var is set to "true".
func requireLivenessEnabled(t *testing.T) {
	t.Helper()

	if os.Getenv(livenessEnvVar) != "true" {
		t.Skipf("skipping long-running IBFT liveness test; set %s=true (or run `make test-e2e-liveness`) to enable",
			livenessEnvVar)
	}
}

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
			config.SetBlockTime(1) // 1s blocks (default 2s) to keep runtime down
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

// assertChainStalled samples the committed head after a settle window and again after observe, and
// asserts they are equal (chain not advancing), returning the stalled height. It checks only the
// committed head; internal IBFT round state is not exposed by the API.
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

// runTwoValidatorStallThenRecover boots a 2-validator cluster (quorum 2), stops one peer, asserts
// the head does not advance for the observation window, then restarts it and asserts both seal
// again. With two validators a single fault removes quorum, so the chain must halt then resume.
func runTwoValidatorStallThenRecover(t *testing.T, validatorType validators.ValidatorType, c twoValStallCase) {
	t.Helper()

	const (
		initialSealHeight uint64 = 8
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

// TestIBFTLiveness_TwoValidators_OneDownStallsChain: with a 2-validator set (quorum 2), gracefully
// stopping one validator must freeze the committed head (sealing without quorum is the faulty
// behaviour we guard against); the chain must recover after restart.
func TestIBFTLiveness_TwoValidators_OneDownStallsChain(t *testing.T) {
	requireLivenessEnabled(t)

	run := func(t *testing.T, validatorType validators.ValidatorType) {
		t.Helper()

		runTwoValidatorStallThenRecover(t, validatorType, twoValStallCase{
			dirPrefix:        "e2e-ibft-2val-",
			stop:             func(s *framework.TestServer) { s.Stop() },
			stallObservation: 30 * time.Second,
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

// TestIBFTLiveness_TwoValidators_OneDownKill9Recovers is like the graceful-stop variant, but the
// validator is killed with `kill -9` (StopViaKill9) to simulate an unclean crash before restart.
func TestIBFTLiveness_TwoValidators_OneDownKill9Recovers(t *testing.T) {
	requireLivenessEnabled(t)

	run := func(t *testing.T, validatorType validators.ValidatorType) {
		t.Helper()

		runTwoValidatorStallThenRecover(t, validatorType, twoValStallCase{
			dirPrefix:        "e2e-ibft-2val-kill9-",
			stop:             func(s *framework.TestServer) { s.StopViaKill9() },
			stallObservation: 30 * time.Second,
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

// TestIBFTLiveness_TwoValidators_LongPartitionRecovers is the graceful-stop variant with a much
// longer down period, checking IBFT still recovers after a prolonged partition without a
// whole-cluster restart.
func TestIBFTLiveness_TwoValidators_LongPartitionRecovers(t *testing.T) {
	requireLivenessEnabled(t)

	run := func(t *testing.T, validatorType validators.ValidatorType) {
		t.Helper()

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

// TestIBFTLiveness_MinorityValidatorRestart stops one of four validators (quorum retained), lets
// the rest advance, then restarts the node and asserts all four converge on a common height —
// failing if the restarted node never rejoins consensus.
func TestIBFTLiveness_MinorityValidatorRestart(t *testing.T) {
	requireLivenessEnabled(t)

	const (
		initialSealHeight  uint64 = 10
		blocksWhileDown    uint64 = 6
		blocksAfterRestart uint64 = 4
		partitionWallTime         = 20 * time.Second
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

// TestIBFTLiveness_SixValidators_OneDownMajoritySeals: with six validators (quorum = (2*6)/3+1 = 5),
// stopping one leaves exactly quorum (5), which must still seal. One fault is the most IBFT tolerates
// here (f = floor((n-1)/3) = 1 for n=6), so this is the boundary case, not a margin.
func TestIBFTLiveness_SixValidators_OneDownMajoritySeals(t *testing.T) {
	requireLivenessEnabled(t)

	const (
		initialSealHeight uint64 = 10
		blocksWithOneDown uint64 = 8
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
			t.Fatalf("with 6 validators and 1 stopped, remaining 5 should still seal (quorum=5): %v", errs)
		}
	}

	t.Run("ECDSA", func(t *testing.T) {
		run(t, validators.ECDSAValidatorType)
	})

	t.Run("BLS", func(t *testing.T) {
		run(t, validators.BLSValidatorType)
	})
}

// TestIBFTLiveness_SuperminorityPartitionRecovers stops more than n/3 validators so the remainder
// lacks quorum and the head must freeze; after restart, quorum returns and every validator must
// seal further blocks. Layout: 7 validators ⇒ quorum 5; stopping 3 leaves 4 (< quorum), 3/7 > 1/3.
func TestIBFTLiveness_SuperminorityPartitionRecovers(t *testing.T) {
	requireLivenessEnabled(t)

	const (
		initialSealHeight     uint64 = 10
		blocksAfterQuorumBack uint64 = 4
		stopCount                    = 3
		partitionWallTime            = 30 * time.Second
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

// TestIBFTLiveness_DuplicateValidatorKeyHealthy replaces validator[4]'s signing key with
// validator[0]'s before startup, so nodes 0 and 4 both sign as addr0 (double signing). Peers
// discard the redundant votes, leaving four unique signers (addr0..addr3) — exactly quorum for
// n=5 (floor(2*5/3)+1 = 4) — so the chain must keep sealing and no node should crash.
func TestIBFTLiveness_DuplicateValidatorKeyHealthy(t *testing.T) {
	requireLivenessEnabled(t)

	const (
		initialSealHeight uint64 = 8
		blocksAfterDup    uint64 = 6
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
				config.SetBlockTime(1) // 1s blocks (default 2s) to keep runtime down
			},
		)

		// Overwrite node[4]'s signing key(s) with node[0]'s so both sign as addr0.
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

		copyKey(secrets.ValidatorKeyLocal)

		if validatorType == validators.BLSValidatorType {
			copyKey(secrets.ValidatorBLSKeyLocal)
		}

		startCtx, startCancel := context.WithTimeout(context.Background(), startTimeout)
		defer startCancel()

		ibftManager.StartServers(startCtx)

		servers := make([]*framework.TestServer, ibftFiveValidators)
		for i := 0; i < ibftFiveValidators; i++ {
			servers[i] = ibftManager.GetServer(i)
		}

		// The four unique signers (addr0..addr3) meet quorum=4, so the chain must keep sealing.
		legitServers := servers[:4]

		if errs := framework.WaitForServersToSeal(legitServers, initialSealHeight); len(errs) != 0 {
			t.Fatalf("chain should seal despite duplicate signing key (4 unique signers meet quorum=4): %v", errs)
		}

		target := initialSealHeight + blocksAfterDup
		waitAllReach(t, legitServers, target, recoverWait,
			"validators should keep sealing with a duplicate-key node present")

		// Node[4] (the double-signer) must not have crashed and should sync via propagation.
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
