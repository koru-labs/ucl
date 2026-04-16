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
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
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

	if params.MessageType != kmstypes.MessageTypeDigest {
		return nil, errors.New("expected MessageType DIGEST")
	}

	ethSig, err := crypto.Sign(m.key, params.Message)
	if err != nil {
		return nil, err
	}

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

	rawPoint := make([]byte, 65)
	rawPoint[0] = 0x04
	m.key.PublicKey.X.FillBytes(rawPoint[1:33])
	m.key.PublicKey.Y.FillBytes(rawPoint[33:65])

	spki := subjectPublicKeyInfo{
		Algorithm: algorithmIdentifier{
			Algorithm: asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1},
			Curve:     asn1.ObjectIdentifier{1, 3, 132, 0, 10},
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

func newTestKMSKeyManager(t *testing.T) (*KMSKeyManager, *mockKMSClient) {
	t.Helper()

	mock := newMockKMSClient(t)

	km, err := newKMSKeyManagerFromClient(mock, "test-key-id")
	require.NoError(t, err)

	return km.(*KMSKeyManager), mock
}

func TestNewKMSKeyManagerFromClient(t *testing.T) {
	t.Parallel()

	t.Run("should return error if GetPublicKey fails", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)
		mock.getPubErr = errTest

		km, err := newKMSKeyManagerFromClient(mock, "test-key-id")
		assert.Nil(t, km)
		assert.ErrorContains(t, err, "failed to fetch KMS public key")
	})

	t.Run("should initialize KMSKeyManager with correct address", func(t *testing.T) {
		t.Parallel()

		mock := newMockKMSClient(t)

		km, err := newKMSKeyManagerFromClient(mock, "test-key-id")
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

	km, _ := newTestKMSKeyManager(t)

	assert.Equal(t, validators.ECDSAValidatorType, km.Type())
}

func TestKMSKeyManagerAddress(t *testing.T) {
	t.Parallel()

	km, mock := newTestKMSKeyManager(t)

	assert.Equal(
		t,
		crypto.PubKeyToAddress(&mock.key.PublicKey),
		km.Address(),
	)
}

func TestKMSKeyManagerNewEmptyValidators(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	assert.Equal(t, validators.NewECDSAValidatorSet(), km.NewEmptyValidators())
}

func TestKMSKeyManagerNewEmptyCommittedSeals(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	assert.Equal(t, &SerializedSeal{}, km.NewEmptyCommittedSeals())
}

func TestKMSKeyManagerSignProposerSeal(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	proposerSeal, err := km.SignProposerSeal(msg)
	require.NoError(t, err)

	recoveredAddress, err := ecrecover(proposerSeal, msg)
	require.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

func TestKMSKeyManagerSignProposerSeal_KMSError(t *testing.T) {
	t.Parallel()

	km, mock := newTestKMSKeyManager(t)
	mock.signErr = errTest

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	_, err := km.SignProposerSeal(msg)
	assert.ErrorContains(t, err, "KMS sign request failed")
}

func TestKMSKeyManagerSignIBFTMessageAndEcrecover(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)
	msg := crypto.Keccak256([]byte("message"))

	sig, err := km.SignIBFTMessage(msg)
	require.NoError(t, err)

	recoveredAddress, err := km.Ecrecover(sig, msg)
	require.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

func TestKMSKeyManagerSignCommittedSeal(t *testing.T) {
	t.Parallel()

	km, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	committedSeal, err := km.SignCommittedSeal(msg)
	require.NoError(t, err)

	recoveredAddress, err := ecrecover(committedSeal, msg)
	require.NoError(t, err)

	assert.Equal(t, km.Address(), recoveredAddress)
}

//nolint:dupl
func TestKMSKeyManagerVerifyCommittedSeal(t *testing.T) {
	t.Parallel()

	km1, _ := newTestKMSKeyManager(t)
	km2, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

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

func TestKMSKeyManagerGenerateCommittedSeals(t *testing.T) {
	t.Parallel()

	km1, _ := newTestKMSKeyManager(t)
	km2, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

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

func TestKMSKeyManagerVerifyCommittedSeals(t *testing.T) {
	t.Parallel()

	km1, _ := newTestKMSKeyManager(t)
	km2, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	seal1, err := km1.SignCommittedSeal(msg)
	require.NoError(t, err)

	seal2, err := km2.SignCommittedSeal(msg)
	require.NoError(t, err)

	validSealMap := map[types.Address][]byte{
		km1.Address(): seal1,
		km2.Address(): seal2,
	}

	seals, err := km1.GenerateCommittedSeals(
		validSealMap,
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
func TestKMSKeyManager_MultipleSignersAggregation(t *testing.T) {
	t.Parallel()

	km1, _ := newTestKMSKeyManager(t)
	km2, _ := newTestKMSKeyManager(t)
	km3, _ := newTestKMSKeyManager(t)

	msg := crypto.Keccak256(hex.MustDecodeHex(testHeaderHashHex))

	sealMap := make(map[types.Address][]byte)

	for _, km := range []*KMSKeyManager{km1, km2, km3} {
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

	assert.Equal(t, crypto.PubKeyToAddress(&testKey.PublicKey), recoveredAddr)
}

func TestDerSigToEthSig_InvalidDER(t *testing.T) {
	t.Parallel()

	testKey, _ := newTestECDSAKey(t)

	_, err := derSigToEthSig(
		[]byte("not a DER signature"),
		&testKey.PublicKey,
		make([]byte, 32),
	)
	assert.ErrorContains(t, err, "failed to unmarshal DER signature")
}

func TestDerSigToEthSig_InvalidDigestLength(t *testing.T) {
	t.Parallel()

	testKey, _ := newTestECDSAKey(t)

	_, err := derSigToEthSig(
		[]byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01},
		&testKey.PublicKey,
		[]byte("short"),
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

		mock := newMockKMSClient(t)
		wrongCurveMock := &wrongCurveKMSClient{inner: mock}

		pubKey, err := fetchKMSPublicKey(wrongCurveMock, "test-key-id")
		assert.Nil(t, pubKey)
		assert.ErrorContains(t, err, "unexpected curve OID")
	})
}

// wrongCurveKMSClient returns SPKI with P-256 OID instead of secp256k1
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
