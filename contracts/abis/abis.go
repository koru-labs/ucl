package abis

import (
	"github.com/Ethernal-Tech/ethgo/abi"
)

var (
	// ABI for Staking Contract
	StakingABI = abi.MustNewABI(StakingJSONABI)

	// ABI for Contract used in e2e stress test
	StressTestABI = abi.MustNewABI(StressTestJSONABI)
)
