package jsonrpc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	addr1 = types.StringToAddress("1")
	addr2 = types.StringToAddress("2")

	hash1 = types.StringToHash("1")
	hash2 = types.StringToHash("2")
	hash3 = types.StringToHash("3")
	hash4 = types.StringToHash("4")
)

func TestFilterDecode(t *testing.T) {
	cases := []struct {
		str string
		res *LogQuery
	}{
		{
			`{}`,
			&LogQuery{
				fromBlock: LatestBlockNumber,
				toBlock:   LatestBlockNumber,
			},
		},
		{
			`{
				"address": "1"
			}`,
			nil,
		},
		{
			`{
				"address": "` + addr1.String() + `"
			}`,
			&LogQuery{
				fromBlock: LatestBlockNumber,
				toBlock:   LatestBlockNumber,
				Addresses: []types.Address{
					addr1,
				},
			},
		},
		{
			`{
				"address": [
					"` + addr1.String() + `",
					"` + addr2.String() + `"
				]
			}`,
			&LogQuery{
				fromBlock: LatestBlockNumber,
				toBlock:   LatestBlockNumber,
				Addresses: []types.Address{
					addr1,
					addr2,
				},
			},
		},
		{
			`{
				"topics": [
					"` + hash1.String() + `",
					[
						"` + hash1.String() + `"
					],
					[
						"` + hash1.String() + `",
						"` + hash2.String() + `"
					],
					null
				]
			}`,
			&LogQuery{
				fromBlock: LatestBlockNumber,
				toBlock:   LatestBlockNumber,
				Topics: [][]types.Hash{
					{
						hash1,
					},
					{
						hash1,
					},
					{
						hash1,
						hash2,
					},
					{},
				},
			},
		},
		{
			`{
				"fromBlock": "earliest",
				"toBlock": ""
			}`,
			&LogQuery{
				fromBlock: EarliestBlockNumber,
				toBlock:   LatestBlockNumber, // empty is converted to the latest
			},
		},
		{
			`{
				"fromBlock": "pending",
				"toBlock": "earliest"
			}`,
			&LogQuery{
				fromBlock: LatestBlockNumber, // pending is converted to the latest
				toBlock:   EarliestBlockNumber,
			},
		},
		{
			`{
				"blockHash": "` + hash1.String() + `"
			}`,
			&LogQuery{
				BlockHash: &hash1,
				fromBlock: LatestBlockNumber,
				toBlock:   LatestBlockNumber,
			},
		},
	}

	for _, c := range cases {
		res := &LogQuery{}
		err := res.UnmarshalJSON([]byte(c.str))

		if c.res != nil {
			require.NoError(t, err)
			require.Equal(t, c.res.fromBlock, res.fromBlock)
			require.Equal(t, c.res.toBlock, res.toBlock)
			require.Equal(t, c.res.BlockHash, res.BlockHash)
			require.Equal(t, c.res.Addresses, res.Addresses)
			require.Equal(t, c.res.Topics, res.Topics)
		} else {
			require.Error(t, err)
		}
	}
}

func TestFilterMatch(t *testing.T) {
	cases := []struct {
		filter LogQuery
		log    *types.Log
		match  bool
	}{
		{
			// correct, exact match
			LogQuery{
				Topics: [][]types.Hash{
					{
						hash1,
					},
				},
			},
			&types.Log{
				Topics: []types.Hash{
					hash1,
				},
			},
			true,
		},
		{
			// bad, the filter has two hashes
			LogQuery{
				Topics: [][]types.Hash{
					{
						hash1,
					},
					{
						hash1,
					},
				},
			},
			&types.Log{
				Topics: []types.Hash{
					hash1,
				},
			},
			false,
		},
		{
			// correct, wildcard in one hash
			LogQuery{
				Topics: [][]types.Hash{
					{},
					{
						hash2,
					},
				},
			},
			&types.Log{
				Topics: []types.Hash{
					hash1,
					hash2,
				},
			},
			true,
		},
		{
			// correct, more topics than in filter
			LogQuery{
				Topics: [][]types.Hash{
					{
						hash1,
					},
					{
						hash2,
					},
				},
			},
			&types.Log{
				Topics: []types.Hash{
					hash1,
					hash2,
					hash3,
				},
			},
			true,
		},
		{
			// address hit via set lookup
			LogQuery{
				Addresses: []types.Address{addr1, addr2},
			},
			&types.Log{
				Address: addr2,
			},
			true,
		},
		{
			// address miss
			LogQuery{
				Addresses: []types.Address{addr1},
			},
			&types.Log{
				Address: addr2,
			},
			false,
		},
		{
			// first address in the list still matches
			LogQuery{
				Addresses: []types.Address{addr1, addr2},
			},
			&types.Log{
				Address: addr1,
			},
			true,
		},
	}

	for _, c := range cases {
		assert.Equal(t, c.match, c.filter.Match(c.log))
	}
}

