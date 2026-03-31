package e2e

import (
	"fmt"
	"sync"
	"testing"
	"time"

	frameworkV2 "github.com/0xPolygon/polygon-edge/e2e/frameworkV2"
	"github.com/stretchr/testify/require"
)

// TestIBFT_AddRemoveValidator tests the full validator lifecycle by first having all 4 existing
// validators cast auth votes to admit a 5th candidate, then using the new 5-validator set to
// cast drop votes removing Servers[0], and asserting the final set is back to 4 active validators.
func TestIBFT_AddRemoveValidator(t *testing.T) {
	const (
		initialValidators = 4
		epochSize         = 10
	)

	var (
		firstValidatorDataDir = fmt.Sprintf("test-chain-%d", initialValidators+1)
	)

	cluster := frameworkV2.NewTestCluster(t, initialValidators,
		frameworkV2.WithEpochSize(epochSize),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	cluster.WaitForReady(t)
	t.Log("cluster is ready")

	candidateAddrs, err := cluster.InitSecrets(firstValidatorDataDir, 1)
	require.NoError(t, err)
	require.Len(t, candidateAddrs, 1)

	candidateBLS, err := ReadValidatorBLSKey(cluster.Config.Dir(firstValidatorDataDir))
	require.NoError(t, err)

	candidateAddr := candidateAddrs[0]

	t.Logf("candidate address: %s", candidateAddr)
	t.Log("candidate BLS public key", candidateBLS)

	// cast auth votes from every existing validator
	for i, srv := range cluster.Servers {
		err := srv.IBFTPropose(candidateAddr, candidateBLS, true)
		require.NoErrorf(t, err, "node %d failed to cast auth vote", i)
		t.Logf("node %d voted auth for %s", i, candidateAddr)
	}

	cluster.InitTestServer(t, firstValidatorDataDir, frameworkV2.Validator)

	require.NoError(t, cluster.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		validators, snapshotErr := cluster.Servers[0].IBFTGetValidators()
		if snapshotErr != nil {
			return false
		}

		for _, v := range validators {
			if v == candidateAddr {
				return true
			}
		}

		return false
	}))

	validators, err := cluster.Servers[0].IBFTGetValidators()
	require.NoError(t, err)

	t.Log("validators after add:", validators)

	require.Contains(t, validators, candidateAddr,
		"candidate %s must be in the active validator set", candidateAddr)
	require.Len(t, validators, initialValidators+1,
		"validator set should have grown from %d to %d", initialValidators, initialValidators+1)

	t.Logf("validator %s successfully added — set size: %d", candidateAddr, len(validators))

	targetAddr := cluster.Servers[0].Address()
	targetDataDir := cluster.Config.Dir(fmt.Sprintf("test-chain-%d", 1))

	targetBLS, err := ReadValidatorBLSKey(targetDataDir)
	require.NoError(t, err)
	t.Logf("target for removal: %s", targetAddr)

	for i := 1; i <= initialValidators; i++ {
		err := cluster.Servers[i].IBFTPropose(targetAddr, targetBLS, false)
		require.NoErrorf(t, err, "node %d failed to cast drop vote", i)
		t.Logf("node %d voted drop for %s", i, targetAddr)
	}

	// wait until target disappears from the snapshot
	err = cluster.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		current, snapshotErr := cluster.Servers[1].IBFTGetValidators()
		if snapshotErr != nil {
			return false
		}

		for _, v := range current {
			if v == targetAddr {
				return false
			}
		}

		return true
	})
	require.NoError(t, err, "target validator was not removed within timeout")

	// chain must still be alive 4 validators remain which satisfies N=3F+1 (F=1)
	require.NoError(t,
		cluster.WaitForBlock(epochSize*2+1, time.Minute),
		"chain stalled after removal — remaining validators lost quorum",
	)

	finalValidators, err := cluster.Servers[1].IBFTGetValidators()
	require.NoError(t, err)

	t.Log("validators after remove:", finalValidators)

	require.NotContains(t, finalValidators, targetAddr,
		"removed validator %s must not be in the active set", targetAddr)
	require.Contains(t, finalValidators, candidateAddr,
		"newly added validator %s must still be in the active set", candidateAddr)
	require.Len(t, finalValidators, initialValidators,
		"set should be back to %d after add+remove", initialValidators)

	t.Logf("newly added validator proved voting rights by removing %s — final set size: %d",
		targetAddr, len(finalValidators))

	currentBlock, err := cluster.Servers[1].JSONRPC().BlockNumber()
	require.NoError(t, err)

	//sStopping one more validator to be sure that a new validator is voting for new blocks
	cluster.Servers[2].Stop()

	require.NoError(t, cluster.WaitForBlock(currentBlock+uint64(5), time.Minute))
}

