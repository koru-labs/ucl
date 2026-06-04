package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewDisjointSetUnion(t *testing.T) {
	const n = 10

	d := NewDisjointSetUnion(n)

	require.Equal(t, n, len(d.parent))
	require.Equal(t, n, len(d.rank))

	for i := range n {
		require.Equal(t, i, d.parent[i])
		require.Equal(t, 1, d.rank[i])
	}
}

func TestUnionFindAndPathCompression(t *testing.T) {
	const n = 6

	d := NewDisjointSetUnion(n)

	require.True(t, d.Union(0, 1), "expected union(0,1) to return true")
	require.True(t, d.Union(1, 2), "expected union(1,2) to return true")
	require.False(t, d.Union(0, 2), "expected union(0,2) to return false (already connected)")

	// Ensure Find returns the same root for connected nodes
	r0 := d.Find(0)
	r1 := d.Find(1)
	r2 := d.Find(2)

	require.Equal(t, r0, r1)
	require.Equal(t, r1, r2)

	// Create another component and then connect components
	require.True(t, d.Union(3, 4))
	require.True(t, d.Union(4, 5))
	require.True(t, d.Union(2, 3), "expected union(2,3) to connect the two components")

	// After connecting all, all nodes should share the same root
	root := d.Find(0)
	for i := range n {
		require.Equal(t, root, d.Find(i), "node %d not connected to root %d", i, root)
	}

	// Test path compression: parents of intermediate nodes should point to root
	for i := range n {
		require.Equal(t, root, d.parent[i], "parent[%d]=%d expected root %d after path compression", i, d.parent[i], root)
	}
}

func TestUnionOutOfBounds(t *testing.T) {
	d := NewDisjointSetUnion(3)

	require.False(t, d.Union(-1, 0), "expected Union to return false for negative index")
	require.False(t, d.Union(0, 3), "expected Union to return false for index >= size")
}

func TestGetGroupsVarious(t *testing.T) {
	const n = 7

	d := NewDisjointSetUnion(n)

	d.Union(0, 1)
	d.Union(1, 2)
	d.Union(3, 4)
	d.Union(5, 6)

	expected := [][]int{{0, 1, 2}, {3, 4}, {5, 6}}

	require.Equal(t, expected, d.GetGroups())

	d.Union(2, 3)

	groups := d.GetGroups()

	require.Len(t, groups, 2)
	require.Equal(t, []int{0, 1, 2, 3, 4}, groups[0])
	require.Equal(t, []int{5, 6}, groups[1])

	d.Union(1, 6)

	groups = d.GetGroups()

	require.Len(t, groups, 1)
	require.Equal(t, []int{0, 1, 2, 3, 4, 5, 6}, groups[0])
}