func TestFilterDecode_CapsAddressesAndTopicPositions(t *testing.T) {
	t.Parallel()

	limits := LogQueryLimits{MaxAddresses: 2, MaxTopicPositions: 2}

	t.Run("rejects more addresses than the cap", func(t *testing.T) {
		t.Parallel()

		payload := fmt.Sprintf(
			`{"address":["%s","%s","%s"]}`,
			addr1.String(),
			addr2.String(),
			types.StringToAddress("3").String(),
		)

		q := &LogQuery{}
		err := unmarshalLogQuery(q, []byte(payload), limits)
		require.ErrorIs(t, err, ErrExceedMaxAddresses)
		require.Empty(t, q.Addresses)
	})

	t.Run("accepts the address cap", func(t *testing.T) {
		t.Parallel()

		payload := fmt.Sprintf(`{"address":["%s","%s"]}`, addr1.String(), addr2.String())

		q := &LogQuery{}
		require.NoError(t, unmarshalLogQuery(q, []byte(payload), limits))
		require.Equal(t, []types.Address{addr1, addr2}, q.Addresses)
		_, ok := q.addressSet[addr2]
		require.True(t, ok)
	})

	t.Run("rejects more topic positions than the cap", func(t *testing.T) {
		t.Parallel()

		payload := fmt.Sprintf(
			`{"topics":["%s","%s","%s"]}`,
			hash1.String(),
			hash2.String(),
			hash3.String(),
		)

		q := &LogQuery{}
		err := unmarshalLogQuery(q, []byte(payload), limits)
		require.ErrorIs(t, err, ErrExceedMaxTopics)
		require.Empty(t, q.Topics)
	})

	t.Run("rejects a topic set larger than the address cap", func(t *testing.T) {
		t.Parallel()

		payload := fmt.Sprintf(
			`{"topics":[["%s","%s","%s"]]}`,
			hash1.String(),
			hash2.String(),
			hash3.String(),
		)

		q := &LogQuery{}
		err := unmarshalLogQuery(q, []byte(payload), limits)
		require.ErrorIs(t, err, ErrExceedMaxTopics)
	})

	t.Run("default topic-position cap is 4", func(t *testing.T) {
		t.Parallel()

		payload := fmt.Sprintf(
			`{"topics":["%s","%s","%s","%s","%s"]}`,
			hash1.String(),
			hash2.String(),
			hash3.String(),
			hash4.String(),
			types.StringToHash("5").String(),
		)

		q := &LogQuery{}
		err := q.UnmarshalJSON([]byte(payload))
		require.ErrorIs(t, err, ErrExceedMaxTopics)
	})
}

// TestPoC_LogQueryAddressListIsUnbounded is the inverted proof of the original report:
// UnmarshalJSON used to accept 200_000 addresses. It must reject them before they
// land on the query and become per-log linear work.
func TestPoC_LogQueryAddressListIsUnbounded(t *testing.T) {
	t.Parallel()

	const n = 200_000

	var b strings.Builder
	b.Grow(n * 50)
	b.WriteString(`{"address":[`)

	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}

		fmt.Fprintf(&b, `"%s"`, types.StringToAddress(fmt.Sprintf("%d", i)))
	}

	b.WriteString(`]}`)

	q := &LogQuery{}
	err := q.UnmarshalJSON([]byte(b.String()))
	require.ErrorIs(t, err, ErrExceedMaxAddresses)
	require.Empty(t, q.Addresses)
	require.Nil(t, q.addressSet)
}

func TestLogQuery_MatchUsesAddressSet(t *testing.T) {
	t.Parallel()

	addresses := make([]types.Address, 100)
	for i := range addresses {
		addresses[i] = types.StringToAddress(fmt.Sprintf("%d", i+10))
	}

	q := &LogQuery{Addresses: addresses}
	require.True(t, q.Match(&types.Log{Address: addresses[len(addresses)-1]}))
	require.False(t, q.Match(&types.Log{Address: addr1}))
	_, ok := q.addressSet[addresses[0]]
	require.True(t, ok, "Match must index addresses into a set")
}

func TestLogQuery_MatchBloom(t *testing.T) {
	t.Parallel()

	matchingLog := &types.Log{
		Address: addr1,
		Topics:  []types.Hash{hash1, hash2},
	}
	bloom := types.CreateBloom([]*types.Receipt{{Logs: []*types.Log{matchingLog}}})

	t.Run("empty query matches any bloom", func(t *testing.T) {
		t.Parallel()

		require.True(t, (&LogQuery{}).MatchBloom(types.Bloom{}))
		require.True(t, (&LogQuery{}).MatchBloom(bloom))
	})

	t.Run("zero bloom is unknown and does not skip a constrained query", func(t *testing.T) {
		t.Parallel()

		q := &LogQuery{
			Addresses: []types.Address{addr1},
			Topics:    [][]types.Hash{{hash1}},
		}
		require.True(t, q.MatchBloom(types.Bloom{}))
	})

	t.Run("address present in the bloom", func(t *testing.T) {
		t.Parallel()

		q := &LogQuery{Addresses: []types.Address{addr1, addr2}}
		require.True(t, q.MatchBloom(bloom))
	})

	t.Run("address missing from the bloom", func(t *testing.T) {
		t.Parallel()

		q := &LogQuery{Addresses: []types.Address{addr2}}
		require.False(t, q.MatchBloom(bloom))
	})

	t.Run("topic present in the bloom", func(t *testing.T) {
		t.Parallel()

		q := &LogQuery{Topics: [][]types.Hash{{hash1}, {hash2, hash3}}}
		require.True(t, q.MatchBloom(bloom))
	})

	t.Run("topic missing from the bloom", func(t *testing.T) {
		t.Parallel()

		q := &LogQuery{Topics: [][]types.Hash{{hash3}}}
		require.False(t, q.MatchBloom(bloom))
	})

	t.Run("wildcard topic position is ignored", func(t *testing.T) {
		t.Parallel()

		q := &LogQuery{Topics: [][]types.Hash{{}, {hash2}}}
		require.True(t, q.MatchBloom(bloom))
	})
}
