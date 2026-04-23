package signer

import (
	"crypto"
	"crypto/ecdsa"
	"encoding/asn1"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"

	polygoncrypto "github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	btc_ecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/miekg/pkcs11"
)

const maxSignRetries = 10

// HSMKeyManager implements KeyManager using PKCS#11 directly via miekg/pkcs11.
// crypto11 is intentionally not used here because it maps key OIDs to
// crypto/elliptic, which has no secp256k1 entry.
//
// All three sign methods use ECDSA via CKM_ECDSA.
// The private key never leaves the HSM
type HSMKeyManager struct {
	ctx     *pkcs11.Ctx
	session pkcs11.SessionHandle
	privKey pkcs11.ObjectHandle
	pubKey  *ecdsa.PublicKey
	address types.Address
	mu      sync.Mutex
	signer  hsmSigner // non-nil only in tests
}

// hsmSigner is a test seam — allows injecting a local key without a real HSM.
type hsmSigner interface {
	Public() crypto.PublicKey
	Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error)
}

func newHSMKeyManagerFromSigner(signer hsmSigner) (KeyManager, error) {
	pubKey, err := extractSecp256k1PubKey(signer)
	if err != nil {
		return nil, fmt.Errorf("hsm: %w", err)
	}

	return &HSMKeyManager{
		pubKey:  pubKey,
		address: polygoncrypto.PubKeyToAddress(pubKey),
		signer:  signer,
	}, nil
}

func extractSecp256k1PubKey(signer hsmSigner) (*ecdsa.PublicKey, error) {
	pub, ok := signer.Public().(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("HSM key is not ECDSA")
	}

	curve := polygoncrypto.S256
	if !curve.IsOnCurve(pub.X, pub.Y) {
		return nil, fmt.Errorf("HSM public key is not on secp256k1 curve")
	}

	return &ecdsa.PublicKey{Curve: curve, X: pub.X, Y: pub.Y}, nil
}

