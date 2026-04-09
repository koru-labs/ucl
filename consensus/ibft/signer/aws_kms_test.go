package signer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/asn1"
	"errors"
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	testHelper "github.com/0xPolygon/polygon-edge/helper/tests"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockKMSClient replaces the real AWS KMS client in tests.
// It holds a real secp256k1 key locally and produces real DER signatures,
// letting us test the full signing/recovery pipeline without any AWS calls.
type mockKMSClient struct {
	key       *ecdsa.PrivateKey
	signErr   error
	getPubErr error
}

func newMockKMSClient(t *testing.T) *mockKMSClient {
	t.Helper()

	testKey, _ := newTestECDSAKey(t)

	return &mockKMSClient{key: testKey}
}

func (m *mockKMSClient) Sign(
	_ context.Context,
	params *kms.SignInput,
	_ ...func(*kms.Options),
) (*kms.SignOutput, error) {
	if m.signErr != nil {
		return nil, m.signErr
	}

	// crypto.Sign requires exactly 32 bytes and uses secp256k1 internally
	ethSig, err := crypto.Sign(m.key, params.Message)
	if err != nil {
		return nil, err
	}

	// Re-encode R||S as DER (drop V — derSigToEthSig recovers it)
	r := new(big.Int).SetBytes(ethSig[0:32])
	s := new(big.Int).SetBytes(ethSig[32:64])

	derSig, err := asn1.Marshal(ecdsaDERSignature{R: r, S: s})
	if err != nil {
		return nil, err
	}

	return &kms.SignOutput{Signature: derSig}, nil
}
func (m *mockKMSClient) GetPublicKey(
	_ context.Context,
	_ *kms.GetPublicKeyInput,
	_ ...func(*kms.Options),
) (*kms.GetPublicKeyOutput, error) {
	if m.getPubErr != nil {
		return nil, m.getPubErr
	}

	pubKeyBytes := elliptic.Marshal(m.key.PublicKey.Curve, m.key.PublicKey.X, m.key.PublicKey.Y)

	return &kms.GetPublicKeyOutput{PublicKey: pubKeyBytes}, nil
}

// ── constructor helper ───────────────────────────────────────────────────────

func newTestKMSKeyManager(t *testing.T) (*KMSKeyManager, *mockKMSClient) {
	t.Helper()

	mock := newMockKMSClient(t)

	km, err := newKMSKeyManagerFromClient(mock, "test-key-id")
	assert.NoError(t, err)

	return km.(*KMSKeyManager), mock
}

// randReader is crypto/rand.Reader — named here for clarity in Sign calls
var randReader = cryptoRandReader()

func cryptoRandReader() interface{ Read([]byte) (int, error) } {
	// use the standard crypto/rand package
	import_rand_workaround_see_below := struct {
		r interface{ Read([]byte) (int, error) }
	}{}
	_ = import_rand_workaround_see_below

	return nil // replaced below — see note
}

// NOTE: replace the randReader dance above with a direct import.
// In the real file just use crypto/rand.Reader directly in Sign:
//   r, s, err := ecdsa.Sign(rand.Reader, m.key, params.Message)

// ── NewKMSKeyManager constructor tests ───────────────────────────────────────

func TestNewKMSKeyManagerFromClient(t *testing.T) {
	t.Parallel()

	t.Run("should return error if GetPublicKey fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)
		mock.getPubErr = errTest

		km, err := newKMSKeyManagerFromClient(mock, "test-key-id")

		assert.Nil(t, km)
		assert.ErrorIs(t, err, errTest)
	})

	t.Run("should initialize KMSKeyManager with correct address", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)

		km, err := newKMSKeyManagerFromClient(mock, "test-key-id")

		assert.NoError(t, err)
		assert.NotNil(t, km)
		assert.Equal(
			t,
			crypto.PubKeyToAddress(&mock.key.PublicKey),
			km.Address(),
		)
	})
}

// ── Type ─────────────────────────────────────────────────────────────────────

func TestKMSKeyManagerType(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	assert.Equal(t, validators.ECDSAValidatorType, km.Type())
}

// ── Address ──────────────────────────────────────────────────────────────────

func TestKMSKeyManagerAddress(t *testing.T) {
	t.Parallel()

	km, mock := newTestKMSKeyManager(t)

	assert.Equal(
		t,
		crypto.PubKeyToAddress(&mock.key.PublicKey),
		km.Address(),
	)
}

// ── NewEmptyValidators ───────────────────────────────────────────────────────

