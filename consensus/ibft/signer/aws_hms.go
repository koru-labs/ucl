package signer

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/Ethernal-Tech/kryptology/pkg/signatures/bls/bls_sig"
	"github.com/ThalesGroup/crypto11"
)

// HSMKeyManager implements KeyManager with a split backend:
//   - ECDSA key: lives in AWS CloudHSM, private key never exported
//   - BLS key:   loaded from AWS SSM Parameter Store SecureString at startup
//
// This mirrors BLSKeyManager's signing split exactly:
//
//	SignProposerSeal  → ECDSA (HSM)
//	SignIBFTMessage   → ECDSA (HSM)
//	SignCommittedSeal → BLS   (SSM → memory)
type HSMKeyManager struct {
	// ECDSA — backed by CloudHSM
	hsmSigner crypto11.Signer
	pubKey    *ecdsa.PublicKey
	address   types.Address

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
	// ── 1. ECDSA key from CloudHSM ──────────────────────────────────────
	hsmSigner, err := hsmCtx.FindKeyPair(nil, []byte(keyLabel))
	if err != nil {
		return nil, fmt.Errorf("hsm key manager: FindKeyPair(%q): %w", keyLabel, err)
	}

	if hsmSigner == nil {
		return nil, fmt.Errorf(
			"hsm key manager: no key with label %q in HSM — run `secrets init` first",
			keyLabel,
		)
	}

	pubKey, ok := hsmSigner.Public().(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("hsm key manager: HSM key is not ECDSA")
	}

	address := types.Address(crypto.PubKeyToAddress(*pubKey))

	// ── 2. BLS key from AWS SSM ──────────────────────────────────────────
	// SSM stores the raw BLS secret key bytes as a SecureString parameter.
	// GetSecret decrypts and returns the bytes via the AWS SDK.
	blsKeyBytes, err := ssmMgr.GetSecret(secrets.ValidatorBLSKey)
	if err != nil {
		return nil, fmt.Errorf("hsm key manager: failed to load BLS key from SSM: %w", err)
	}

	blsKey, err := crypto.BytesToBLSSecretKey(blsKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("hsm key manager: failed to deserialize BLS key: %w", err)
	}

	return &HSMKeyManager{
		hsmSigner: hsmSigner,
		pubKey:    pubKey,
		address:   address,
		blsKey:    blsKey,
	}, nil
}

// ── Identity ─────────────────────────────────────────────────────────────────

func (m *HSMKeyManager) Type() validators.ValidatorType {
	// BLS mode — matches BLSKeyManager exactly
	return validators.BLSValidatorType
}

func (m *HSMKeyManager) Address() types.Address {
	return m.address
}

func (m *HSMKeyManager) NewEmptyValidators() validators.Validators {
	return validators.NewBLSValidatorSet()
}

func (m *HSMKeyManager) NewEmptyCommittedSeals() Seals {
	return &AggregatedSeal{}
}

// ── Signing — ECDSA via CloudHSM ─────────────────────────────────────────────

// SignProposerSeal signs the block header hash with the ECDSA key in the HSM.
// Mirrors: BLSKeyManager.SignProposerSeal → crypto.Sign(ecdsaKey, data)
func (m *HSMKeyManager) SignProposerSeal(hash []byte) ([]byte, error) {
	return m.hsmECDSASign(hash)
}

// SignIBFTMessage signs consensus p2p messages with the ECDSA key in the HSM.
// Mirrors: BLSKeyManager.SignIBFTMessage → crypto.Sign(ecdsaKey, msg)
func (m *HSMKeyManager) SignIBFTMessage(msg []byte) ([]byte, error) {
	// polygon-edge hashes the message before signing, same as crypto.Sign
	hash := ethcrypto.Keccak256(msg)
	return m.hsmECDSASign(hash)
}

// ── Signing — BLS in-process from SSM-loaded key ─────────────────────────────

// SignCommittedSeal signs the commit hash with the BLS key loaded from SSM.
// Mirrors: BLSKeyManager.SignCommittedSeal → crypto.SignByBLS(blsKey, data)
func (m *HSMKeyManager) SignCommittedSeal(hash []byte) ([]byte, error) {
	return crypto.SignByBLS(m.blsKey, hash)
}

// ── Verification — all in-process, no HSM or SSM call ────────────────────────

func (m *HSMKeyManager) Ecrecover(sig []byte, msg []byte) (types.Address, error) {
	return ecrecover(sig, msg) // existing helper in signer package
}

func (m *HSMKeyManager) VerifyCommittedSeal(
	set validators.Validators,
	addr types.Address,
	rawSignature []byte,
	hash []byte,
) error {
	// Identical to BLSKeyManager.VerifyCommittedSeal
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

	return crypto.VerifyBLSSignatureFromBytes(
		validator.BLSPublicKey,
		rawSignature,
		hash,
	)
}

