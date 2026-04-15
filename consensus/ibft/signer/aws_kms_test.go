package signer

import (
	"context"
	"crypto/ecdsa"
	"encoding/asn1"
	"errors"
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	testHelper "github.com/0xPolygon/polygon-edge/helper/tests"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/Ethernal-Tech/kryptology/pkg/signatures/bls/bls_sig"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
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

	// Verify the caller is using the correct signing parameters
	if params.MessageType != kmstypes.MessageTypeDigest {
		return nil, errors.New("expected MessageType DIGEST")
	}

	// crypto.Sign uses secp256k1 internally — produces [R || S || V] (65 bytes)
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

	type algorithmIdentifier struct {
		Algorithm asn1.ObjectIdentifier
		Curve     asn1.ObjectIdentifier
	}

	type subjectPublicKeyInfo struct {
		Algorithm algorithmIdentifier
		PublicKey asn1.BitString
	}

	ecPublicKeyOID := asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	secp256k1OID := asn1.ObjectIdentifier{1, 3, 132, 0, 10}

	rawPoint := make([]byte, 65)
	rawPoint[0] = 0x04
	m.key.PublicKey.X.FillBytes(rawPoint[1:33])
	m.key.PublicKey.Y.FillBytes(rawPoint[33:65])

	spki := subjectPublicKeyInfo{
		Algorithm: algorithmIdentifier{
			Algorithm: ecPublicKeyOID,
			Curve:     secp256k1OID,
		},
		PublicKey: asn1.BitString{
			Bytes:     rawPoint,
			BitLength: len(rawPoint) * 8,
		},
	}

	derBytes, err := asn1.Marshal(spki)
	if err != nil {
		return nil, err
	}

	return &kms.GetPublicKeyOutput{PublicKey: derBytes}, nil
}

// mockBLSSecretsManager returns a pre-generated BLS key from GetSecret.
type mockBLSSecretsManager struct {
	secrets.SecretsManager
	blsKeyBytes []byte
	getErr      error
}

func newMockBLSSecretsManager(t *testing.T) (*mockBLSSecretsManager, *bls_sig.SecretKey) {
	t.Helper()
	blsKey, _ := newTestBLSKey(t)

	blsKeyBytes, err := blsKey.MarshalBinary()
	require.NoError(t, err)

	return &mockBLSSecretsManager{
		blsKeyBytes: blsKeyBytes,
	}, blsKey
}
func (m *mockBLSSecretsManager) GetSecret(name string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}

	if name == secrets.ValidatorBLSKey {
		return m.blsKeyBytes, nil
	}

	return nil, errors.New("unknown secret: " + name)
}

func newTestKMSKeyManager(t *testing.T) (*KMSKeyManager, *mockKMSClient, *bls_sig.SecretKey) {
	t.Helper()

	mock := newMockKMSClient(t)
	ssmMock, blsKey := newMockBLSSecretsManager(t)

	km, err := newKMSKeyManagerFromClient(mock, "test-key-id", ssmMock)
	require.NoError(t, err)

	return km.(*KMSKeyManager), mock, blsKey
}

// helper: convert KMSKeyManager to BLSValidator for test validator sets
func testKMSKeyManagerToBLSValidator(t *testing.T, km *KMSKeyManager) *validators.BLSValidator {
	t.Helper()

	pubkeyBytes, err := crypto.BLSSecretKeyToPubkeyBytes(km.blsKey)
	require.NoError(t, err)

	return validators.NewBLSValidator(km.Address(), pubkeyBytes)
}