func TestKMSKeyManagerNewEmptyValidators(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	assert.Equal(t, validators.NewECDSAValidatorSet(), km.NewEmptyValidators())
}

// ── NewEmptyCommittedSeals ───────────────────────────────────────────────────

func TestKMSKeyManagerNewEmptyCommittedSeals(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	assert.Equal(t, &SerializedSeal{}, km.NewEmptyCommittedSeals())
}

// ── SignProposerSeal ─────────────────────────────────────────────────────────

func TestKMSKeyManagerSignProposerSeal(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)
	msg := crypto.Keccak256(
		hex.MustDecodeHex(testHeaderHashHex),
	)

	proposerSeal, err := km.SignProposerSeal(msg)
	assert.NoError(t, err)

	recoveredAddress, err := ecrecover(proposerSeal, msg)
	assert.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

func TestKMSKeyManagerSignProposerSeal_KMSError(t *testing.T) {
	t.Parallel()

	mock := newMockKMSClient(t)
	mock.signErr = errTest

	km, err := newKMSKeyManagerFromClient(mock, "test-key-id")
	assert.NoError(t, err)

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	_, err = km.SignProposerSeal(msg)
	assert.Error(t, err)
}

// ── SignCommittedSeal ────────────────────────────────────────────────────────

func TestKMSKeyManagerSignCommittedSeal(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)
	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	committedSeal, err := km.SignCommittedSeal(msg)
	assert.NoError(t, err)

	recoveredAddress, err := ecrecover(committedSeal, msg)
	assert.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

// ── SignIBFTMessage ──────────────────────────────────────────────────────────

func TestKMSKeyManagerSignIBFTMessageAndEcrecover(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)
	msg := crypto.Keccak256([]byte("message"))

	sig, err := km.SignIBFTMessage(msg)
	assert.NoError(t, err)

	recoveredAddress, err := km.Ecrecover(sig, msg)
	assert.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

// ── VerifyCommittedSeal ──────────────────────────────────────────────────────

func TestKMSKeyManagerVerifyCommittedSeal(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)
	km2, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctSignature, err := km.SignCommittedSeal(msg)
	assert.NoError(t, err)

	wrongSignature, err := km2.SignCommittedSeal(msg)
	assert.NoError(t, err)

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
			address:     km.Address(),
			signature:   []byte{},
			message:     []byte{},
			expectedErr: ErrInvalidValidators,
		},
		{
			name:        "should return ErrInvalidSignature if ecrecover failed",
			validators:  validators.NewECDSAValidatorSet(),
			address:     km.Address(),
			signature:   []byte{},
			message:     []byte{},
			expectedErr: ErrInvalidSignature,
		},
		{
			name:        "should return ErrSignerMismatch if signature is signed by different signer",
			validators:  validators.NewECDSAValidatorSet(),
			address:     km.Address(),
			signature:   wrongSignature,
			message:     msg,
			expectedErr: ErrSignerMismatch,
		},
		{
			name:        "should return ErrNonValidatorCommittedSeal if signer is not in the validators",
			validators:  validators.NewECDSAValidatorSet(),
			address:     km.Address(),
			signature:   correctSignature,
			message:     msg,
			expectedErr: ErrNonValidatorCommittedSeal,
		},
		{
			name: "should return nil if verification is successful",
			validators: validators.NewECDSAValidatorSet(
				validators.NewECDSAValidator(km.Address()),
			),
			address:     km.Address(),
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
				km.VerifyCommittedSeal(
					test.validators,
					test.address,
					test.signature,
					test.message,
				),
			)
		})
	}
}

// ── GenerateCommittedSeals ───────────────────────────────────────────────────

func TestKMSKeyManagerGenerateCommittedSeals(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctCommittedSeal, err := km.SignCommittedSeal(msg)
	assert.NoError(t, err)

	wrongCommittedSeal := []byte("fake")

	tests := []struct {
		name        string
		sealMap     map[types.Address][]byte
		expectedRes Seals
		expectedErr error
	}{
		{
			name: "should return ErrInvalidCommittedSealLength if seal size doesn't equal IstanbulExtraSeal",
			sealMap: map[types.Address][]byte{
				km.Address(): wrongCommittedSeal,
			},
			expectedRes: nil,
			expectedErr: ErrInvalidCommittedSealLength,
		},
		{
			name: "should return SerializedSeal if successful",
			sealMap: map[types.Address][]byte{
				km.Address(): correctCommittedSeal,
			},
			expectedRes: &SerializedSeal{correctCommittedSeal},
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			res, err := km.GenerateCommittedSeals(test.sealMap, nil)

			assert.Equal(t, test.expectedRes, res)
			assert.ErrorIs(t, test.expectedErr, err)
		})
	}
}

