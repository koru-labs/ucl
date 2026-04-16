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
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	btc_ecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// kmsClient allows injection of a mock in tests
type kmsClient interface {
	Sign(ctx context.Context, params *kms.SignInput, optFns ...func(*kms.Options)) (*kms.SignOutput, error)
	GetPublicKey(
		ctx context.Context,
		params *kms.GetPublicKeyInput,
		optFns ...func(*kms.Options),
	) (*kms.GetPublicKeyOutput, error)
}

// KMSKeyManager is a KeyManager that delegates all signing to AWS KMS.
// The private key never leaves KMS — only the public key is cached locally.
type KMSKeyManager struct {
	client    kmsClient
	keyID     string
	publicKey *ecdsa.PublicKey
	address   types.Address
}

func NewKMSKeyManager(cfg *KMSConfig) (KeyManager, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithRetryMode(aws.RetryModeAdaptive),
	}

	// explicit credentials — only if instance role is not available
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID,
				cfg.SecretAccessKey,
				"",
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// assume role if configured
	if cfg.AssumeRoleARN != "" {
		stsSvc := sts.NewFromConfig(awsCfg)
		awsCfg.Credentials = stscreds.NewAssumeRoleProvider(stsSvc, cfg.AssumeRoleARN)
	}

	kmsOpts := []func(*kms.Options){}

	// custom endpoint for localstack / testing
	if cfg.Endpoint != "" {
		kmsOpts = append(kmsOpts, func(o *kms.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		})
	}

	client := kms.NewFromConfig(awsCfg, kmsOpts...)

	return newKMSKeyManagerFromClient(client, cfg.KeyID)
}

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

func (k *KMSKeyManager) Type() validators.ValidatorType {
	return validators.ECDSAValidatorType
}

func (k *KMSKeyManager) Address() types.Address {
	return k.address
}

func (k *KMSKeyManager) NewEmptyValidators() validators.Validators {
	return validators.NewECDSAValidatorSet()
}

func (k *KMSKeyManager) NewEmptyCommittedSeals() Seals {
	return &SerializedSeal{}
}

func (k *KMSKeyManager) SignProposerSeal(digest []byte) ([]byte, error) {
	return k.signDigest(digest)
}

func (k *KMSKeyManager) SignCommittedSeal(digest []byte) ([]byte, error) {
	return k.signDigest(digest)
}

func (k *KMSKeyManager) SignIBFTMessage(digest []byte) ([]byte, error) {
	return k.signDigest(digest)
}

func (k *KMSKeyManager) Ecrecover(sig, digest []byte) (types.Address, error) {
	return ecrecover(sig, digest)
}

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

var _ KeyManager = (*KMSKeyManager)(nil)

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

func fetchKMSPublicKey(client kmsClient, keyID string) (*ecdsa.PublicKey, error) {
	resp, err := client.GetPublicKey(context.Background(), &kms.GetPublicKeyInput{
		KeyId: aws.String(keyID),
	})
	if err != nil {
		return nil, fmt.Errorf("GetPublicKey failed: %w", err)
	}

	var spki struct {
		Algorithm struct {
			Algorithm asn1.ObjectIdentifier
			Curve     asn1.ObjectIdentifier
		}
		PublicKey asn1.BitString
	}

	if _, err := asn1.Unmarshal(resp.PublicKey, &spki); err != nil {
		return nil, fmt.Errorf("unmarshal SPKI: %w", err)
	}

	secp256k1OID := asn1.ObjectIdentifier{1, 3, 132, 0, 10}
	if !spki.Algorithm.Curve.Equal(secp256k1OID) {
		return nil, fmt.Errorf("unexpected curve OID: %v, expected secp256k1", spki.Algorithm.Curve)
	}

	raw := spki.PublicKey.Bytes
	if len(raw) != 65 || raw[0] != 0x04 {
		return nil, fmt.Errorf("unexpected public key format: len=%d prefix=0x%x", len(raw), raw[0])
	}

	x := new(big.Int).SetBytes(raw[1:33])
	y := new(big.Int).SetBytes(raw[33:65])

	curve := crypto.S256
	if !curve.IsOnCurve(x, y) {
		return nil, fmt.Errorf("point not on secp256k1")
	}

	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

type ecdsaDERSignature struct {
	R, S *big.Int
}

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

func normaliseS(s, n *big.Int) *big.Int {
	halfN := new(big.Int).Rsh(n, 1)
	if s.Cmp(halfN) > 0 {
		return new(big.Int).Sub(n, s)
	}

	return s
}