func TestNewKMSKeyManagerFromClient(t *testing.T) {
	t.Parallel()

	t.Run("should return error if GetPublicKey fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)
		mock.getPubErr = errTest

		ssmMock, _ := newMockBLSSecretsManager(t)

		km, err := newKMSKeyManagerFromClient(mock, "test-key-id", ssmMock)
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "failed to fetch KMS public key")
	})

	t.Run("should return error if BLS key loading fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)
		ssmMock := &mockBLSSecretsManager{getErr: errTest}

		km, err := newKMSKeyManagerFromClient(mock, "test-key-id", ssmMock)
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "failed to load BLS key from SSM")
	})

	t.Run("should return error if BLS key is empty", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)
		ssmMock := &mockBLSSecretsManager{blsKeyBytes: []byte{}}

		km, err := newKMSKeyManagerFromClient(mock, "test-key-id", ssmMock)
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "empty BLS key")
	})

	t.Run("should initialize KMSKeyManager with correct address", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)
		ssmMock, _ := newMockBLSSecretsManager(t)

		km, err := newKMSKeyManagerFromClient(mock, "test-key-id", ssmMock)
		require.NoError(t, err)
		require.NotNil(t, km)

		assert.Equal(
			t,
			crypto.PubKeyToAddress(&mock.key.PublicKey),
			km.Address(),
		)
	})
}

func TestKMSKeyManagerType(t *testing.T) {
	t.Parallel()

	km, _, _ := newTestKMSKeyManager(t)

	assert.Equal(t, validators.BLSValidatorType, km.Type())
}

func TestKMSKeyManagerAddress(t *testing.T) {
	t.Parallel()

	km, mock, _ := newTestKMSKeyManager(t)

	assert.Equal(
		t,
		crypto.PubKeyToAddress(&mock.key.PublicKey),
		km.Address(),
	)
}

func TestKMSKeyManagerNewEmptyValidators(t *testing.T) {
	t.Parallel()

	km, _, _ := newTestKMSKeyManager(t)

	assert.Equal(t, validators.NewBLSValidatorSet(), km.NewEmptyValidators())
}

func TestKMSKeyManagerNewEmptyCommittedSeals(t *testing.T) {
	t.Parallel()

	km, _, _ := newTestKMSKeyManager(t)

	assert.Equal(t, &AggregatedSeal{}, km.NewEmptyCommittedSeals())
}

func TestKMSKeyManagerSignProposerSeal(t *testing.T) {
	t.Parallel()

	km, _, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
		hex.MustDecodeHex(testHeaderHashHex),
	)

	proposerSeal, err := km.SignProposerSeal(msg)
	require.NoError(t, err)

	recoveredAddress, err := ecrecover(proposerSeal, msg)
	require.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

func TestKMSKeyManagerSignProposerSeal_KMSError(t *testing.T) {
	t.Parallel()

	km, mock, _ := newTestKMSKeyManager(t)
	mock.signErr = errTest

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	_, err := km.SignProposerSeal(msg)
	assert.ErrorContains(t, err, "KMS sign request failed")
}