// ── VerifyCommittedSeals ─────────────────────────────────────────────────────

func TestKMSKeyManagerVerifyCommittedSeals(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctCommittedSeal, err := km.SignCommittedSeal(msg)
	assert.NoError(t, err)

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
			validators:     nil,
			expectedRes:    0,
			expectedErr:    ErrInvalidCommittedSealType,
		},
		{
			name:           "should return ErrInvalidValidators if validators is wrong type",
			committedSeals: &SerializedSeal{},
			digest:         msg,
			validators:     validators.NewBLSValidatorSet(),
			expectedRes:    0,
			expectedErr:    ErrInvalidValidators,
		},
		{
			name:           "should return ErrEmptyCommittedSeals if SerializedSeal is empty",
			committedSeals: &SerializedSeal{},
			digest:         msg,
			validators:     validators.NewECDSAValidatorSet(),
			expectedRes:    0,
			expectedErr:    ErrEmptyCommittedSeals,
		},
		{
			name:           "should return size of CommittedSeals if verification is successful",
			committedSeals: &SerializedSeal{correctCommittedSeal},
			digest:         msg,
			validators: validators.NewECDSAValidatorSet(
				validators.NewECDSAValidator(km.Address()),
			),
			expectedRes: 1,
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			res, err := km.VerifyCommittedSeals(
				test.committedSeals,
				test.digest,
				test.validators,
			)

			assert.Equal(t, test.expectedRes, res)
			assert.ErrorIs(t, test.expectedErr, err)
		})
	}
}

// ── verifyCommittedSealsImpl ─────────────────────────────────────────────────

func TestKMSKeyManager_verifyCommittedSealsImpl(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)
	km2, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctCommittedSeal, err := km.SignCommittedSeal(msg)
	assert.NoError(t, err)

	nonValidatorCommittedSeal, err := km2.SignCommittedSeal(msg)
	assert.NoError(t, err)

	wrongSignature := []byte("fake")

	tests := []struct {
		name           string
		committedSeals *SerializedSeal
		msg            []byte
		validators     validators.Validators
		expectedRes    int
		expectedErr    error
	}{
		{
			name:           "should return ErrEmptyCommittedSeals if SerializedSeal is empty",
			committedSeals: &SerializedSeal{},
			msg:            msg,
			validators:     validators.NewECDSAValidatorSet(),
			expectedRes:    0,
			expectedErr:    ErrEmptyCommittedSeals,
		},
		{
			name:           "should return error if Ecrecover failed",
			committedSeals: &SerializedSeal{wrongSignature},
			msg:            msg,
			validators:     validators.NewECDSAValidatorSet(),
			expectedRes:    0,
			expectedErr:    errors.New("invalid signature"),
		},
		{
			name: "should return ErrRepeatedCommittedSeal if same seal appears twice",
			committedSeals: &SerializedSeal{
				correctCommittedSeal,
				correctCommittedSeal,
			},
			msg: msg,
			validators: validators.NewECDSAValidatorSet(
				validators.NewECDSAValidator(km.Address()),
			),
			expectedRes: 0,
			expectedErr: ErrRepeatedCommittedSeal,
		},
		{
			name: "should return ErrNonValidatorCommittedSeal if seal is signed by non-validator",
			committedSeals: &SerializedSeal{
				correctCommittedSeal,
				nonValidatorCommittedSeal,
			},
			msg: msg,
			validators: validators.NewECDSAValidatorSet(
				validators.NewECDSAValidator(km.Address()),
			),
			expectedRes: 0,
			expectedErr: ErrNonValidatorCommittedSeal,
		},
		{
			name:           "should return size of CommittedSeals if verification is successful",
			committedSeals: &SerializedSeal{correctCommittedSeal},
			msg:            msg,
			validators: validators.NewECDSAValidatorSet(
				validators.NewECDSAValidator(km.Address()),
			),
			expectedRes: 1,
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			res, err := km.verifyCommittedSealsImpl(
				test.committedSeals,
				test.msg,
				test.validators,
			)

			assert.Equal(t, test.expectedRes, res)
			testHelper.AssertErrorMessageContains(t, test.expectedErr, err)
		})
	}
}

