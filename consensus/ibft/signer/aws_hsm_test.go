package signer

import (
	"crypto"
	"crypto/ecdsa"
	"encoding/asn1"
	"errors"
	"io"
	"math/big"
	"testing"

	polygoncrypto "github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	testHelper "github.com/0xPolygon/polygon-edge/helper/tests"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockHSMSigner implements hsmSigner using a local secp256k1 key.
// Produces real DER-encoded ECDSA signatures so the full
// sign → DER parse → Eth format → ecrecover pipeline is tested.
type mockHSMSigner struct {
	key     *ecdsa.PrivateKey
	signErr error
}

func newMockHSMSigner(t *testing.T) *mockHSMSigner {
	t.Helper()

	testKey, _ := newTestECDSAKey(t)

	return &mockHSMSigner{key: testKey}
}

func (m *mockHSMSigner) Public() crypto.PublicKey {
	return &m.key.PublicKey
}

func (m *mockHSMSigner) Sign(
	_ io.Reader,
	digest []byte,
	_ crypto.SignerOpts,
) ([]byte, error) {
	if m.signErr != nil {
		return nil, m.signErr
	}

	ethSig, err := polygoncrypto.Sign(m.key, digest)
	if err != nil {
		return nil, err
	}

	r := new(big.Int).SetBytes(ethSig[0:32])
	s := new(big.Int).SetBytes(ethSig[32:64])

	return asn1.Marshal(ecdsaDERSignature{R: r, S: s})
}

// nonECDSASigner returns a non-ECDSA public key to test type checking
type nonECDSASigner struct{}

func (n *nonECDSASigner) Public() crypto.PublicKey {
	return "not-an-ecdsa-key"
}

func (n *nonECDSASigner) Sign(_ io.Reader, _ []byte, _ crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func newTestHSMKeyManager(t *testing.T) (*HSMKeyManager, *mockHSMSigner) {
	t.Helper()

	signer := newMockHSMSigner(t)

	km, err := newHSMKeyManagerFromSigner(signer)
	require.NoError(t, err)

	return km.(*HSMKeyManager), signer
}

func TestNewHSMKeyManagerFromSigner(t *testing.T) {
	t.Parallel()

	t.Run("should initialize with correct address", func(t *testing.T) {
		t.Parallel()

		signer := newMockHSMSigner(t)

		km, err := newHSMKeyManagerFromSigner(signer)
		require.NoError(t, err)
		require.NotNil(t, km)

		assert.Equal(
			t,
			polygoncrypto.PubKeyToAddress(&signer.key.PublicKey),
			km.Address(),
		)
	})

	t.Run("should return error if signer public key is not ECDSA", func(t *testing.T) {
		t.Parallel()

		km, err := newHSMKeyManagerFromSigner(&nonECDSASigner{})
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "not ECDSA")
	})
}

func TestHSMKeyManagerType(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	assert.Equal(t, validators.ECDSAValidatorType, km.Type())
}

func TestHSMKeyManagerAddress(t *testing.T) {
	t.Parallel()

	km, signer := newTestHSMKeyManager(t)

	assert.Equal(
		t,
		polygoncrypto.PubKeyToAddress(&signer.key.PublicKey),
		km.Address(),
	)
}

func TestHSMKeyManagerNewEmptyValidators(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	assert.Equal(t, validators.NewECDSAValidatorSet(), km.NewEmptyValidators())
}

func TestHSMKeyManagerNewEmptyCommittedSeals(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	assert.Equal(t, &SerializedSeal{}, km.NewEmptyCommittedSeals())
}

func TestHSMKeyManagerSignProposerSeal(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	proposerSeal, err := km.SignProposerSeal(msg)
	require.NoError(t, err)

	recoveredAddress, err := ecrecover(proposerSeal, msg)
	require.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

func TestHSMKeyManagerSignProposerSeal_HSMError(t *testing.T) {
	t.Parallel()

	km, signer := newTestHSMKeyManager(t)
	signer.signErr = errTest

	msg := polygoncrypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	_, err := km.SignProposerSeal(msg)
	assert.ErrorContains(t, err, "ECDSA sign failed")
}

func TestHSMKeyManagerSignProposerSeal_InvalidDigestLength(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	_, err := km.SignProposerSeal([]byte("short"))
	assert.ErrorContains(t, err, "expected 32-byte digest")
}

func TestHSMKeyManagerSignIBFTMessageAndEcrecover(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)
	msg := polygoncrypto.Keccak256([]byte("message"))

	sig, err := km.SignIBFTMessage(msg)
	require.NoError(t, err)

	recoveredAddress, err := km.Ecrecover(sig, msg)
	require.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

func TestHSMKeyManagerSignCommittedSeal(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	committedSeal, err := km.SignCommittedSeal(msg)
	require.NoError(t, err)

	recoveredAddress, err := ecrecover(committedSeal, msg)
	require.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

//nolint:dupl
func TestHSMKeyManagerVerifyCommittedSeal(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	correctSignature, err := km1.SignCommittedSeal(msg)
	require.NoError(t, err)

	wrongSignature, err := km2.SignCommittedSeal(msg)
	require.NoError(t, err)

	tests := []struct {
		name        string
		validators  validators.Validators
		address     types.Address
		signature   []byte
		message     []byte
		expectedErr error
	}{
		{
			name:        "should return ErrInvalidValidators if validators is wrong type",
			validators:  validators.NewBLSValidatorSet(),
			address:     km1.Address(),
			signature:   []byte{},
			message:     []byte{},
			expectedErr: ErrInvalidValidators,
		},
		{
			name: "should return ErrSignerMismatch if signature is from wrong signer",
			validators: validators.NewECDSAValidatorSet(
				validators.NewECDSAValidator(km1.Address()),
			),
			address:     km1.Address(),
			signature:   wrongSignature,
			message:     msg,
			expectedErr: ErrSignerMismatch,
		},
		{
			name: "should return ErrNonValidatorCommittedSeal if address not in set",
			validators: validators.NewECDSAValidatorSet(
				validators.NewECDSAValidator(km2.Address()),
			),
			address:     km1.Address(),
			signature:   correctSignature,
			message:     msg,
			expectedErr: ErrNonValidatorCommittedSeal,
		},
		{
			name: "should return nil if verification is successful",
			validators: validators.NewECDSAValidatorSet(
				validators.NewECDSAValidator(km1.Address()),
			),
			address:     km1.Address(),
			signature:   correctSignature,
			message:     msg,
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.ErrorIs(
				t,
				test.expectedErr,
				km1.VerifyCommittedSeal(
					test.validators,
					test.address,
					test.signature,
					test.message,
				),
			)
		})
	}
}

func TestHSMKeyManagerGenerateCommittedSeals(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	seal1, err := km1.SignCommittedSeal(msg)
	require.NoError(t, err)

	seal2, err := km2.SignCommittedSeal(msg)
	require.NoError(t, err)

	tests := []struct {
		name        string
		sealMap     map[types.Address][]byte
		validators  validators.Validators
		expectedErr error
	}{
		{
			name: "should return error if seal length is invalid",
			sealMap: map[types.Address][]byte{
				km1.Address(): []byte("short"),
			},
			validators:  validators.NewECDSAValidatorSet(),
			expectedErr: ErrInvalidCommittedSealLength,
		},
		{
			name: "should return SerializedSeal if successful with one signer",
			sealMap: map[types.Address][]byte{
				km1.Address(): seal1,
			},
			validators:  validators.NewECDSAValidatorSet(),
			expectedErr: nil,
		},
		{
			name: "should return SerializedSeal if successful with two signers",
			sealMap: map[types.Address][]byte{
				km1.Address(): seal1,
				km2.Address(): seal2,
			},
			validators:  validators.NewECDSAValidatorSet(),
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			res, err := km1.GenerateCommittedSeals(test.sealMap, test.validators)

			testHelper.AssertErrorMessageContains(t, test.expectedErr, err)

			if test.expectedErr == nil {
				serialized, ok := res.(*SerializedSeal)
				require.True(t, ok)
				assert.Equal(t, len(test.sealMap), serialized.Num())
			}
		})
	}
}

func TestHSMKeyManagerVerifyCommittedSeals(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	seal1, err := km1.SignCommittedSeal(msg)
	require.NoError(t, err)

	seal2, err := km2.SignCommittedSeal(msg)
	require.NoError(t, err)

	seals, err := km1.GenerateCommittedSeals(
		map[types.Address][]byte{
			km1.Address(): seal1,
			km2.Address(): seal2,
		},
		validators.NewECDSAValidatorSet(),
	)
	require.NoError(t, err)

	valSet := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(km1.Address()),
		validators.NewECDSAValidator(km2.Address()),
	)

	tests := []struct {
		name           string
		committedSeals Seals
		digest         []byte
		validators     validators.Validators
		expectedRes    int
		expectedErr    error
	}{
		{
			name:           "should return ErrInvalidCommittedSealType if Seals is not *SerializedSeal",
			committedSeals: &AggregatedSeal{},
			digest:         msg,
			validators:     valSet,
			expectedRes:    0,
			expectedErr:    ErrInvalidCommittedSealType,
		},
		{
			name:           "should return ErrInvalidValidators if validators is wrong type",
			committedSeals: seals,
			digest:         msg,
			validators:     validators.NewBLSValidatorSet(),
			expectedRes:    0,
			expectedErr:    ErrInvalidValidators,
		},
		{
			name:           "should return ErrEmptyCommittedSeals if seal set is empty",
			committedSeals: &SerializedSeal{},
			digest:         msg,
			validators:     valSet,
			expectedRes:    0,
			expectedErr:    ErrEmptyCommittedSeals,
		},
		{
			name:           "should return count if verification is successful",
			committedSeals: seals,
			digest:         msg,
			validators:     valSet,
			expectedRes:    2,
			expectedErr:    nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			res, err := km1.VerifyCommittedSeals(
				test.committedSeals,
				test.digest,
				test.validators,
			)

			assert.Equal(t, test.expectedRes, res)
			testHelper.AssertErrorMessageContains(t, test.expectedErr, err)
		})
	}
}

