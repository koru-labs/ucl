package common

import "sort"

type DisjointSetUnion struct {
	parent []int
	rank   []int
}

// NewDisjointSetUnion initializes a DisjointSetUnion structure for 'size' number of nodes (0 to size-1)
func NewDisjointSetUnion(size int) *DisjointSetUnion {
	parent := make([]int, size)
	rank := make([]int, size)

	for i := range size {
		parent[i] = i // Each node is initially its own parent
		rank[i] = 1   // Initial rank/depth of each tree is 1
	}

	return &DisjointSetUnion{parent: parent, rank: rank}
}

// Find locates the representative (root) of the set that 'i' belongs to.
// It applies Path Compression to flatten the structure for fast future lookups.
func (d *DisjointSetUnion) Find(i int) int {
	if d.parent[i] == i {
		return i
	}
	// Path compression step
	d.parent[i] = d.Find(d.parent[i])

	return d.parent[i]
}

// Union merges the sets containing 'i' and 'j'.
// Returns true if a merge happened, or false if they were already in the same set.
func (d *DisjointSetUnion) Union(i int, j int) bool {
	if i < 0 || i >= len(d.parent) || j < 0 || j >= len(d.parent) {
		return false // Out of bounds
	}

	rootI := d.Find(i)
	rootJ := d.Find(j)

	// They already belong to the same group
	if rootI == rootJ {
		return false
	}

	// Union by Rank: Attach smaller tree under roots of larger tree
	if d.rank[rootI] < d.rank[rootJ] {
		d.parent[rootI] = rootJ
	} else if d.rank[rootI] > d.rank[rootJ] {
		d.parent[rootJ] = rootI
	} else {
		d.parent[rootJ] = rootI
		d.rank[rootI]++ // Increase rank if heights were equal
	}

	return true
}

// GetGroups returns the disjoint sets as a slice of groups.
// Each group is a sorted slice of member indices. The list of groups
// is sorted by the group root to provide deterministic output.
func (d *DisjointSetUnion) GetGroups() [][]int {
	n := len(d.parent)
	groups := make([][]int, n)

	for i := range n {
		root := d.Find(i)

		groups[root] = append(groups[root], i)
	}

	res := make([][]int, 0, n)

	for i := range n {
		if len(groups[i]) > 0 {
			sort.Ints(groups[i])

			res = append(res, groups[i])
		}
	}

	return res
}
