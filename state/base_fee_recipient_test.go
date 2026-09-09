package state

import (
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

func TestExecutor_baseFeeRecipient(t *testing.T) {
	t.Parallel()

	var (
		coinbase = types.StringToAddress("0xc0ffee")
		burnAddr = types.StringToAddress("0xdead")
		burnAt10 = types.StringToAddress("0xbeef")
	)

	tests := []struct {
		name     string
		params   *chain.Params
		forks    chain.ForksInTime
		number   uint64
		expected types.Address
	}{
		{
			name:     "pre-London: no base fee, nothing to credit",
			params:   &chain.Params{},
			forks:    chain.ForksInTime{London: false},
			number:   5,
			expected: types.ZeroAddress,
		},
		{
			name:     "London without burn contract: base fee goes to the proposer",
			params:   &chain.Params{},
			forks:    chain.ForksInTime{London: true},
			number:   5,
			expected: coinbase,
		},
		{
			name: "London with burn contract: base fee goes to the burn contract",
			params: &chain.Params{BurnContract: map[uint64]types.Address{
				0: burnAddr,
			}},
			forks:    chain.ForksInTime{London: true},
			number:   5,
			expected: burnAddr,
		},
		{
			name: "London with scheduled burn contracts: the one active at the block is used",
			params: &chain.Params{BurnContract: map[uint64]types.Address{
				0:  burnAddr,
				10: burnAt10,
			}},
			forks:    chain.ForksInTime{London: true},
			number:   12,
			expected: burnAt10,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := &Executor{config: tt.params, logger: hclog.NewNullLogger()}

			got, err := e.baseFeeRecipient(tt.forks, tt.number, coinbase)
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
		})
	}
}
