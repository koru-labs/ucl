package server

import (
	"testing"

	"github.com/0xPolygon/polygon-edge/command/server/config"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/require"
)

// --migration-attestations takes a full 0x address; anything else stops the node instead of
// leaving it without a halt.
func TestMigrationAttestationsIsParsedStrictly(t *testing.T) {
	contract := types.StringToAddress("0x4a17000000000000000000000000000000000001")

	for _, tc := range []struct {
		raw  string
		want *types.Address
		ok   bool
	}{
		{raw: "", want: nil, ok: true},
		{raw: "0x4a17000000000000000000000000000000000001", want: &contract, ok: true},
		{raw: "4a17000000000000000000000000000000000001", ok: false},
		{raw: "0x4a17", ok: false},
		{raw: "0x4a1700000000000000000000000000000000000g", ok: false},
		{raw: "0x0000000000000000000000000000000000000000", ok: false},
	} {
		raw := config.DefaultConfig()
		raw.MigrationAttestations = tc.raw
		p := &serverParams{rawConfig: raw}
		err := p.initMigrationAttestations()

		if !tc.ok {
			require.Error(t, err, tc.raw)

			continue
		}

		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, p.migrationAttestations, tc.raw)
		require.Equal(t, tc.want, p.generateConfig().MigrationAttestations, tc.raw)
	}
}
