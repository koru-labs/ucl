package ibft

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCalcMaxFaultyNodes checks if the max faulty nodes is calculated correctly
// based on number of validators (network size).
func TestCalcMaxFaultyNodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		Network, Faulty uint64
	}{
		{1, 0},
		{2, 0},
		{3, 0},
		{4, 1},
		{5, 1},
		{6, 1},
		{7, 2},
		{8, 2},
		{9, 2},
	}
	for _, c := range cases {
		pool := newTesterAccountPool(t, int(c.Network))
		vals := pool.ValidatorSet()
		assert.Equal(t, calcMaxFaultyNodes(vals), int(c.Faulty))
	}
}

// TestQuorumSize checks if the quorum size is calculated correctly
// based on number of validators (network size).
func TestQuorumSize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		Network, Quorum uint64
	}{
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 3},
		{5, 4},
		{6, 5},
		{7, 5},
		{8, 6},
		{9, 7},
		{10, 7},
	}

	addAccounts := func(
		pool *testerAccountPool,
		numAccounts int,
	) {
		// add accounts
		for i := 0; i < numAccounts; i++ {
			pool.add(strconv.Itoa(i))
		}
	}

	for _, c := range cases {
		pool := newTesterAccountPool(t, int(c.Network))
		addAccounts(pool, int(c.Network))

		assert.Equal(t,
			int(c.Quorum),
			quorumSize(pool.ValidatorSet()),
		)
	}
}

// TestCalcProposer checks if the proposer is calculated correctly
// based on the round number and the last proposer.
func TestCalcProposer(t *testing.T) {
	t.Parallel()

	const validators = 4

	pool := newTesterAccountPool(t, validators)
	for i := 0; i < validators; i++ {
		pool.add(strconv.Itoa(i))
	}

	assert.Equal(t,
		pool.accounts[3].Address(),
		calcProposer(pool.ValidatorSet(), 0, pool.accounts[2].Address()).Addr(),
	)
}
