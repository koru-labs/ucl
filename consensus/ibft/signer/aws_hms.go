package signer

import (
	"crypto"
	"crypto/ecdsa"
	"fmt"
	"io"

	polygoncrypto "github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/Ethernal-Tech/kryptology/pkg/signatures/bls/bls_sig"
	"github.com/ThalesGroup/crypto11"
)

// hsmSigner abstracts the crypto.Signer returned by crypto11.
// Allows mock injection in tests without a real HSM.
type hsmSigner interface {
	Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error)
	Public() crypto.PublicKey
}

// HSMKeyManager implements KeyManager with a split backend:
//   - ECDSA key: lives in AWS CloudHSM via PKCS#11, private key never exported
//   - BLS key:   loaded from AWS SSM Parameter Store SecureString at startup
//
// This mirrors BLSKeyManager's signing split exactly:
//
//	SignProposerSeal  → ECDSA (HSM)
//	SignIBFTMessage   → ECDSA (HSM)
//	SignCommittedSeal → BLS   (SSM → memory)
type HSMKeyManager struct {
	// ECDSA — backed by CloudHSM via PKCS#11
	signer  hsmSigner
	pubKey  *ecdsa.PublicKey
	address types.Address

	// BLS — loaded from SSM into memory at startup
	blsKey *bls_sig.SecretKey
}

// NewHSMKeyManager constructs an HSMKeyManager.
//
// hsmCtx:   already-initialized crypto11 context (PKCS#11 to CloudHSM)
// keyLabel: CKA_LABEL of the ECDSA key pair in the HSM
// ssmMgr:   an AWSSSM SecretsManager instance used to load the BLS key bytes
//
// The BLS key is read once from SSM at startup and held in memory.
// The ECDSA private key never leaves the HSM.
func NewHSMKeyManager(
	hsmCtx *crypto11.Context,
	keyLabel string,
	ssmMgr secrets.SecretsManager,
) (KeyManager, error) {
	signer, err := hsmCtx.FindKeyPair(nil, []byte(keyLabel))
	if err != nil {
		return nil, fmt.Errorf("hsm: FindKeyPair(%q): %w", keyLabel, err)
	}

	if signer == nil {
		return nil, fmt.Errorf(
			"hsm: no key with label %q in HSM — generate one first",
			keyLabel,
		)
	}

	return newHSMKeyManagerFromSigner(signer, ssmMgr)
}

// newHSMKeyManagerFromSigner constructs an HSMKeyManager from an existing signer.
// Used internally and in tests to inject a mock signer.
func newHSMKeyManagerFromSigner(
	signer hsmSigner,
	ssmMgr secrets.SecretsManager,
) (KeyManager, error) {
	pubKey, err := extractSecp256k1PubKey(signer)
	if err != nil {
		return nil, fmt.Errorf("hsm: %w", err)
	}

	blsKey, err := blsKeyFromSSM(ssmMgr)
	if err != nil {
		return nil, fmt.Errorf("hsm: failed to load BLS key from SSM: %w", err)
	}

	return &HSMKeyManager{
		signer:  signer,
		pubKey:  pubKey,
		address: polygoncrypto.PubKeyToAddress(pubKey),
		blsKey:  blsKey,
	}, nil
}

// extractSecp256k1PubKey validates that the signer holds a secp256k1 ECDSA key
// and returns it with the correct curve assignment.
func extractSecp256k1PubKey(signer hsmSigner) (*ecdsa.PublicKey, error) {
	pub, ok := signer.Public().(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("HSM key is not ECDSA")
	}

	curve := polygoncrypto.S256
	if !curve.IsOnCurve(pub.X, pub.Y) {
		return nil, fmt.Errorf("HSM public key is not on secp256k1 curve")
	}

	return &ecdsa.PublicKey{
		Curve: curve,
		X:     pub.X,
		Y:     pub.Y,
	}, nil
}

// Type returns BLSValidatorType because committed seals use BLS aggregation.
// Mirrors BLSKeyManager.Type() — must match so IBFT uses AggregatedSeal.
func (m *HSMKeyManager) Type() validators.ValidatorType {
	return validators.BLSValidatorType
}