// TestIBFT_NotEnoughVotes verifies that a candidate is not admitted when only 1 out of 4
// validators cast an auth vote (25% < 51% threshold), asserting the set remains unchanged after 2 full epochs.
func TestIBFT_NotEnoughVotes(t *testing.T) {
	const (
		initialValidators = 4
		epochSize         = 10
		// 1 vote out of 4 = 25%, which is below the 51% threshold
		votesToCast  = 1
		waitDuration = 30 * time.Second
	)

	cluster := frameworkV2.NewTestCluster(t, initialValidators,
		frameworkV2.WithEpochSize(epochSize),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	cluster.WaitForReady(t)
	t.Log("cluster is ready")

	candidateDataDir := fmt.Sprintf("test-chain-%d", initialValidators+1)

	candidateAddrs, err := cluster.InitSecrets(candidateDataDir, 1)
	require.NoError(t, err)
	require.Len(t, candidateAddrs, 1)

	candidateAddr := candidateAddrs[0]
	candidateBLS, err := ReadValidatorBLSKey(cluster.Config.Dir(candidateDataDir))
	require.NoError(t, err)
	t.Logf("candidate address: %s", candidateAddr)

	// cast only 1 auth vote (insufficient)
	for i := 0; i < votesToCast; i++ {
		err := cluster.Servers[i].IBFTPropose(candidateAddr, candidateBLS, true)
		require.NoErrorf(t, err, "node %d failed to cast auth vote", i)
		t.Logf("node %d cast auth vote (only %d/%d — insufficient)", i, votesToCast, initialValidators)
	}

	// wait for 2 full epochs and verify the candidate never joined
	currentBlock, err := cluster.Servers[0].JSONRPC().BlockNumber()
	require.NoError(t, err)

	require.NoError(t,
		cluster.WaitForBlock(currentBlock+uint64(epochSize*2), waitDuration+time.Minute),
		"chain stopped producing blocks unexpectedly",
	)

	validators, err := cluster.Servers[0].IBFTGetValidators()
	require.NoError(t, err)

	require.NotContains(t, validators, candidateAddr,
		"candidate %s must NOT be added with only %d/%d votes",
		candidateAddr, votesToCast, initialValidators)
	require.Len(t, validators, initialValidators,
		"validator set size must remain unchanged at %d", initialValidators)

	t.Logf("candidate correctly rejected with %d/%d votes — set size remains %d",
		votesToCast, initialValidators, len(validators))
}

func TestIBFT_AddRemoveMultipleValidators(t *testing.T) {
	const (
		initialValidators = 4
		epochSize         = 10
	)

	var (
		firstValidatorDataDir  = fmt.Sprintf("test-chain-%d", initialValidators+1)
		secondValidatorDataDir = fmt.Sprintf("test-chain-%d", initialValidators+2)
	)

	cluster := frameworkV2.NewTestCluster(t, initialValidators,
		frameworkV2.WithEpochSize(epochSize),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	cluster.WaitForReady(t)
	t.Log("cluster is ready")

	firstCandidateAddrs, err := cluster.InitSecrets(firstValidatorDataDir, 1)
	require.NoError(t, err)
	require.Len(t, firstCandidateAddrs, 1)

	firstCandidateBLS, err := ReadValidatorBLSKey(cluster.Config.Dir(firstValidatorDataDir))
	require.NoError(t, err)

	firstCandidateAddr := firstCandidateAddrs[0]

	t.Logf("first candidate address: %s", firstCandidateAddr)
	t.Log("first candidate BLS public key", firstCandidateBLS)

	secondCandidateAddrs, err := cluster.InitSecrets(secondValidatorDataDir, 1)
	require.NoError(t, err)
	require.Len(t, firstCandidateAddrs, 1)

	secondCandidateBLS, err := ReadValidatorBLSKey(cluster.Config.Dir(secondValidatorDataDir))
	require.NoError(t, err)

	secondCandidateAddr := secondCandidateAddrs[0]

	t.Logf("second candidate address: %s", firstCandidateAddr)
	t.Log("second candidate BLS public key", firstCandidateBLS)

	// cast auth votes from every existing validator
	for i, srv := range cluster.Servers {
		// vote for fist candidate
		err := srv.IBFTPropose(firstCandidateAddr, firstCandidateBLS, true)
		require.NoErrorf(t, err, "node %d failed to cast auth vote", i)
		t.Logf("node %d voted auth for %s", i, firstCandidateAddr)
		// vote for second candidate
		err = srv.IBFTPropose(secondCandidateAddr, secondCandidateBLS, true)
		require.NoErrorf(t, err, "node %d failed to cast auth vote", i)
		t.Logf("node %d voted auth for %s", i, secondCandidateAddr)
	}

	cluster.InitTestServer(t, firstValidatorDataDir, frameworkV2.Validator)

	cluster.InitTestServer(t, secondValidatorDataDir, frameworkV2.Validator)

	require.NoError(t, cluster.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		validators, snapshotErr := cluster.Servers[0].IBFTGetValidators()
		if snapshotErr != nil {
			return false
		}

		firstFound, secondFound := false, false

		for _, v := range validators {
			if v == firstCandidateAddr {
				firstFound = true
			}

			if v == secondCandidateAddr {
				secondFound = true
			}
		}

		return firstFound && secondFound
	}))

	validators, err := cluster.Servers[0].IBFTGetValidators()
	require.NoError(t, err)

	t.Log("validators after add:", validators)

	require.Contains(t, validators, firstCandidateAddr,
		"candidate %s must be in the active validator set", firstCandidateAddr)
	require.Contains(t, validators, secondCandidateAddr,
		"candidate %s must be in the active validator set", secondCandidateAddr)
	require.Len(t, validators, initialValidators+2,
		"validator set should have grown from %d to %d", initialValidators, initialValidators+2)

	t.Logf("validators 1. %s 2. %s successfully added — set size: %d",
		firstCandidateAddr, secondCandidateAddr, len(validators))

	firstTargetAddr := cluster.Servers[0].Address()
	firstTargetDataDir := cluster.Config.Dir(fmt.Sprintf("test-chain-%d", 1))

	secondTargetAddr := cluster.Servers[1].Address()
	secondTargetDataDir := cluster.Config.Dir(fmt.Sprintf("test-chain-%d", 2))

	targetBLS, err := ReadValidatorBLSKey(firstTargetDataDir)
	require.NoError(t, err)
	t.Logf("target for removal: %s", firstTargetAddr)

	secondTargetBLS, err := ReadValidatorBLSKey(secondTargetDataDir)
	require.NoError(t, err)
	t.Logf("target for removal: %s", secondTargetAddr)

	for i := 2; i <= initialValidators+1; i++ {
		err := cluster.Servers[i].IBFTPropose(firstTargetAddr, targetBLS, false)
		require.NoErrorf(t, err, "node %d failed to cast drop vote", i)
		t.Logf("node %d voted drop for %s", i, firstTargetAddr)

		err = cluster.Servers[i].IBFTPropose(secondTargetAddr, secondTargetBLS, false)
		require.NoErrorf(t, err, "node %d failed to cast drop vote", i)
		t.Logf("node %d voted drop for %s", i, secondTargetAddr)
	}

	// wait until target disappears from the snapshot
	err = cluster.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		current, snapshotErr := cluster.Servers[2].IBFTGetValidators()
		if snapshotErr != nil {
			return false
		}

		for _, v := range current {
			if v == firstTargetAddr || v == secondTargetAddr {
				return false
			}
		}

		return true
	})
	require.NoError(t, err, "target validator was not removed within timeout")

	// chain must still be alive 4 validators remain which satisfies N=3F+1 (F=1)
	require.NoError(t,
		cluster.WaitForBlock(epochSize*2+1, time.Minute),
		"chain stalled after removal — remaining validators lost quorum",
	)

	finalValidators, err := cluster.Servers[1].IBFTGetValidators()
	require.NoError(t, err)

	t.Log("validators after remove:", finalValidators)

	require.NotContains(t, finalValidators, firstTargetAddr,
		"removed validator %s must not be in the active set", firstTargetAddr)
	require.NotContains(t, finalValidators, secondTargetAddr,
		"removed validator %s must not be in the active set", secondTargetAddr)
	require.Contains(t, finalValidators, firstCandidateAddr,
		"newly added validator %s must still be in the active set", firstCandidateAddr)
	require.Contains(t, finalValidators, secondCandidateAddr,
		"newly added validator %s must still be in the active set", secondCandidateAddr)
	require.Len(t, finalValidators, initialValidators,
		"set should be back to %d after add+remove", initialValidators)

	t.Logf("newly added validator proved voting rights by removing %s — final set size: %d",
		firstTargetAddr, len(finalValidators))
}

