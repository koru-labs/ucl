package ibft

import (
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
)

// quorumSize returns a callback that when executed on a Validators computes
// number of votes required to reach quorum based on the size of the set.
// The blockNumber argument indicates which formula was used to calculate the result (see PRs #513, #549)
func quorumSize(set validators.Validators) int {
	// If the set is empty, return 0
	if set == nil {
		return 0
	}

	// ibft forumal 2/3*N+1, where N is the number of validators in the set
	return (2*set.Len())/3 + 1
}

func calcMaxFaultyNodes(s validators.Validators) int {
	// N -> number of nodes in IBFT
	// F -> number of faulty nodes
	//
	// N = 3F + 1
	// => F = (N - 1) / 3
	//
	// IBFT tolerates 1 failure with 4 nodes
	// 4 = 3 * 1 + 1
	// To tolerate 2 failures, IBFT requires 7 nodes
	// 7 = 3 * 2 + 1
	// It should always take the floor of the result
	return (s.Len() - 1) / 3
}

func calcProposer(
	validators validators.Validators,
	round uint64,
	lastProposer types.Address,
) validators.Validator {
	var seed uint64

	if lastProposer == types.ZeroAddress {
		seed = round
	} else {
		offset := int64(0)

		if index := validators.Index(lastProposer); index != -1 {
			offset = index
		}

		seed = uint64(offset) + round + 1
	}

	pick := seed % uint64(validators.Len())

	return validators.At(pick)
}
