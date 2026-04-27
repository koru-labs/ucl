package addresshsm

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/miekg/pkcs11"
)

const hsmConfigPathFlag = "hsm-config-path"

var params = &addressParams{}

type addressParams struct {
	hsmConfigPath string
}

type addressResult struct {
	Address string `json:"address"`
}

func (r *addressResult) GetOutput() string {
	out, _ := json.Marshal(r)

	return string(out)
}

func loadHSMConfig(path string) (*signer.HSMConfig, error) {
	cfg, err := signer.ReadConfig(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read signer config at %s: %w", path, err)
	}

	if cfg == nil {
		return nil, fmt.Errorf("signer config not found at %s", path)
	}

	if cfg.Backend != signer.SignerBackendHSM {
		return nil, fmt.Errorf("signer backend is %q, expected %q", cfg.Backend, signer.SignerBackendHSM)
	}

	if cfg.HSM == nil {
		return nil, errors.New("signer config has backend=hsm but hsm section is empty")
	}

	return cfg.HSM, nil
}

func deriveValidatorAddress(cfg *signer.HSMConfig) (types.Address, error) {
	ctx := pkcs11.New(cfg.LibPath)
	if ctx == nil {
		return types.Address{}, fmt.Errorf("hsm: failed to load PKCS#11 library: %s", cfg.LibPath)
	}

	if err := ctx.Initialize(); err != nil {
		return types.Address{}, fmt.Errorf("hsm: initialize failed: %w", err)
	}
	defer ctx.Finalize() //nolint:errcheck
	defer ctx.Destroy()

	slots, err := ctx.GetSlotList(true)
	if err != nil {
		return types.Address{}, fmt.Errorf("hsm: GetSlotList failed: %w", err)
	}

	slot, err := signer.FindSlotByTokenLabel(ctx, slots, cfg.TokenLabel)
	if err != nil {
		return types.Address{}, err
	}

	session, err := ctx.OpenSession(slot, pkcs11.CKF_SERIAL_SESSION)
	if err != nil {
		return types.Address{}, fmt.Errorf("hsm: OpenSession failed: %w", err)
	}
	defer ctx.CloseSession(session) //nolint:errcheck

	pubObj, err := signer.FindPublicKeyByLabel(ctx, session, cfg.PubKeyLabel)
	if err != nil {
		return types.Address{}, err
	}

	pubKey, err := signer.ParseECPoint(ctx, session, pubObj)
	if err != nil {
		return types.Address{}, fmt.Errorf("hsm: failed to parse EC point: %w", err)
	}

	return crypto.PubKeyToAddress(pubKey), nil
}