func TestIBFT_AddRemoveValidatorSimultaneous(t *testing.T) {
	const (
		initialValidators = 4
		epochSize         = 10
	)

	var (
		newValidatorDataDir = fmt.Sprintf("test-chain-%d", initialValidators+1)
	)

	cluster := frameworkV2.NewTestCluster(t, initialValidators,
		frameworkV2.WithEpochSize(epochSize),
		frameworkV2.WithBootnodeCount(1),
	)
	defer cluster.Stop()

	cluster.WaitForReady(t)
	t.Log("cluster is ready")

	// prepare new candidate
	candidateAddrs, err := cluster.InitSecrets(newValidatorDataDir, 1)
	require.NoError(t, err)
	require.Len(t, candidateAddrs, 1)

	candidateBLS, err := ReadValidatorBLSKey(cluster.Config.Dir(newValidatorDataDir))
	require.NoError(t, err)

	candidateAddr := candidateAddrs[0]
	t.Logf("candidate address: %s", candidateAddr)

	// pick removal target before voting
	targetAddr := cluster.Servers[0].Address()
	targetDataDir := cluster.Config.Dir(fmt.Sprintf("test-chain-%d", 1))
	targetBLS, err := ReadValidatorBLSKey(targetDataDir)
	require.NoError(t, err)
	t.Logf("target for removal: %s", targetAddr)

	var voteWg sync.WaitGroup
	voteWg.Add(2)

	var authErr, dropErr error

	go func() {
		defer voteWg.Done()

		for i, srv := range cluster.Servers {
			authErr = srv.IBFTPropose(candidateAddr, candidateBLS, true)
			require.NoErrorf(t, authErr, "node %d failed to cast auth vote", i)
			t.Logf("node %d voted auth for %s", i, candidateAddr)
		}
	}()

	go func() {
		defer voteWg.Done()

		for i, srv := range cluster.Servers {
			dropErr = srv.IBFTPropose(targetAddr, targetBLS, false)
			require.NoErrorf(t, dropErr, "node %d failed to cast drop vote", i)
			t.Logf("node %d voted drop for %s", i, targetAddr)
		}
	}()

	voteWg.Wait()

	cluster.InitTestServer(t, newValidatorDataDir, frameworkV2.Validator)

	//  wait until candidate is added AND target is removed
	err = cluster.WaitUntil(2*time.Minute, 2*time.Second, func() bool {
		current, snapshotErr := cluster.Servers[1].IBFTGetValidators()
		if snapshotErr != nil {
			return false
		}

		candidateFound := false

		for _, v := range current {
			if v == targetAddr {
				return false
			}

			if v == candidateAddr {
				candidateFound = true
			}
		}

		return candidateFound
	})
	require.NoError(t, err, "add+remove did not settle within timeout")

	require.NoError(t,
		cluster.WaitForBlock(epochSize*2+1, time.Minute),
		"chain stalled after simultaneous add+remove",
	)

	finalValidators, err := cluster.Servers[1].IBFTGetValidators()
	require.NoError(t, err)

	t.Log("validators after simultaneous add+remove:", finalValidators)

	require.Contains(t, finalValidators, candidateAddr,
		"new validator %s must be in the active set", candidateAddr)
	require.NotContains(t, finalValidators, targetAddr,
		"removed validator %s must not be in the active set", targetAddr)
	require.Len(t, finalValidators, initialValidators,
		"set should remain at %d after simultaneous add+remove", initialValidators)

	t.Logf("simultaneous add+remove succeeded — final set size: %d", len(finalValidators))

	// confirm new validator is actively voting
	currentBlock, err := cluster.Servers[1].JSONRPC().BlockNumber()
	require.NoError(t, err)

	cluster.Servers[2].Stop()

	require.NoError(t, cluster.WaitForBlock(currentBlock+uint64(5), time.Minute))
}