func (m *HSMKeyManager) GenerateCommittedSeals(
	sealMap map[types.Address][]byte,
	set validators.Validators,
) (Seals, error) {
	// Identical to BLSKeyManager.GenerateCommittedSeals
	if set.Type() != m.Type() {
		return nil, ErrInvalidValidators
	}

	blsSignatures, bitMap, err := getBLSSignatures(sealMap, set)
	if err != nil {
		return nil, err
	}

	multiSignature, err := bls_sig.NewSigPop().AggregateSignatures(blsSignatures...)
	if err != nil {
		return nil, err
	}

	multiSignatureBytes, err := multiSignature.MarshalBinary()
	if err != nil {
		return nil, err
	}

	return &AggregatedSeal{
		Bitmap:    bitMap,
		Signature: multiSignatureBytes,
	}, nil
}

func (m *HSMKeyManager) VerifyCommittedSeals(
	rawCommittedSeal Seals,
	message []byte,
	vals validators.Validators,
) (int, error) {
	// Identical to BLSKeyManager.VerifyCommittedSeals
	committedSeal, ok := rawCommittedSeal.(*AggregatedSeal)
	if !ok {
		return 0, ErrInvalidCommittedSealType
	}

	if vals.Type() != m.Type() {
		return 0, ErrInvalidValidators
	}

	return verifyBLSCommittedSealsImpl(committedSeal, message, vals)
}

// ── Internal: ECDSA signing via CloudHSM ─────────────────────────────────────

// hsmECDSASign sends a 32-byte hash to the HSM for ECDSA signing and
// converts the DER-encoded response into polygon-edge's expected
// Ethereum [R(32) || S(32) || V(1)] format.
func (m *HSMKeyManager) hsmECDSASign(hash []byte) ([]byte, error) {
	if len(hash) != 32 {
		return nil, fmt.Errorf("hsm key manager: expected 32-byte hash, got %d bytes", len(hash))
	}

	// Private key stays in HSM — only the hash crosses the PKCS#11 boundary
	derSig, err := m.hsmSigner.Sign(nil, hash, nil)
	if err != nil {
		return nil, fmt.Errorf("hsm key manager: HSM ECDSA sign failed: %w", err)
	}

	r, s, err := parseDERToRS(derSig)
	if err != nil {
		return nil, fmt.Errorf("hsm key manager: DER parse failed: %w", err)
	}

	// Enforce low-s per EIP-2 — go-ethereum rejects high-s signatures
	s = enforceLoWS(s)

	return m.buildEthSig(hash, r, s)
}

// parseDERToRS decodes a DER ASN.1 ECDSA signature into (r, s) big.Ints.
// Format: 0x30 [totalLen] 0x02 [rLen] [r...] 0x02 [sLen] [s...]
func parseDERToRS(der []byte) (*big.Int, *big.Int, error) {
	if len(der) < 8 || der[0] != 0x30 {
		return nil, nil, fmt.Errorf("not a DER SEQUENCE")
	}

	body := der[2:] // skip 0x30 and total length

	if body[0] != 0x02 {
		return nil, nil, fmt.Errorf("expected INTEGER tag for r")
	}
	rLen := int(body[1])
	r := new(big.Int).SetBytes(body[2 : 2+rLen])
	body = body[2+rLen:]

	if body[0] != 0x02 {
		return nil, nil, fmt.Errorf("expected INTEGER tag for s")
	}
	sLen := int(body[1])
	s := new(big.Int).SetBytes(body[2 : 2+sLen])

	return r, s, nil
}

// enforceLoWS applies EIP-2 low-s normalization.
func enforceLoWS(s *big.Int) *big.Int {
	N := crypto.S256.N
	halfN := new(big.Int).Rsh(N, 1)

	if s.Cmp(halfN) > 0 {
		return new(big.Int).Sub(N, s)
	}

	return s
}

// buildEthSig encodes (r, s) into [R(32)||S(32)||V(1)] by trying V=0
// then V=1, accepting whichever recovers our known public key address.
func (m *HSMKeyManager) buildEthSig(hash []byte, r, s *big.Int) ([]byte, error) {
	sig := make([]byte, 65)
	r.FillBytes(sig[0:32])
	s.FillBytes(sig[32:64])

	for v := byte(0); v <= 1; v++ {
		sig[64] = v

		recovered, err := crypto.SigToPub(hash, sig)
		if err != nil {
			continue
		}

		if types.Address(crypto.PubKeyToAddress(*recovered)) == m.address {
			return sig, nil
		}
	}

	return nil, fmt.Errorf(
		"hsm key manager: cannot find recovery bit V for address %s",
		m.address,
	)
}