func NewHSMKeyManagerFromConfig(cfg *HSMConfig) (KeyManager, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("hsm: invalid config: %w", err)
	}

	ctx := pkcs11.New(cfg.LibPath)
	if ctx == nil {
		return nil, fmt.Errorf("hsm: failed to load PKCS#11 library: %s", cfg.LibPath)
	}

	if err := ctx.Initialize(); err != nil {
		return nil, fmt.Errorf("hsm: initialize failed: %w", err)
	}

	slots, err := ctx.GetSlotList(true)
	if err != nil {
		return nil, fmt.Errorf("hsm: GetSlotList failed: %w", err)
	}

	slot, err := findSlotByTokenLabel(ctx, slots, cfg.TokenLabel)
	if err != nil {
		return nil, err
	}

	session, err := ctx.OpenSession(slot, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
	if err != nil {
		return nil, fmt.Errorf("hsm: OpenSession failed: %w", err)
	}

	if err := ctx.Login(session, pkcs11.CKU_USER, cfg.Pin); err != nil {
		return nil, fmt.Errorf("hsm: Login failed: %w", err)
	}

	privKey, pubKey, err := findKeyPairByLabel(ctx, session, cfg.PubKeyLabel, cfg.PrivKeyLabel)
	if err != nil {
		return nil, err
	}

	ecPub, err := parseECPoint(ctx, session, pubKey)
	if err != nil {
		return nil, fmt.Errorf("hsm: failed to parse EC point: %w", err)
	}

	return &HSMKeyManager{
		ctx:     ctx,
		session: session,
		privKey: privKey,
		pubKey:  ecPub,
		address: polygoncrypto.PubKeyToAddress(ecPub),
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

func (m *HSMKeyManager) SignProposerSeal(d []byte) ([]byte, error) {
	return m.hsmECDSASign(d)
}

func (m *HSMKeyManager) SignCommittedSeal(d []byte) ([]byte, error) {
	return m.hsmECDSASign(d)
}

func (m *HSMKeyManager) SignIBFTMessage(d []byte) ([]byte, error) {
	return m.hsmECDSASign(d)
}

func (m *HSMKeyManager) Ecrecover(sig, digest []byte) (types.Address, error) {
	return ecrecover(sig, digest)
}

func (k *HSMKeyManager) VerifyCommittedSeal(
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

func (k *HSMKeyManager) GenerateCommittedSeals(
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

func (k *HSMKeyManager) VerifyCommittedSeals(
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

func (k *HSMKeyManager) verifyCommittedSealsImpl(
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

func findSlotByTokenLabel(ctx *pkcs11.Ctx, slots []uint, label string) (uint, error) {
	for _, slot := range slots {
		info, err := ctx.GetTokenInfo(slot)
		if err != nil {
			continue
		}

		if trimPadding(info.Label) == label {
			return slot, nil
		}
	}

	return 0, fmt.Errorf("hsm: no slot found with token label %q", label)
}

func findKeyPairByLabel(ctx *pkcs11.Ctx, session pkcs11.SessionHandle, pubLabel, privLabel string) (pkcs11.ObjectHandle, pkcs11.ObjectHandle, error) {
	// find private key
	if err := ctx.FindObjectsInit(session, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_PRIVATE_KEY),
		pkcs11.NewAttribute(pkcs11.CKA_LABEL, privLabel),
	}); err != nil {
		return 0, 0, fmt.Errorf("hsm: FindObjectsInit (privkey) failed: %w", err)
	}

	privObjs, _, err := ctx.FindObjects(session, 1)
	ctx.FindObjectsFinal(session) //nolint:errcheck

	if err != nil || len(privObjs) == 0 {
		return 0, 0, fmt.Errorf("hsm: private key with label %q not found", privLabel)
	}

	// find public key
	if err := ctx.FindObjectsInit(session, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_CLASS, pkcs11.CKO_PUBLIC_KEY),
		pkcs11.NewAttribute(pkcs11.CKA_LABEL, pubLabel),
	}); err != nil {
		return 0, 0, fmt.Errorf("hsm: FindObjectsInit (pubkey) failed: %w", err)
	}

	pubObjs, _, err := ctx.FindObjects(session, 1)
	ctx.FindObjectsFinal(session) //nolint:errcheck

	if err != nil || len(pubObjs) == 0 {
		return 0, 0, fmt.Errorf("hsm: public key with label %q not found", pubLabel)
	}

	return privObjs[0], pubObjs[0], nil
}

// parseECPoint reads CKA_EC_POINT from the public key object.
// The attribute is DER-encoded: OCTET STRING wrapping the uncompressed point (04 || X || Y).
func parseECPoint(ctx *pkcs11.Ctx, session pkcs11.SessionHandle, pubObj pkcs11.ObjectHandle) (*ecdsa.PublicKey, error) {
	attrs, err := ctx.GetAttributeValue(session, pubObj, []*pkcs11.Attribute{
		pkcs11.NewAttribute(pkcs11.CKA_EC_POINT, nil),
	})
	if err != nil {
		return nil, fmt.Errorf("GetAttributeValue CKA_EC_POINT failed: %w", err)
	}

	// unwrap DER OCTET STRING
	var point []byte
	if _, err := asn1.Unmarshal(attrs[0].Value, &point); err != nil {
		// some HSMs return the raw point directly without DER wrapping
		point = attrs[0].Value
	}

	if len(point) != 65 || point[0] != 0x04 {
		return nil, fmt.Errorf("expected 65-byte uncompressed point, got %d bytes", len(point))
	}

	x := new(big.Int).SetBytes(point[1:33])
	y := new(big.Int).SetBytes(point[33:65])

	curve := polygoncrypto.S256
	if !curve.IsOnCurve(x, y) {
		return nil, fmt.Errorf("point is not on secp256k1")
	}

	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

func (m *HSMKeyManager) hsmECDSASign(digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("hsm: expected 32-byte digest, got %d", len(digest))
	}

	// test path
	if m.signer != nil {
		raw, err := m.signer.Sign(nil, digest, nil)
		if err != nil {
			return nil, fmt.Errorf("hsm: ECDSA sign failed: %w", err)
		}

		ethSig, err := rawRSToEthSig(raw, m.pubKey, digest)
		if err != nil {
			return nil, fmt.Errorf("hsm: %w", err)
		}

		return ethSig, nil
	}

	// production pkcs11 path with retry
	for attempt := 0; attempt < maxSignRetries; attempt++ {
		if err := m.ctx.SignInit(m.session, []*pkcs11.Mechanism{
			pkcs11.NewMechanism(pkcs11.CKM_ECDSA, nil),
		}, m.privKey); err != nil {
			return nil, fmt.Errorf("hsm: SignInit failed: %w", err)
		}

		raw, err := m.ctx.Sign(m.session, digest)
		if err != nil {
			return nil, fmt.Errorf("hsm: Sign failed: %w", err)
		}

		if len(raw) != 64 {
			return nil, fmt.Errorf("hsm: expected 64-byte r||s, got %d", len(raw))
		}

		ethSig, err := rawRSToEthSig(raw, m.pubKey, digest)
		if err == nil {
			return ethSig, nil
		}
	}

	return nil, fmt.Errorf("hsm: failed to produce valid signature after %d attempts", maxSignRetries)
}

func rawRSToEthSig(raw []byte, pubKey *ecdsa.PublicKey, digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("digest must be 32 bytes, got %d", len(digest))
	}

	if len(raw) != 64 {
		return nil, fmt.Errorf("expected 64-byte r||s, got %d", len(raw))
	}

	r := new(big.Int).SetBytes(raw[:32])
	s := new(big.Int).SetBytes(raw[32:64])
	s = normaliseS(s, pubKey.Curve.Params().N)

	ethSig := make([]byte, 65)
	r.FillBytes(ethSig[0:32])
	s.FillBytes(ethSig[32:64])

	btcSig := make([]byte, 65)
	copy(btcSig[1:], ethSig[:64])

	for v := byte(0); v <= 1; v++ {
		btcSig[0] = v + 27

		recovered, _, err := btc_ecdsa.RecoverCompact(btcSig, digest)
		if err != nil {
			fmt.Printf("DEBUG: v=%d RecoverCompact err: %v\n", v, err)
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

// trimPadding removes trailing spaces from PKCS#11 fixed-width token label strings.
func trimPadding(s string) string {
	return strings.TrimRight(s, " ")
}
