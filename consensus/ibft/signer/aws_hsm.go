package signer

import (
	"crypto"
	"crypto/ecdsa"
	"fmt"
	"io"

	polygoncrypto "github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/ThalesGroup/crypto11"
)

// hsmSigner abstracts the crypto.Signer returned by crypto11.
// Allows mock injection in tests without a real HSM.
type hsmSigner interface {
	Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error)
	Public() crypto.PublicKey
}

// HSMKeyManager implements KeyManager using AWS CloudHSM via PKCS#11.
// Mirrors ECDSAKeyManager — all three sign methods use ECDSA.
// The private key never leaves the HSM.
//
//	SignProposerSeal  → ECDSA (HSM)
//	SignIBFTMessage   → ECDSA (HSM)
//	SignCommittedSeal → ECDSA (HSM)
type HSMKeyManager struct {
	signer  hsmSigner
	pubKey  *ecdsa.PublicKey
	address types.Address
}

// NewHSMKeyManagerFromConfig constructs an HSMKeyManager from an HSMConfig,
// initializing the crypto11 PKCS#11 context with production-safe defaults.
func NewHSMKeyManagerFromConfig(cfg *HSMConfig) (KeyManager, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("hsm: invalid config: %w", err)
	}

	hsmCtx, err := crypto11.Configure(&crypto11.Config{
		Path:        cfg.LibPath,     // required
		TokenLabel:  cfg.TokenLabel,  // required
		Pin:         cfg.Pin,         // required
		MaxSessions: cfg.MaxSessions, // important for production
	})
	if err != nil {
		return nil, fmt.Errorf("hsm: failed to initialize PKCS#11 context: %w", err)
	}

	keyManager, err := NewHSMKeyManager(hsmCtx, cfg.KeyLabel)
	if err != nil {
		hsmCtx.Close()

		return nil, err
	}

	return keyManager, nil
}

// NewHSMKeyManager constructs an HSMKeyManager from an already-initialized
// crypto11 context. Use NewHSMKeyManagerFromConfig for normal production use.
func NewHSMKeyManager(hsmCtx *crypto11.Context, keyLabel string) (KeyManager, error) {
	signer, err := hsmCtx.FindKeyPair(nil, []byte(keyLabel))
	if err != nil {
		return nil, fmt.Errorf("hsm: FindKeyPair(%q): %w", keyLabel, err)
	}

	if signer == nil {
		return nil, fmt.Errorf("hsm: no key with label %q in HSM — generate one first", keyLabel)
	}

	return newHSMKeyManagerFromSigner(signer)
}

// newHSMKeyManagerFromSigner constructs an HSMKeyManager from an existing signer.
// Used internally and in tests to inject a mock signer.
func newHSMKeyManagerFromSigner(signer hsmSigner) (KeyManager, error) {
	pubKey, err := extractSecp256k1PubKey(signer)
	if err != nil {
		return nil, fmt.Errorf("hsm: %w", err)
	}

	return &HSMKeyManager{
		signer:  signer,
		pubKey:  pubKey,
		address: polygoncrypto.PubKeyToAddress(pubKey),
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

func (m *HSMKeyManager) Type() validators.ValidatorType {
	return validators.ECDSAValidatorType
}

func (m *HSMKeyManager) Address() types.Address {
	return m.address
}

func (m *HSMKeyManager) NewEmptyValidators() validators.Validators {
	return validators.NewECDSAValidatorSet()
}

func (m *HSMKeyManager) NewEmptyCommittedSeals() Seals {
	return &SerializedSeal{}
}

func (m *HSMKeyManager) SignProposerSeal(digest []byte) ([]byte, error) {
	return m.hsmECDSASign(digest)
}

func (m *HSMKeyManager) SignCommittedSeal(digest []byte) ([]byte, error) {
	return m.hsmECDSASign(digest)
}

func (m *HSMKeyManager) SignIBFTMessage(msg []byte) ([]byte, error) {
	return m.hsmECDSASign(msg)
}

func (m *HSMKeyManager) Ecrecover(sig, digest []byte) (types.Address, error) {
	return ecrecover(sig, digest)
}

func (m *HSMKeyManager) VerifyCommittedSeal(
	vals validators.Validators,
	address types.Address,
	signature []byte,
	digest []byte,
) error {
	if vals.Type() != m.Type() {
		return ErrInvalidValidators
	}

	signer, err := m.Ecrecover(signature, digest)
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

func (m *HSMKeyManager) GenerateCommittedSeals(
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

func (m *HSMKeyManager) VerifyCommittedSeals(
	rawCommittedSeal Seals,
	digest []byte,
	vals validators.Validators,
) (int, error) {
	committedSeal, ok := rawCommittedSeal.(*SerializedSeal)
	if !ok {
		return 0, ErrInvalidCommittedSealType
	}

	if vals.Type() != m.Type() {
		return 0, ErrInvalidValidators
	}

	return m.verifyCommittedSealsImpl(committedSeal, digest, vals)
}

var _ KeyManager = (*HSMKeyManager)(nil)

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

func (m *HSMKeyManager) verifyCommittedSealsImpl(
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
		addr, err := m.Ecrecover(seal, msg)
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
