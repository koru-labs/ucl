package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBloomContains(t *testing.T) {
	t.Parallel()

	addr := StringToAddress("1")
	topic := StringToHash("4")
	other := StringToAddress("2")

	bloom := CreateBloom([]*Receipt{{
		Logs: []*Log{{
			Address: addr,
			Topics:  []Hash{topic},
		}},
	}})

	require.True(t, bloom.Contains(addr.Bytes()))
	require.True(t, bloom.Contains(topic.Bytes()))
	require.False(t, bloom.Contains(other.Bytes()))
}