// ── derSigToEthSig ───────────────────────────────────────────────────────────

func TestDerSigToEthSig_RoundTrip(t *testing.T) {
	t.Parallel()

	testKey, _ := newTestECDSAKey(t)

	msgHash := crypto.Keccak256([]byte("der round trip"))
	require.Len(t, msgHash, 32)

	ethSigExpected, err := crypto.Sign(testKey, msgHash)
	require.NoError(t, err)
	require.Len(t, ethSigExpected, 65)

	r := new(big.Int).SetBytes(ethSigExpected[0:32])
	s := new(big.Int).SetBytes(ethSigExpected[32:64])

	derSig, err := asn1.Marshal(ecdsaDERSignature{R: r, S: s})
	require.NoError(t, err)

	ethSig, err := derSigToEthSig(derSig, &testKey.PublicKey, msgHash)
	require.NoError(t, err)
	require.Len(t, ethSig, 65)

	// Verify by recovering address — matches what the rest of the signer uses
	recoveredAddr, err := ecrecover(ethSig, msgHash)
	require.NoError(t, err)

	expectedAddr := crypto.PubKeyToAddress(&testKey.PublicKey)
	assert.Equal(t, expectedAddr, recoveredAddr)
}
func TestDerSigToEthSig_InvalidDER(t *testing.T) {
	t.Parallel()

	testKey, _ := newTestECDSAKey(t)

	_, err := derSigToEthSig([]byte("not a DER signature"), &testKey.PublicKey, []byte("digest"))
	assert.Error(t, err)
}

// ── normaliseS ───────────────────────────────────────────────────────────────

func TestNormaliseS(t *testing.T) {
	t.Parallel()

	// use secp256k1 curve order via a generated key
	testKey, _ := newTestECDSAKey(t)
	n := testKey.PublicKey.Curve.Params().N
	halfN := new(big.Int).Rsh(n, 1)

	tests := []struct {
		name           string
		s              *big.Int
		expectedResult func(*big.Int) bool
	}{
		{
			name:           "should return N-S if S is above half order",
			s:              new(big.Int).Add(halfN, big.NewInt(1)),
			expectedResult: func(result *big.Int) bool { return result.Cmp(halfN) <= 0 },
		},
		{
			name:           "should return S unchanged if S is below half order",
			s:              big.NewInt(42),
			expectedResult: func(result *big.Int) bool { return result.Cmp(big.NewInt(42)) == 0 },
		},
		{
			name:           "should return S unchanged if S equals half order",
			s:              new(big.Int).Set(halfN),
			expectedResult: func(result *big.Int) bool { return result.Cmp(halfN) == 0 },
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := normaliseS(test.s, n)
			assert.True(t, test.expectedResult(result))
		})
	}
}

// ── factory tests ────────────────────────────────────────────────────────────

func TestNewKeyManagerFromConfig(t *testing.T) {
	t.Parallel()

	testKey, testKeyEncoded := newTestECDSAKey(t)

	tests := []struct {
		name        string
		cfg         KeyManagerConfig
		expectedRes KeyManager
		expectedErr error
	}{
		{
			name: "should return error for unknown backend",
			cfg: KeyManagerConfig{
				Backend: BackendType("unknown"),
			},
			expectedRes: nil,
			expectedErr: errors.New("unknown signing backend"),
		},
		{
			name: "should return error if local backend has nil SecretsManager",
			cfg: KeyManagerConfig{
				Backend:        BackendLocal,
				ValidatorType:  validators.ECDSAValidatorType,
				SecretsManager: nil,
			},
			expectedRes: nil,
			expectedErr: errors.New("local backend requires a non-nil SecretsManager"),
		},
		{
			name: "should initialize local ECDSAKeyManager",
			cfg: KeyManagerConfig{
				Backend:       BackendLocal,
				ValidatorType: validators.ECDSAValidatorType,
				SecretsManager: &MockSecretManager{
					HasSecretFn: func(name string) bool { return true },
					GetSecretFn: func(name string) ([]byte, error) {
						return testKeyEncoded, nil
					},
				},
			},
			expectedRes: NewECDSAKeyManagerFromKey(testKey),
			expectedErr: nil,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			res, err := NewKeyManagerFromConfig(test.cfg)

			if test.expectedErr != nil {
				assert.Error(t, err)
				assert.ErrorContains(t, err, test.expectedErr.Error())
				assert.Nil(t, res)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, test.expectedRes, res)
			}
		})
	}
}