func TestKMSKeyManagerSignIBFTMessageAndEcrecover(t *testing.T) {
	t.Parallel()

	km, _, _ := newTestKMSKeyManager(t)
	msg := crypto.Keccak256([]byte("message"))

	sig, err := km.SignIBFTMessage(msg)
	require.NoError(t, err)

	recoveredAddress, err := km.Ecrecover(sig, msg)
	require.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

func TestKMSKeyManagerSignCommittedSeal(t *testing.T) {
	t.Parallel()

	km, _, blsSecretKey := newTestKMSKeyManager(t)

	blsPubKey, err := blsSecretKey.GetPublicKey()
	require.NoError(t, err)

	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	committedSealBytes, err := km.SignCommittedSeal(msg)
	require.NoError(t, err)

	// unmarshal and check against the BLS public key
	committedSeal, err := crypto.UnmarshalBLSSignature(committedSealBytes)
	require.NoError(t, err)

	assert.NoError(
		t,
		crypto.VerifyBLSSignature(blsPubKey, committedSeal, msg),
	)
}

func TestKMSKeyManagerVerifyCommittedSeal(t *testing.T) {
	t.Parallel()

	km1, _, _ := newTestKMSKeyManager(t)
	km2, _, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctSignature, err := km1.SignCommittedSeal(msg)
	require.NoError(t, err)

	wrongSignature, err := km2.SignCommittedSeal(msg)
	require.NoError(t, err)

	blsPubKeyBytes, err := crypto.BLSSecretKeyToPubkeyBytes(km1.blsKey)
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
				testKMSKeyManagerToBLSValidator(t, km2),
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
			expectedErr: crypto.ErrInvalidBLSSignature,
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

func TestKMSKeyManagerGenerateCommittedSeals(t *testing.T) {
	t.Parallel()

	km1, _, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	correctCommittedSeal, err := km1.SignCommittedSeal(msg)
	require.NoError(t, err)

	// Build expected aggregated signature (single signer)
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
				testKMSKeyManagerToBLSValidator(t, km1),
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

func TestKMSKeyManagerVerifyCommittedSeals(t *testing.T) {
	t.Parallel()

	km1, _, _ := newTestKMSKeyManager(t)
	km2, _, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
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
				testKMSKeyManagerToBLSValidator(t, km1),
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
				testKMSKeyManagerToBLSValidator(t, km1),
				testKMSKeyManagerToBLSValidator(t, km2),
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
				testKMSKeyManagerToBLSValidator(t, km1),
				testKMSKeyManagerToBLSValidator(t, km2),
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

	recoveredAddr, err := ecrecover(ethSig, msgHash)
	require.NoError(t, err)

	expectedAddr := crypto.PubKeyToAddress(&testKey.PublicKey)
	assert.Equal(t, expectedAddr, recoveredAddr)
}

func TestDerSigToEthSig_InvalidDER(t *testing.T) {
	t.Parallel()

	testKey, _ := newTestECDSAKey(t)

	_, err := derSigToEthSig(
		[]byte("not a DER signature"),
		&testKey.PublicKey,
		make([]byte, 32), // valid 32-byte digest
	)
	assert.ErrorContains(t, err, "failed to unmarshal DER signature")
}

func TestDerSigToEthSig_InvalidDigestLength(t *testing.T) {
	t.Parallel()

	testKey, _ := newTestECDSAKey(t)

	_, err := derSigToEthSig(
		[]byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01}, // minimal valid DER
		&testKey.PublicKey,
		[]byte("short"), // not 32 bytes
	)
	assert.ErrorContains(t, err, "digest must be 32 bytes")
}

func TestNormaliseS(t *testing.T) {
	t.Parallel()

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

func TestFetchKMSPublicKey(t *testing.T) {
	t.Parallel()

	t.Run("should parse SPKI and return secp256k1 key", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)

		pubKey, err := fetchKMSPublicKey(mock, "test-key-id")
		require.NoError(t, err)

		assert.Equal(t, mock.key.PublicKey.X, pubKey.X)
		assert.Equal(t, mock.key.PublicKey.Y, pubKey.Y)
		assert.Equal(t, crypto.S256, pubKey.Curve)
	})

	t.Run("should return error if GetPublicKey fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)
		mock.getPubErr = errTest

		pubKey, err := fetchKMSPublicKey(mock, "test-key-id")
		assert.Nil(t, pubKey)
		assert.ErrorContains(t, err, "GetPublicKey failed")
	})

	t.Run("should return error for non-secp256k1 SPKI", func(t *testing.T) {
		t.Parallel()

		// Return SPKI with P-256 OID instead of secp256k1
		mock := &mockKMSClient{
			key: func() *ecdsa.PrivateKey {
				k, _ := newTestECDSAKey(t)
				return k
			}(),
		}

		// Override GetPublicKey to return wrong curve OID
		wrongCurveMock := &wrongCurveKMSClient{inner: mock}

		pubKey, err := fetchKMSPublicKey(wrongCurveMock, "test-key-id")
		assert.Nil(t, pubKey)
		assert.ErrorContains(t, err, "unexpected curve OID")
	})
}

// wrongCurveKMSClient returns SPKI with P-256 OID
type wrongCurveKMSClient struct {
	inner *mockKMSClient
}

func (w *wrongCurveKMSClient) Sign(
	ctx context.Context,
	params *kms.SignInput,
	optFns ...func(*kms.Options),
) (*kms.SignOutput, error) {
	return w.inner.Sign(ctx, params, optFns...)
}

