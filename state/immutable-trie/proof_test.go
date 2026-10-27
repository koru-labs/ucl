package itrie

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/require"
	"github.com/umbracle/fastrlp"
)

// verifyProof is a verifier written apart from Prove: it checks that each node hashes to the
// reference its parent holds, and returns the value the proof shows for key (nil for absent).
func verifyProof(t *testing.T, root types.Hash, key []byte, proof [][]byte) []byte {
	t.Helper()

	nibbles := make([]byte, 0, 64)
	for _, b := range crypto.Keccak256(key) {
		nibbles = append(nibbles, b>>4, b&0xf)
	}

	want := root.Bytes()

	for i, node := range proof {
		require.Equal(t, want, crypto.Keccak256(node), "node %d is not the one its parent names", i)

		var p fastrlp.Parser

		v, err := p.Parse(node)
		require.NoError(t, err)

		want = nil

		for want == nil {
			if v.Type() == fastrlp.TypeBytes {
				if len(v.Raw()) == 0 {
					require.Equal(t, len(proof)-1, i, "the path ends before the proof does")

					return nil
				}

				want = v.Raw()

				break
			}

			items, err := v.GetElems()
			require.NoError(t, err)

			if len(items) == 17 {
				v, nibbles = items[nibbles[0]], nibbles[1:]

				continue
			}

			require.Len(t, items, 2)

			compact := items[0].Raw()
			flag := compact[0] >> 4
			path := []byte{}

			if flag&1 == 1 {
				path = append(path, compact[0]&0xf)
			}

			for _, b := range compact[1:] {
				path = append(path, b>>4, b&0xf)
			}

			if flag >= 2 {
				require.Equal(t, len(proof)-1, i, "a leaf before the proof's last node")

				if !bytes.Equal(path, nibbles) {
					return nil
				}

				return items[1].Raw()
			}

			if !bytes.HasPrefix(nibbles, path) {
				require.Equal(t, len(proof)-1, i, "the path leaves the trie before the proof ends")

				return nil
			}

			v, nibbles = items[1], nibbles[len(path):]
		}
	}

	require.Fail(t, "the proof ends at a reference to a node it does not hold")

	return nil
}

// Prove's proofs verify for every key the trie holds and for keys it does not, in tries small
// enough that the root embeds everything and large enough for extension and embedded nodes.
func TestProveKeysPresentAndAbsent(t *testing.T) {
	for _, n := range []int{0, 1, 2, 16, 300} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			storage := NewMemoryStorage()
			state := NewState(storage)
			txn := state.newTrie().Txn(storage)
			batch := storage.Batch()
			txn.batch = batch

			values := map[string][]byte{}

			for i := 0; i < n; i++ {
				key := []byte(fmt.Sprintf("key-%d", i))
				// Short values make nodes under 32 bytes, which their parents embed.
				value := bytes.Repeat([]byte{byte(i + 1)}, 1+i%40)
				values[string(key)] = value

				txn.Insert(crypto.Keccak256(key), value)
			}

			rootBytes, err := txn.Hash()
			require.NoError(t, err)
			require.NoError(t, batch.Write())

			root := types.BytesToHash(rootBytes)

			for key, value := range values {
				proof, err := Prove(state.Get, root, crypto.Keccak256([]byte(key)))
				require.NoError(t, err)
				require.Equal(t, value, verifyProof(t, root, []byte(key), proof), key)
			}

			for i := 0; i < 50; i++ {
				key := []byte(fmt.Sprintf("absent-%d", i))
				proof, err := Prove(state.Get, root, crypto.Keccak256(key))
				require.NoError(t, err)

				if n == 0 {
					require.Empty(t, proof)

					continue
				}

				require.Nil(t, verifyProof(t, root, key, proof), string(key))
			}
		})
	}
}

// A node the storage lacks fails the proof instead of shortening it.
func TestProveMissingNode(t *testing.T) {
	_, err := Prove(NewState(NewMemoryStorage()).Get, types.StringToHash("0x01"), []byte{1})
	require.ErrorContains(t, err, "missing")
}