// Address returns the validator address derived from the HSM public key.
func (m *HSMKeyManager) Address() types.Address {
	return m.address
}

// NewEmptyValidators returns empty BLS validator set.
func (m *HSMKeyManager) NewEmptyValidators() validators.Validators {
	return validators.NewBLSValidatorSet()
}

// NewEmptyCommittedSeals returns empty AggregatedSeal for BLS.
func (m *HSMKeyManager) NewEmptyCommittedSeals() Seals {
	return &AggregatedSeal{}
}

// SignProposerSeal signs the block header hash with the ECDSA key in the HSM.
// Mirrors: BLSKeyManager.SignProposerSeal → crypto.Sign(ecdsaKey, data)
func (m *HSMKeyManager) SignProposerSeal(digest []byte) ([]byte, error) {
	return m.hsmECDSASign(digest)
}

// SignIBFTMessage signs consensus messages with the ECDSA key in the HSM.
// Mirrors: BLSKeyManager.SignIBFTMessage → crypto.Sign(ecdsaKey, msg)
//
// NOTE: The caller already passes a Keccak-256 hash — do NOT re-hash.
func (m *HSMKeyManager) SignIBFTMessage(msg []byte) ([]byte, error) {
	return m.hsmECDSASign(msg)
}

// SignCommittedSeal signs the commit hash with the BLS key loaded from SSM.
// Mirrors: BLSKeyManager.SignCommittedSeal → crypto.SignByBLS(blsKey, data)
func (m *HSMKeyManager) SignCommittedSeal(hash []byte) ([]byte, error) {
	return polygoncrypto.SignByBLS(m.blsKey, hash)
}

// Ecrecover recovers the address that produced the given signature over digest.
// Pure local crypto — no HSM call needed.
func (m *HSMKeyManager) Ecrecover(sig, digest []byte) (types.Address, error) {
	return ecrecover(sig, digest)
}

// VerifyCommittedSeal verifies a single BLS committed seal.
// Mirrors BLSKeyManager.VerifyCommittedSeal exactly.
func (m *HSMKeyManager) VerifyCommittedSeal(
	set validators.Validators,
	addr types.Address,
	rawSignature []byte,
	hash []byte,
) error {
	if set.Type() != m.Type() {
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

	return polygoncrypto.VerifyBLSSignatureFromBytes(
		validator.BLSPublicKey,
		rawSignature,
		hash,
	)
}

// GenerateCommittedSeals aggregates BLS signatures into an AggregatedSeal.
// Mirrors BLSKeyManager.GenerateCommittedSeals exactly.
func (m *HSMKeyManager) GenerateCommittedSeals(
	sealMap map[types.Address][]byte,
	set validators.Validators,
) (Seals, error) {
	if set.Type() != m.Type() {
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
func (m *HSMKeyManager) VerifyCommittedSeals(
	rawCommittedSeal Seals,
	message []byte,
	vals validators.Validators,
) (int, error) {
	committedSeal, ok := rawCommittedSeal.(*AggregatedSeal)
	if !ok {
		return 0, ErrInvalidCommittedSealType
	}

	if vals.Type() != m.Type() {
		return 0, ErrInvalidValidators
	}

	return verifyBLSCommittedSealsImpl(committedSeal, message, vals)
}

var _ KeyManager = (*HSMKeyManager)(nil)

// hsmECDSASign sends a 32-byte digest to the HSM for raw ECDSA signing
// (CKM_ECDSA no re-hashing) and converts the DER response into the
// 65-byte [R || S || V] Ethereum format.
//
// Reuses derSigToEthSig from kms_key_manager.go which handles:
//   - ASN.1 DER unmarshalling
//   - Low-S normalization (EIP-2)
//   - Recovery bit (V) detection
func (m *HSMKeyManager) hsmECDSASign(digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("hsm: expected 32-byte digest, got %d", len(digest))
	}

	derSig, err := m.signer.Sign(nil, digest, nil)
	if err != nil {
		return nil, fmt.Errorf("hsm: ECDSA sign failed: %w", err)
	}

	ethSig, err := derSigToEthSig(derSig, m.pubKey, digest)
	if err != nil {
		return nil, fmt.Errorf("hsm: %w", err)
	}

	return ethSig, nil
}