func (w *wrongCurveKMSClient) GetPublicKey(
	_ context.Context,
	_ *kms.GetPublicKeyInput,
	_ ...func(*kms.Options),
) (*kms.GetPublicKeyOutput, error) {
	type algorithmIdentifier struct {
		Algorithm asn1.ObjectIdentifier
		Curve     asn1.ObjectIdentifier
	}

	type subjectPublicKeyInfo struct {
		Algorithm algorithmIdentifier
		PublicKey asn1.BitString
	}

	rawPoint := make([]byte, 65)
	rawPoint[0] = 0x04
	w.inner.key.PublicKey.X.FillBytes(rawPoint[1:33])
	w.inner.key.PublicKey.Y.FillBytes(rawPoint[33:65])

	spki := subjectPublicKeyInfo{
		Algorithm: algorithmIdentifier{
			Algorithm: asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1},
			Curve:     asn1.ObjectIdentifier{1, 2, 840, 10045, 3, 1, 7}, // P-256, not secp256k1
		},
		PublicKey: asn1.BitString{
			Bytes:     rawPoint,
			BitLength: len(rawPoint) * 8,
		},
	}

	derBytes, err := asn1.Marshal(spki)
	if err != nil {
		return nil, err
	}

	return &kms.GetPublicKeyOutput{PublicKey: derBytes}, nil
}

func TestBlsKeyFromSSM(t *testing.T) {
	t.Parallel()

	t.Run("should load and deserialize BLS key", func(t *testing.T) {
		t.Parallel()

		ssmMock, expectedKey := newMockBLSSecretsManager(t)

		key, err := blsKeyFromSSM(ssmMock)
		require.NoError(t, err)

		// Compare marshalled bytes since SecretKey may not be directly comparable
		expectedBytes, err := expectedKey.MarshalBinary()
		require.NoError(t, err)

		actualBytes, err := key.MarshalBinary()
		require.NoError(t, err)

		assert.Equal(t, expectedBytes, actualBytes)
	})

	t.Run("should return error if GetSecret fails", func(t *testing.T) {
		t.Parallel()

		ssmMock := &mockBLSSecretsManager{getErr: errTest}

		key, err := blsKeyFromSSM(ssmMock)
		assert.Nil(t, key)
		assert.ErrorIs(t, err, errTest)
	})

	t.Run("should return error if BLS key bytes are empty", func(t *testing.T) {
		t.Parallel()

		ssmMock := &mockBLSSecretsManager{blsKeyBytes: []byte{}}

		key, err := blsKeyFromSSM(ssmMock)
		assert.Nil(t, key)
		assert.ErrorContains(t, err, "empty BLS key")
	})

	t.Run("should return error if BLS key bytes are invalid", func(t *testing.T) {
		t.Parallel()

		ssmMock := &mockBLSSecretsManager{blsKeyBytes: []byte("invalid-bls-key")}

		key, err := blsKeyFromSSM(ssmMock)
		assert.Nil(t, key)
		assert.ErrorContains(t, err, "UnmarshalBinary BLS key")
	})
}

func TestKMSKeyManager_MultipleSignersAggregation(t *testing.T) {
	t.Parallel()

	km1, _, _ := newTestKMSKeyManager(t)
	km2, _, _ := newTestKMSKeyManager(t)
	km3, _, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(
		wrapCommitHash(
			hex.MustDecodeHex(testHeaderHashHex),
		),
	)

	// Each validator signs the committed seal
	sealMap := make(map[types.Address][]byte)

	for _, km := range []*KMSKeyManager{km1, km2, km3} {
		seal, err := km.SignCommittedSeal(msg)
		require.NoError(t, err)

		sealMap[km.Address()] = seal
	}

	// Build validator set
	valSet := validators.NewBLSValidatorSet(
		testKMSKeyManagerToBLSValidator(t, km1),
		testKMSKeyManagerToBLSValidator(t, km2),
		testKMSKeyManagerToBLSValidator(t, km3),
	)

	// Generate aggregated seal
	seals, err := km1.GenerateCommittedSeals(sealMap, valSet)
	require.NoError(t, err)

	// Verify aggregated seal
	count, err := km1.VerifyCommittedSeals(seals, msg, valSet)
	require.NoError(t, err)

	assert.Equal(t, 3, count)
}
