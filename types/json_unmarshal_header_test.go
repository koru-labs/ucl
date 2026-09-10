package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// rpcHeaderJSON builds a header document shaped like the eth_getBlockByNumber /
// eth_getHeaderByNumber response, with the base fee field spelled as the caller wants
// (or omitted when key is empty).
func rpcHeaderJSON(t *testing.T, key, value string) []byte {
	t.Helper()

	doc := map[string]string{
		"hash":             "0x" + ZeroHash.String()[2:],
		"parentHash":       ZeroHash.String(),
		"sha3Uncles":       EmptyUncleHash.String(),
		"transactionsRoot": EmptyRootHash.String(),
		"stateRoot":        EmptyRootHash.String(),
		"receiptsRoot":     EmptyRootHash.String(),
		"miner":            ZeroAddress.String(),
		"number":           "0x7",
		"gasLimit":         "0x1c9c380",
		"gasUsed":          "0x0",
		"mixHash":          ZeroHash.String(),
		"nonce":            "0x0000000000000000",
		"timestamp":        "0x1",
		"difficulty":       "0x7",
		"extraData":        "0x",
	}

	if key != "" {
		doc[key] = value
	}

	buf, err := json.Marshal(doc)
	require.NoError(t, err)

	return buf
}

// The JSON-RPC server emits the base fee as "baseFeePerGas". The client-side decoder must
// read it back, otherwise every header fetched through the Go client reports a zero base fee.
func TestHeader_UnmarshalJSON_BaseFee(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
		want  uint64
	}{
		{"baseFeePerGas as emitted by the RPC server", "baseFeePerGas", "0x3b9aca00", 1_000_000_000},
		{"legacy baseFee key", "baseFee", "0x3b9aca00", 1_000_000_000},
		{"pre-London header without base fee", "", "", 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := &Header{}
			require.NoError(t, json.Unmarshal(rpcHeaderJSON(t, tt.key, tt.value), out))
			require.Equal(t, tt.want, out.BaseFee)
			require.Equal(t, uint64(7), out.Number)
		})
	}
}
