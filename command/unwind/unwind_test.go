package unwind

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveTarget(t *testing.T) {
	t.Parallel()

	target, err := resolveTarget(20, 3, 0, true)
	require.NoError(t, err)
	require.Equal(t, uint64(17), target)

	target, err = resolveTarget(20, 0, 10, false)
	require.NoError(t, err)
	require.Equal(t, uint64(10), target)

	target, err = resolveTarget(20, 0, 0, false)
	require.NoError(t, err)
	require.Equal(t, uint64(0), target)

	_, err = resolveTarget(20, 0, 0, true)
	require.Error(t, err)

	_, err = resolveTarget(5, 6, 0, true)
	require.Error(t, err)

	_, err = resolveTarget(20, 0, 21, false)
	require.Error(t, err)

	_, err = resolveTarget(20, 0, 20, false)
	require.Error(t, err)
}
