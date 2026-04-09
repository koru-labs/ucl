package signer

import (
	"context"
	"crypto/ecdsa"
	"encoding/asn1"
	"fmt"
	"math/big"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/Ethernal-Tech/kryptology/pkg/signatures/bls/bls_sig"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	btc_ecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// KMSConfig holds the configuration needed to connect to AWS KMS
type KMSConfig struct {
	KeyID  string
	Region string
}

// kmsClient is an interface over the AWS KMS client,
// allowing injection of a mock in tests
type kmsClient interface {
	Sign(ctx context.Context, params *kms.SignInput, optFns ...func(*kms.Options)) (*kms.SignOutput, error)
	GetPublicKey(ctx context.Context, params *kms.GetPublicKeyInput, optFns ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

// KMSKeyManager is a KeyManager that signs via AWS KMS for ECDSA
// and AWS SSM for BLS committed seals.
//
// Signing split — mirrors BLSKeyManager exactly:
//
//	SignProposerSeal  → ECDSA via AWS KMS  (private key never leaves KMS)
//	SignIBFTMessage   → ECDSA via AWS KMS  (private key never leaves KMS)
//	SignCommittedSeal → BLS   via AWS SSM  (key loaded into memory at startup,
//	                                        encrypted at rest as SSM SecureString)
type KMSKeyManager struct {
	// ECDSA — AWS KMS
	client    kmsClient
	keyID     string
	publicKey *ecdsa.PublicKey
	address   types.Address

	// BLS — loaded from AWS SSM at startup
	blsKey *bls_sig.SecretKey
}

// NewKMSKeyManager creates a KMSKeyManager, fetching and caching the
// public key from KMS and loading the BLS key from SSM at startup.
// No private key material is ever stored locally.
func NewKMSKeyManager(cfg KMSConfig, ssmMgr secrets.SecretsManager) (KeyManager, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := kms.NewFromConfig(awsCfg)

	return newKMSKeyManagerFromClient(client, cfg.KeyID, ssmMgr)
}

// newKMSKeyManagerFromClient constructs a KMSKeyManager from an existing client.
// Used internally and in tests to inject a mock client.
func newKMSKeyManagerFromClient(
	client kmsClient,
	keyID string,
	ssmMgr secrets.SecretsManager,
) (KeyManager, error) {
	// ── 1. ECDSA public key from KMS ─────────────────────────────────────
	pubKey, err := fetchKMSPublicKey(client, keyID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch KMS public key: %w", err)
	}

	// ── 2. BLS secret key from SSM ───────────────────────────────────────
	blsKey, err := blsKeyFromSSM(ssmMgr)
	if err != nil {
		return nil, fmt.Errorf("failed to load BLS key from SSM: %w", err)
	}

	return &KMSKeyManager{
		client:    client,
		keyID:     keyID,
		publicKey: pubKey,
		address:   crypto.PubKeyToAddress(pubKey),
		blsKey:    blsKey,
	}, nil
}

// ── Identity ──────────────────────────────────────────────────────────────────

// Type returns BLSValidatorType because committed seals use BLS aggregation.
// Mirrors BLSKeyManager.Type() — must match so IBFT uses AggregatedSeal.
func (k *KMSKeyManager) Type() validators.ValidatorType {
	return validators.BLSValidatorType
}

// Address returns the validator address derived from the KMS public key
func (k *KMSKeyManager) Address() types.Address {
	return k.address
}

// NewEmptyValidators returns empty BLS validator set
func (k *KMSKeyManager) NewEmptyValidators() validators.Validators {
	return validators.NewBLSValidatorSet()
}

// NewEmptyCommittedSeals returns empty AggregatedSeal for BLS
func (k *KMSKeyManager) NewEmptyCommittedSeals() Seals {
	return &AggregatedSeal{}
}

// ── Signing ───────────────────────────────────────────────────────────────────

// SignProposerSeal signs the given digest via KMS.
// Mirrors BLSKeyManager.SignProposerSeal → crypto.Sign(ecdsaKey, data)
func (k *KMSKeyManager) SignProposerSeal(digest []byte) ([]byte, error) {
	return k.signDigest(digest)
}

// SignIBFTMessage signs an arbitrary IBFT message digest via KMS.
// Mirrors BLSKeyManager.SignIBFTMessage → crypto.Sign(ecdsaKey, msg)
func (k *KMSKeyManager) SignIBFTMessage(digest []byte) ([]byte, error) {
	return k.signDigest(digest)
}

// SignCommittedSeal signs the committed seal hash via BLS using the SSM-loaded key.
// Mirrors BLSKeyManager.SignCommittedSeal → crypto.SignByBLS(blsKey, data)
func (k *KMSKeyManager) SignCommittedSeal(hash []byte) ([]byte, error) {
	return crypto.SignByBLS(k.blsKey, hash)
}

// ── Verification — all in-process, no KMS or SSM call ────────────────────────

// Ecrecover recovers the address that produced the given signature over digest.
// Pure local crypto — no KMS call needed.
func (k *KMSKeyManager) Ecrecover(sig, digest []byte) (types.Address, error) {
	return ecrecover(sig, digest)
}

// VerifyCommittedSeal verifies a single BLS committed seal.
// Mirrors BLSKeyManager.VerifyCommittedSeal exactly.
func (k *KMSKeyManager) VerifyCommittedSeal(
	set validators.Validators,
	addr types.Address,
	rawSignature []byte,
	hash []byte,
) error {
	if set.Type() != k.Type() {
		return ErrInvalidValidators
	}

	validatorIndex := set.Index(addr)
	if validatorIndex == -1 {
		return ErrValidatorNotFound
	}

	validator, ok := set.At(uint64(validatorIndex)).(*validators.BLSValidator)
	if !ok {
		return ErrInvalidValidators
	}

	return crypto.VerifyBLSSignatureFromBytes(
		validator.BLSPublicKey,
		rawSignature,
		hash,
	)
}

// GenerateCommittedSeals aggregates BLS signatures into an AggregatedSeal.
// Mirrors BLSKeyManager.GenerateCommittedSeals exactly.
func (k *KMSKeyManager) GenerateCommittedSeals(
	sealMap map[types.Address][]byte,
	set validators.Validators,
) (Seals, error) {
	if set.Type() != k.Type() {
		return nil, ErrInvalidValidators
	}

	blsSignatures, bitMap, err := getBLSSignatures(sealMap, set)
	if err != nil {
		return nil, err
	}

	multiSig, err := bls_sig.NewSigPop().AggregateSignatures(blsSignatures...)
	if err != nil {
		return nil, err
	}

	multiSigBytes, err := multiSig.MarshalBinary()
	if err != nil {
		return nil, err
	}

	return &AggregatedSeal{
		Bitmap:    bitMap,
		Signature: multiSigBytes,
	}, nil
}

// VerifyCommittedSeals verifies the aggregated BLS seal set.
// Mirrors BLSKeyManager.VerifyCommittedSeals exactly.
func (k *KMSKeyManager) VerifyCommittedSeals(
	rawCommittedSeal Seals,
	message []byte,
	vals validators.Validators,
) (int, error) {
	committedSeal, ok := rawCommittedSeal.(*AggregatedSeal)
	if !ok {
		return 0, ErrInvalidCommittedSealType
	}

	if vals.Type() != k.Type() {
		return 0, ErrInvalidValidators
	}

	return verifyBLSCommittedSealsImpl(committedSeal, message, vals)
}

// ── compile-time check ────────────────────────────────────────────────────────

var _ KeyManager = (*KMSKeyManager)(nil)

// ── internal ──────────────────────────────────────────────────────────────────

// signDigest calls KMS Sign and converts the DER response to
// the 65-byte Ethereum [R || S || V] format the rest of the signer expects.
func (k *KMSKeyManager) signDigest(digest []byte) ([]byte, error) {
	resp, err := k.client.Sign(context.Background(), &kms.SignInput{
		KeyId:            aws.String(k.keyID),
		Message:          digest,
		MessageType:      kmstypes.MessageTypeDigest,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return nil, fmt.Errorf("KMS sign request failed: %w", err)
	}

	return derSigToEthSig(resp.Signature, k.publicKey, digest)
}

// fetchKMSPublicKey retrieves and parses the public key from KMS.
// Called once at startup — result is cached in KMSKeyManager.
func fetchKMSPublicKey(client kmsClient, keyID string) (*ecdsa.PublicKey, error) {
	resp, err := client.GetPublicKey(context.Background(), &kms.GetPublicKeyInput{
		KeyId: aws.String(keyID),
	})
	if err != nil {
		return nil, fmt.Errorf("GetPublicKey failed: %w", err)
	}

	pubKeyBytes := resp.PublicKey

	// expect 65-byte uncompressed point: 04 || X (32 bytes) || Y (32 bytes)
	if len(pubKeyBytes) != 65 || pubKeyBytes[0] != 0x04 {
		return nil, fmt.Errorf(
			"unexpected public key format: length=%d prefix=%x",
			len(pubKeyBytes), pubKeyBytes[0],
		)
	}

	x := new(big.Int).SetBytes(pubKeyBytes[1:33])
	y := new(big.Int).SetBytes(pubKeyBytes[33:65])

	curve := crypto.S256

	if !curve.IsOnCurve(x, y) {
		return nil, fmt.Errorf("public key point is not on secp256k1 curve")
	}

	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

// blsKeyFromSSM loads the BLS secret key bytes from SSM and deserialises them.
// SSM SecureString parameters are decrypted transparently by the AWS SDK.
// Uses the same deserialisation path as getOrCreateBLSKey in bls_key_manager.go.
func blsKeyFromSSM(ssmMgr secrets.SecretsManager) (*bls_sig.SecretKey, error) {
	raw, err := ssmMgr.GetSecret(secrets.ValidatorBLSKey)
	if err != nil {
		return nil, fmt.Errorf("GetSecret(%q): %w", secrets.ValidatorBLSKey, err)
	}

	if len(raw) == 0 {
		return nil, fmt.Errorf("empty BLS key in SSM — run `secrets init` first")
	}

	key := &bls_sig.SecretKey{}
	if err := key.UnmarshalBinary(raw); err != nil {
		return nil, fmt.Errorf("UnmarshalBinary BLS key: %w", err)
	}

	return key, nil
}

// ecdsaDERSignature decodes the ASN.1 DER signature KMS returns
type ecdsaDERSignature struct {
	R, S *big.Int
}

// derSigToEthSig converts an ASN.1 DER-encoded {R,S} from KMS
// into the 65-byte [R || S || V] Ethereum format.
// V is found by trying 0 and 1 and checking which recovers the known public key.
func derSigToEthSig(derSig []byte, pubKey *ecdsa.PublicKey, digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("digest must be 32 bytes, got %d", len(digest))
	}

	var parsed ecdsaDERSignature
	if _, err := asn1.Unmarshal(derSig, &parsed); err != nil {
		return nil, fmt.Errorf("failed to unmarshal DER signature: %w", err)
	}

	parsed.S = normaliseS(parsed.S, pubKey.Curve.Params().N)

	ethSig := make([]byte, 65)
	parsed.R.FillBytes(ethSig[0:32])
	parsed.S.FillBytes(ethSig[32:64])

	// btc_ecdsa.RecoverCompact format: [v+27 || R || S]
	btcSig := make([]byte, 65)
	copy(btcSig[1:], ethSig[:64])

	for v := byte(0); v <= 1; v++ {
		btcSig[0] = v + 27

		recovered, _, err := btc_ecdsa.RecoverCompact(btcSig, digest)
		if err != nil {
			continue
		}

		recoveredECDSA := recovered.ToECDSA()
		if recoveredECDSA.X.Cmp(pubKey.X) == 0 && recoveredECDSA.Y.Cmp(pubKey.Y) == 0 {
			ethSig[64] = v
			return ethSig, nil
		}
	}

	return nil, fmt.Errorf("could not determine signature recovery bit")
}

// normaliseS enforces the lower-S convention required by Ethereum.
func normaliseS(s, n *big.Int) *big.Int {
	halfN := new(big.Int).Rsh(n, 1)
	if s.Cmp(halfN) > 0 {
		return new(big.Int).Sub(n, s)
	}

	return s
}