//nolint:dupl
func TestHSMKeyManager_MultipleSignersAggregation(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)
	km3, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	sealMap := make(map[types.Address][]byte)

	for _, km := range []*HSMKeyManager{km1, km2, km3} {
		seal, err := km.SignCommittedSeal(msg)
		require.NoError(t, err)

		sealMap[km.Address()] = seal
	}

	valSet := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(km1.Address()),
		validators.NewECDSAValidator(km2.Address()),
		validators.NewECDSAValidator(km3.Address()),
	)

	seals, err := km1.GenerateCommittedSeals(sealMap, valSet)
	require.NoError(t, err)

	count, err := km1.VerifyCommittedSeals(seals, msg, valSet)
	require.NoError(t, err)

	assert.Equal(t, 3, count)
}

func TestHSMKeyManager_FullIBFTRound(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)

	headerHash := polygoncrypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	proposerSeal, err := km1.SignProposerSeal(headerHash)
	require.NoError(t, err)

	recoveredProposer, err := ecrecover(proposerSeal, headerHash)
	require.NoError(t, err)
	assert.Equal(t, km1.Address(), recoveredProposer)

	prepareMsg := polygoncrypto.Keccak256([]byte("prepare-message"))

	for _, km := range []*HSMKeyManager{km1, km2} {
		sig, err := km.SignIBFTMessage(prepareMsg)
		require.NoError(t, err)

		recovered, err := km.Ecrecover(sig, prepareMsg)
		require.NoError(t, err)
		assert.Equal(t, km.Address(), recovered)
	}

	commitHash := polygoncrypto.Keccak256(wrapCommitHash(headerHash))

	sealMap := make(map[types.Address][]byte)

	for _, km := range []*HSMKeyManager{km1, km2} {
		seal, err := km.SignCommittedSeal(commitHash)
		require.NoError(t, err)

		sealMap[km.Address()] = seal
	}

	valSet := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(km1.Address()),
		validators.NewECDSAValidator(km2.Address()),
	)

	seals, err := km1.GenerateCommittedSeals(sealMap, valSet)
	require.NoError(t, err)

	count, err := km1.VerifyCommittedSeals(seals, commitHash, valSet)
	require.NoError(t, err)

	assert.Equal(t, 2, count)
}

func TestExtractSecp256k1PubKey(t *testing.T) {
	t.Parallel()

	t.Run("should extract secp256k1 key from signer", func(t *testing.T) {
		t.Parallel()

		signer := newMockHSMSigner(t)

		pubKey, err := extractSecp256k1PubKey(signer)
		require.NoError(t, err)

		assert.Equal(t, signer.key.PublicKey.X, pubKey.X)
		assert.Equal(t, signer.key.PublicKey.Y, pubKey.Y)
		assert.Equal(t, polygoncrypto.S256, pubKey.Curve)
	})

	t.Run("should return error if signer is not ECDSA", func(t *testing.T) {
		t.Parallel()

		_, err := extractSecp256k1PubKey(&nonECDSASigner{})
		assert.ErrorContains(t, err, "not ECDSA")
	})
}
