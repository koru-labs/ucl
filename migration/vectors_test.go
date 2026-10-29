// Package migration_test writes the vectors pitau's ucl follower is tested against: IBFT headers with
// real seals and their ucl hashes, and exported block records. Run with UPDATE_MIGRATION_VECTORS=1
// to rewrite testdata/; without it the test checks that the files still hold what ucl's code
// computes.
package migration_test

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/stretchr/testify/require"
)

// ibftVector is one sealed IBFT header.
type ibftVector struct {
	Number     uint64   `json:"number"`
	HeaderRLP  string   `json:"header_rlp"`
	Hash       string   `json:"hash"`
	Validators []string `json:"validators"`
	Signers    []string `json:"signers"`
}

func key(t *testing.T, i byte) *signer.SignerImpl {
	t.Helper()

	raw := make([]byte, 32)
	raw[31] = i + 1

	priv, err := crypto.BytesToECDSAPrivateKey([]byte(hex.EncodeToString(raw)))
	require.NoError(t, err)

	manager := signer.NewECDSAKeyManagerFromKey(priv)

	return signer.NewSigner(manager, manager)
}

func sealedHeader(t *testing.T, number uint64, parentSeals signer.Seals, signers []*signer.SignerImpl) (*types.Header, types.Hash) {
	t.Helper()

	set := validators.NewECDSAValidatorSet()
	for _, s := range signers {
		require.NoError(t, set.Add(validators.NewECDSAValidator(s.Address())))
	}

	header := &types.Header{
		ParentHash:   types.StringToHash("0x0101"),
		Sha3Uncles:   types.EmptyUncleHash,
		StateRoot:    types.StringToHash("0x0202"),
		TxRoot:       types.EmptyRootHash,
		ReceiptsRoot: types.EmptyRootHash,
		Difficulty:   number,
		Number:       number,
		GasLimit:     200_000_000,
		GasUsed:      21_000,
		Timestamp:    1_789_732_558 + number,
		MixHash:      types.StringToHash("0x63746963616c2062797a616e74696e65206661756c7420746f6c6572616e6365"),
		BaseFee:      1_000_000_000,
	}
	proposer := signers[0]
	proposer.InitIBFTExtra(header, set, parentSeals)

	header, err := proposer.WriteProposerSeal(header)
	require.NoError(t, err)

	hash, err := proposer.CalculateHeaderHash(header)
	require.NoError(t, err)

	// Three of four validators commit: IBFT's quorum. As go-ibft does, they sign the proposal hash:
	// keccak(hash ‖ round), round 2 here (consensus/ibft/verifier.go, calculateProposalHash).
	round := make([]byte, 8)
	binary.BigEndian.PutUint64(round, 2)
	proposalHash := crypto.Keccak256(hash.Bytes(), round)

	seals := map[types.Address][]byte{}

	for _, s := range signers[:3] {
		seal, err := s.CreateCommittedSeal(proposalHash)
		require.NoError(t, err)

		seals[s.Address()] = seal
	}

	header, err = proposer.WriteCommittedSeals(header, 2, seals)
	require.NoError(t, err)

	again, err := proposer.CalculateHeaderHash(header)
	require.NoError(t, err)
	require.Equal(t, hash, again, "seals are outside the hash")

	// What ucl's chain checks on import: the seals verify over the proposal hash with the round.
	extra, err := proposer.GetIBFTExtra(header)
	require.NoError(t, err)
	require.NotNil(t, extra.RoundNumber)
	require.Equal(t, uint64(2), *extra.RoundNumber)
	require.NoError(t, proposer.VerifyCommittedSeals(types.BytesToHash(proposalHash), extra.CommittedSeals, set, 3))

	header.Hash = hash

	return header, hash
}

func TestMigrationVectors(t *testing.T) {
	signers := []*signer.SignerImpl{key(t, 0), key(t, 1), key(t, 2), key(t, 3)}

	vectors := make([]ibftVector, 0, 2)

	// Block 1 has no parent seals; later blocks carry the parent's committed seals.
	first, firstHash := sealedHeader(t, 1, nil, signers)

	parentExtra, err := signers[0].GetIBFTExtra(first)
	require.NoError(t, err)

	second, _ := sealedHeader(t, 2, parentExtra.CommittedSeals, signers)
	_ = firstHash

	for _, header := range []*types.Header{first, second} {
		v := ibftVector{
			Number:    header.Number,
			HeaderRLP: hex.EncodeToString(header.MarshalRLP()),
			Hash:      header.Hash.String(),
		}

		for _, s := range signers {
			v.Validators = append(v.Validators, s.Address().String())
		}

		for _, s := range signers[:3] {
			v.Signers = append(v.Signers, s.Address().String())
		}

		vectors = append(vectors, v)
	}

	path := filepath.Join("testdata", "ibft_headers.json")

	if os.Getenv("UPDATE_MIGRATION_VECTORS") == "1" {
		data, err := json.MarshalIndent(vectors, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))

		return
	}

	// ucl writes committed seals in map order, so files differ between runs; the check is that the
	// stored headers still hash and verify under ucl's code.
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var stored []ibftVector
	require.NoError(t, json.Unmarshal(data, &stored))
	require.Len(t, stored, len(vectors))

	for i, v := range stored {
		raw, err := hex.DecodeString(v.HeaderRLP)
		require.NoError(t, err)

		header := &types.Header{}
		require.NoError(t, header.UnmarshalRLP(raw))

		hash, err := signers[0].CalculateHeaderHash(header)
		require.NoError(t, err)
		require.Equal(t, v.Hash, hash.String())
		// Block 2 hashes block 1's seals, whose order varies between runs; block 1 is stable.
		if v.Number == 1 {
			require.Equal(t, vectors[i].Hash, v.Hash)
		}

		extra, err := signers[0].GetIBFTExtra(header)
		require.NoError(t, err)

		round := make([]byte, 8)
		binary.BigEndian.PutUint64(round, *extra.RoundNumber)
		require.NoError(t, signers[0].VerifyCommittedSeals(
			types.BytesToHash(crypto.Keccak256(hash.Bytes(), round)), extra.CommittedSeals, extra.Validators, 3))
	}
}
