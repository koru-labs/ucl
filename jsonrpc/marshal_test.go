package jsonrpc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarshalCapped(t *testing.T) {
	t.Parallel()

	t.Run("unlimited matches standard marshal", func(t *testing.T) {
		t.Parallel()

		v := []string{"a", "b", "c"}
		got, err := marshalCapped(v, 0)
		require.NoError(t, err)

		want, err := fastJSONIt.Marshal(v)
		require.NoError(t, err)
		require.Equal(t, want, got)
	})

	t.Run("aborts during encoding when the cap is exceeded", func(t *testing.T) {
		t.Parallel()

		payload := make([]string, 200)
		for i := range payload {
			payload[i] = strings.Repeat("x", 64)
		}

		_, err := marshalCapped(payload, 128)
		require.ErrorIs(t, err, errResponseTooLarge)
	})

	t.Run("accepts a result that fits", func(t *testing.T) {
		t.Parallel()

		got, err := marshalCapped("ok", 16)
		require.NoError(t, err)
		require.Equal(t, []byte(`"ok"`), got)
	})
}

func TestWrapLogQueryError(t *testing.T) {
	t.Parallel()

	err := wrapLogQueryError(errTooManyLogs(10))

	var limitErr *limitExceededError
	require.ErrorAs(t, err, &limitErr)
	require.Contains(t, err.Error(), "query returned more than 10 results")

	err = wrapLogQueryError(ErrLogQueryFilterRequired)
	require.ErrorAs(t, err, &limitErr)

	err = wrapLogQueryError(ErrBlockNotFound)
	require.ErrorIs(t, err, ErrBlockNotFound)
}
