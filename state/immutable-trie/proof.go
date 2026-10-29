package itrie

import (
	"bytes"
	"fmt"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/umbracle/fastrlp"
)

// Prove returns the Merkle proof of `key` in the trie whose root is `root`, as Ethereum's
// eth_getProof (EIP-1186) answers it: the RLP encoding of every node stored by hash on the path
// from the root towards the key, the root first. The path ends at the key's leaf, or where the
// trie shows that it does not hold the key; nodes shorter than 32 bytes are embedded in their
// parent and are not listed. `key` is the trie key, already hashed (keccak256 of an address or a
// storage slot). `get` reads a node by its hash.
func Prove(get func(types.Hash) ([]byte, bool, error), root types.Hash, key []byte) ([][]byte, error) {
	proof := [][]byte{}
	if root == types.EmptyRootHash || root == types.ZeroHash {
		return proof, nil
	}

	path := bytesToHexNibbles(key)
	path = path[:len(path)-1]
	ref := root.Bytes()

	p := parserPool.Get()
	defer parserPool.Put(p)

	for ref != nil {
		data, ok, err := get(types.BytesToHash(ref))
		if err != nil {
			return nil, err
		}

		if !ok {
			return nil, fmt.Errorf("trie node %x is missing", ref)
		}

		node := append([]byte(nil), data...)
		proof = append(proof, node)

		v, err := p.Parse(node)
		if err != nil {
			return nil, fmt.Errorf("trie node %x: %w", ref, err)
		}

		next, rest, err := descend(v, path)
		if err != nil {
			return nil, fmt.Errorf("trie node %x: %w", ref, err)
		}

		ref, path = next, rest
	}

	return proof, nil
}

// descend follows `path` from node `v` through the nodes embedded in it, to the next node stored
// by hash: that node's hash and the rest of the path. The hash is nil where the path ends.
func descend(v *fastrlp.Value, path []byte) ([]byte, []byte, error) {
	for {
		if v.Type() == fastrlp.TypeBytes {
			switch raw := v.Raw(); len(raw) {
			case 0:
				return nil, nil, nil
			case types.HashLength:
				return raw, path, nil
			default:
				return nil, nil, fmt.Errorf("a child reference of %d bytes", len(raw))
			}
		}

		switch v.Elems() {
		case 2:
			key := decodeCompact(v.Get(0).Raw())
			if hasTerminator(key) {
				return nil, nil, nil
			}

			if len(path) < len(key) || !bytes.Equal(path[:len(key)], key) {
				return nil, nil, nil
			}

			path = path[len(key):]
			v = v.Get(1)
		case 17:
			if len(path) == 0 {
				return nil, nil, nil
			}

			v = v.Get(int(path[0]))
			path = path[1:]
		default:
			return nil, nil, fmt.Errorf("a node of %d items", v.Elems())
		}
	}
}
