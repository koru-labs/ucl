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

	// Use polygon-edge's crypto.Sign which produces [R||S||V] (65 bytes)
	ethSig, err := polygoncrypto.Sign(m.key, digest)
	if err != nil {
		return nil, err
	}

	// Re-encode as DER — this is what a real HSM returns
	r := new(big.Int).SetBytes(ethSig[0:32])
	s := new(big.Int).SetBytes(ethSig[32:64])

	return asn1.Marshal(ecdsaDERSignature{R: r, S: s})
}

func newTestHSMKeyManager(t *testing.T) (*HSMKeyManager, *mockHSMSigner) {
	t.Helper()

	signer := newMockHSMSigner(t)
	ssmMock, _ := newMockBLSSecretsManager(t)

	km, err := newHSMKeyManagerFromSigner(signer, ssmMock)
	require.NoError(t, err)

	return km.(*HSMKeyManager), signer
}

// helper: convert HSMKeyManager to BLSValidator for test validator sets
func testHSMKeyManagerToBLSValidator(t *testing.T, km *HSMKeyManager) *validators.BLSValidator {
	t.Helper()

	pubkeyBytes, err := polygoncrypto.BLSSecretKeyToPubkeyBytes(km.blsKey)
	require.NoError(t, err)

	return validators.NewBLSValidator(km.Address(), pubkeyBytes)
}

func TestNewHSMKeyManagerFromSigner(t *testing.T) {
	t.Parallel()

	t.Run("should initialize with correct address", func(t *testing.T) {
		t.Parallel()

		signer := newMockHSMSigner(t)
		ssmMock, _ := newMockBLSSecretsManager(t)

		km, err := newHSMKeyManagerFromSigner(signer, ssmMock)
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

		ssmMock, _ := newMockBLSSecretsManager(t)

		km, err := newHSMKeyManagerFromSigner(&nonECDSASigner{}, ssmMock)
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "not ECDSA")
	})

	t.Run("should return error if BLS key loading fails", func(t *testing.T) {
		t.Parallel()

		signer := newMockHSMSigner(t)
		ssmMock := &mockBLSSecretsManager{getErr: errTest}

		km, err := newHSMKeyManagerFromSigner(signer, ssmMock)
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "failed to load BLS key from SSM")
	})

	t.Run("should return error if BLS key is empty", func(t *testing.T) {
		t.Parallel()

		signer := newMockHSMSigner(t)
		ssmMock := &mockBLSSecretsManager{blsKeyBytes: []byte{}}

		km, err := newHSMKeyManagerFromSigner(signer, ssmMock)
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "empty BLS key")
	})

	t.Run("should return error if BLS key bytes are invalid", func(t *testing.T) {
		t.Parallel()

		signer := newMockHSMSigner(t)
		ssmMock := &mockBLSSecretsManager{blsKeyBytes: []byte("not-32-bytes")}

		km, err := newHSMKeyManagerFromSigner(signer, ssmMock)
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "UnmarshalBinary BLS key")
	})
}

// nonECDSASigner returns a non-ECDSA public key to test type checking
type nonECDSASigner struct{}

func (n *nonECDSASigner) Public() crypto.PublicKey {
	return "not-an-ecdsa-key" // string, not *ecdsa.PublicKey
}

