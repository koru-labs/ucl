package signer

import (
	"context"
	"crypto/ecdsa"
	"encoding/asn1"
	"fmt"
	"math/big"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
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

// KMSKeyManager is a KeyManager that signs via AWS KMS.
// The private key never leaves KMS — only the public key is cached locally.
type KMSKeyManager struct {
	client    kmsClient
	keyID     string
	publicKey *ecdsa.PublicKey
	address   types.Address
}

// NewKMSKeyManager creates a KMSKeyManager, fetching and caching the
// public key from KMS at startup. No private key material is ever stored locally.
func NewKMSKeyManager(cfg KMSConfig) (KeyManager, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := kms.NewFromConfig(awsCfg)

	return newKMSKeyManagerFromClient(client, cfg.KeyID)
}

// newKMSKeyManagerFromClient constructs a KMSKeyManager from an existing client.
// Used internally and in tests to inject a mock client.
func newKMSKeyManagerFromClient(client kmsClient, keyID string) (KeyManager, error) {
	pubKey, err := fetchKMSPublicKey(client, keyID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch KMS public key: %w", err)
	}

	return &KMSKeyManager{
		client:    client,
		keyID:     keyID,
		publicKey: pubKey,
		address:   crypto.PubKeyToAddress(pubKey),
	}, nil
}

// Type returns ECDSAValidatorType — KMS uses secp256k1 ECDSA
func (k *KMSKeyManager) Type() validators.ValidatorType {
	return validators.ECDSAValidatorType
}

// Address returns the validator address derived from the KMS public key
func (k *KMSKeyManager) Address() types.Address {
	return k.address
}

// NewEmptyValidators returns empty ECDSA validator set
func (k *KMSKeyManager) NewEmptyValidators() validators.Validators {
	return validators.NewECDSAValidatorSet()
}

// NewEmptyCommittedSeals returns empty SerializedSeal
func (k *KMSKeyManager) NewEmptyCommittedSeals() Seals {
	return &SerializedSeal{}
}

// SignProposerSeal signs the given digest via KMS for use as a proposer seal
func (k *KMSKeyManager) SignProposerSeal(digest []byte) ([]byte, error) {
	return k.signDigest(digest)
}

// SignCommittedSeal signs the given digest via KMS for use as a committed seal
func (k *KMSKeyManager) SignCommittedSeal(digest []byte) ([]byte, error) {
	return k.signDigest(digest)
}

// SignIBFTMessage signs an arbitrary IBFT message digest via KMS
func (k *KMSKeyManager) SignIBFTMessage(digest []byte) ([]byte, error) {
	return k.signDigest(digest)
}

// VerifyCommittedSeal verifies a single committed seal — pure crypto, no KMS call needed
func (k *KMSKeyManager) VerifyCommittedSeal(
	vals validators.Validators,
	address types.Address,
	signature []byte,
	digest []byte,
) error {
	if vals.Type() != k.Type() {
		return ErrInvalidValidators
	}

	signer, err := k.Ecrecover(signature, digest)
	if err != nil {
		return ErrInvalidSignature
	}

	if address != signer {
		return ErrSignerMismatch
	}

	if !vals.Includes(address) {
		return ErrNonValidatorCommittedSeal
	}

	return nil
}

// GenerateCommittedSeals builds a SerializedSeal from the seal map.
// Mirrors ECDSAKeyManager.GenerateCommittedSeals exactly.
func (k *KMSKeyManager) GenerateCommittedSeals(
	sealMap map[types.Address][]byte,
	_ validators.Validators,
) (Seals, error) {
	seals := [][]byte{}

	for _, seal := range sealMap {
		if len(seal) != IstanbulExtraSeal {
			return nil, ErrInvalidCommittedSealLength
		}

		seals = append(seals, seal)
	}

	serializedSeal := SerializedSeal(seals)

	return &serializedSeal, nil
}

// VerifyCommittedSeals verifies all committed seals in the set.
// Mirrors ECDSAKeyManager.VerifyCommittedSeals exactly.
func (k *KMSKeyManager) VerifyCommittedSeals(
	rawCommittedSeal Seals,
	digest []byte,
	vals validators.Validators,
) (int, error) {
	committedSeal, ok := rawCommittedSeal.(*SerializedSeal)
	if !ok {
		return 0, ErrInvalidCommittedSealType
	}

	if vals.Type() != k.Type() {
		return 0, ErrInvalidValidators
	}

	return k.verifyCommittedSealsImpl(committedSeal, digest, vals)
}

// Ecrecover recovers the address that produced the given signature over digest.
// Pure local crypto — no KMS call needed.
func (k *KMSKeyManager) Ecrecover(sig, digest []byte) (types.Address, error) {
	return ecrecover(sig, digest)
}

// ── internal ─────────────────────────────────────────────────────────────────

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

func (k *KMSKeyManager) verifyCommittedSealsImpl(
	committedSeal *SerializedSeal,
	msg []byte,
	vals validators.Validators,
) (int, error) {
	numSeals := committedSeal.Num()
	if numSeals == 0 {
		return 0, ErrEmptyCommittedSeals
	}

	visited := make(map[types.Address]bool)

	for _, seal := range *committedSeal {
		addr, err := k.Ecrecover(seal, msg)
		if err != nil {
			return 0, err
		}

		if visited[addr] {
			return 0, ErrRepeatedCommittedSeal
		}

		if !vals.Includes(addr) {
			return 0, ErrNonValidatorCommittedSeal
		}

		visited[addr] = true
	}

	return numSeals, nil
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
		return nil, fmt.Errorf("unexpected public key format: length=%d prefix=%x", len(pubKeyBytes), pubKeyBytes[0])
	}

	x := new(big.Int).SetBytes(pubKeyBytes[1:33])
	y := new(big.Int).SetBytes(pubKeyBytes[33:65])

	curve := crypto.S256 // polygon-edge secp256k1 curve

	if !curve.IsOnCurve(x, y) {
		return nil, fmt.Errorf("public key point is not on secp256k1 curve")
	}

	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
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

// compile-time check
var _ KeyManager = (*KMSKeyManager)(nil)