func (n *nonECDSASigner) Sign(_ io.Reader, _ []byte, _ crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func TestHSMKeyManagerType(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	assert.Equal(t, validators.BLSValidatorType, km.Type())
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

	assert.Equal(t, validators.NewBLSValidatorSet(), km.NewEmptyValidators())
}

func TestHSMKeyManagerNewEmptyCommittedSeals(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	assert.Equal(t, &AggregatedSeal{}, km.NewEmptyCommittedSeals())
}

func TestHSMKeyManagerSignProposerSeal(t *testing.T) {
	t.Parallel()

	km, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(
		hex.MustDecodeHex(testHeaderHashHex),
	)

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

	blsPubKey, err := km.blsKey.GetPublicKey()
	require.NoError(t, err)

	msg := polygoncrypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	sealBytes, err := km.SignCommittedSeal(msg)
	require.NoError(t, err)

	seal, err := polygoncrypto.UnmarshalBLSSignature(sealBytes)
	require.NoError(t, err)

	assert.NoError(
		t,
		polygoncrypto.VerifyBLSSignature(blsPubKey, seal, msg),
	)
}

func TestHSMKeyManagerVerifyCommittedSeal(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctSignature, err := km1.SignCommittedSeal(msg)
	require.NoError(t, err)

	wrongSignature, err := km2.SignCommittedSeal(msg)
	require.NoError(t, err)

	blsPubKeyBytes, err := polygoncrypto.BLSSecretKeyToPubkeyBytes(km1.blsKey)
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
			validators:  validators.NewECDSAValidatorSet(),
			address:     km1.Address(),
			signature:   []byte{},
			message:     []byte{},
			expectedErr: ErrInvalidValidators,
		},
		{
			name: "should return ErrValidatorNotFound if address is not in validators",
			validators: validators.NewBLSValidatorSet(
				testHSMKeyManagerToBLSValidator(t, km2),
			),
			address:     km1.Address(),
			signature:   []byte{},
			message:     []byte{},
			expectedErr: ErrValidatorNotFound,
		},
		{
			name: "should return error if signature is wrong",
			validators: validators.NewBLSValidatorSet(
				validators.NewBLSValidator(km1.Address(), blsPubKeyBytes),
			),
			address:     km1.Address(),
			signature:   wrongSignature,
			message:     msg,
			expectedErr: polygoncrypto.ErrInvalidBLSSignature,
		},
		{
			name: "should return nil if verification is successful",
			validators: validators.NewBLSValidatorSet(
				validators.NewBLSValidator(km1.Address(), blsPubKeyBytes),
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

	msg := polygoncrypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctCommittedSeal, err := km1.SignCommittedSeal(msg)
	require.NoError(t, err)

	aggregatedBLSSigBytes := testCreateAggregatedSignature(t, msg, km1)

	tests := []struct {
		name        string
		sealMap     map[types.Address][]byte
		validators  validators.Validators
		expectedRes Seals
		expectedErr error
	}{
		{
			name:        "should return ErrInvalidValidators if validators is wrong type",
			sealMap:     nil,
			validators:  validators.NewECDSAValidatorSet(),
			expectedRes: nil,
			expectedErr: ErrInvalidValidators,
		},
		{
			name: "should return error if signer is not in validators",
			sealMap: map[types.Address][]byte{
				km1.Address(): correctCommittedSeal,
			},
			validators:  validators.NewBLSValidatorSet(),
			expectedRes: nil,
			expectedErr: ErrNonValidatorCommittedSeal,
		},
		{
			name:        "should return error if sealMap is empty",
			sealMap:     map[types.Address][]byte{},
			validators:  validators.NewBLSValidatorSet(),
			expectedRes: nil,
			expectedErr: errors.New("at least one signature is required"),
		},
		{
			name: "should return AggregatedSeal if successful",
			sealMap: map[types.Address][]byte{
				km1.Address(): correctCommittedSeal,
			},
			validators: validators.NewBLSValidatorSet(
				testHSMKeyManagerToBLSValidator(t, km1),
			),
			expectedRes: &AggregatedSeal{
				Bitmap:    new(big.Int).SetBit(new(big.Int), 0, 1),
				Signature: aggregatedBLSSigBytes,
			},
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			res, err := km1.GenerateCommittedSeals(test.sealMap, test.validators)

			assert.Equal(t, test.expectedRes, res)
			testHelper.AssertErrorMessageContains(t, test.expectedErr, err)
		})
	}
}

func TestHSMKeyManagerVerifyCommittedSeals(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctAggregatedSig := testCreateAggregatedSignature(t, msg, km1, km2)

	wrongAggregatedSig := testCreateAggregatedSignature(
		t,
		[]byte("fake"),
		km1, km2,
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
			name:           "should return ErrInvalidCommittedSealType if Seals is not *AggregatedSeal",
			committedSeals: &SerializedSeal{},
			digest:         msg,
			validators:     nil,
			expectedRes:    0,
			expectedErr:    ErrInvalidCommittedSealType,
		},
		{
			name: "should return ErrInvalidValidators if validators is wrong type",
			committedSeals: &AggregatedSeal{
				Bitmap:    new(big.Int).SetBytes([]byte{0x3}),
				Signature: correctAggregatedSig,
			},
			digest:      msg,
			validators:  validators.NewECDSAValidatorSet(),
			expectedRes: 0,
			expectedErr: ErrInvalidValidators,
		},
		{
			name: "should return ErrEmptyCommittedSeals if signature is empty",
			committedSeals: &AggregatedSeal{
				Bitmap:    new(big.Int).SetBit(new(big.Int), 0, 1),
				Signature: []byte{},
			},
			digest: msg,
			validators: validators.NewBLSValidatorSet(
				testHSMKeyManagerToBLSValidator(t, km1),
			),
			expectedRes: 0,
			expectedErr: ErrEmptyCommittedSeals,
		},
		{
			name: "should return ErrInvalidSignature if message is different",
			committedSeals: &AggregatedSeal{
				Bitmap:    new(big.Int).SetBytes([]byte{0x3}),
				Signature: wrongAggregatedSig,
			},
			digest: msg,
			validators: validators.NewBLSValidatorSet(
				testHSMKeyManagerToBLSValidator(t, km1),
				testHSMKeyManagerToBLSValidator(t, km2),
			),
			expectedRes: 0,
			expectedErr: ErrInvalidSignature,
		},
		{
			name: "should return count if verification is successful",
			committedSeals: &AggregatedSeal{
				Bitmap:    new(big.Int).SetBytes([]byte{0x3}),
				Signature: correctAggregatedSig,
			},
			digest: msg,
			validators: validators.NewBLSValidatorSet(
				testHSMKeyManagerToBLSValidator(t, km1),
				testHSMKeyManagerToBLSValidator(t, km2),
			),
			expectedRes: 2,
			expectedErr: nil,
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

func TestHSMKeyManager_MultipleSignersAggregation(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)
	km3, _ := newTestHSMKeyManager(t)

	msg := polygoncrypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	// Each validator signs the committed seal
	sealMap := make(map[types.Address][]byte)

	for _, km := range []*HSMKeyManager{km1, km2, km3} {
		seal, err := km.SignCommittedSeal(msg)
		require.NoError(t, err)

		sealMap[km.Address()] = seal
	}

	// Build validator set
	valSet := validators.NewBLSValidatorSet(
		testHSMKeyManagerToBLSValidator(t, km1),
		testHSMKeyManagerToBLSValidator(t, km2),
		testHSMKeyManagerToBLSValidator(t, km3),
	)

	// Generate aggregated seal
	seals, err := km1.GenerateCommittedSeals(sealMap, valSet)
	require.NoError(t, err)

	// Verify aggregated seal
	count, err := km1.VerifyCommittedSeals(seals, msg, valSet)
	require.NoError(t, err)

	assert.Equal(t, 3, count)
}

func TestHSMKeyManager_FullIBFTRound(t *testing.T) {
	t.Parallel()

	km1, _ := newTestHSMKeyManager(t)
	km2, _ := newTestHSMKeyManager(t)

	headerHash := polygoncrypto.Keccak256(
		hex.MustDecodeHex(testHeaderHashHex),
	)

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

	commitHash := polygoncrypto.Keccak256(
		wrapCommitHash(headerHash),
	)

	sealMap := make(map[types.Address][]byte)

	for _, km := range []*HSMKeyManager{km1, km2} {
		seal, err := km.SignCommittedSeal(commitHash)
		require.NoError(t, err)

		sealMap[km.Address()] = seal
	}

	valSet := validators.NewBLSValidatorSet(
		testHSMKeyManagerToBLSValidator(t, km1),
		testHSMKeyManagerToBLSValidator(t, km2),
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
