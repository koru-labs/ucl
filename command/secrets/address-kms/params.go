package addresskms

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/types"
)

const kmsConfigPathFlag = "kms-config-path"

var params = &addressParams{}

type addressParams struct {
	kmsConfigPath string
}

type addressResult struct {
	Address string `json:"address"`
}

func (r *addressResult) GetOutput() string {
	out, _ := json.Marshal(r)

	return string(out)
}

func loadKMSConfig(path string) (*signer.KMSConfig, error) {
	cfg, err := signer.ReadConfig(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read signer config at %s: %w", path, err)
	}

	if cfg == nil {
		return nil, fmt.Errorf("signer config not found at %s", path)
	}

	if cfg.Backend != signer.SignerBackendKMS {
		return nil, fmt.Errorf("signer backend is %q, expected %q", cfg.Backend, signer.SignerBackendKMS)
	}

	if cfg.KMS == nil {
		return nil, errors.New("signer config has backend=kms but kms section is empty")
	}

	return cfg.KMS, nil
}

func deriveValidatorAddress(cfg *signer.KMSConfig) (types.Address, error) {
	km, err := signer.NewKMSKeyManager(cfg)
	if err != nil {
		return types.Address{}, fmt.Errorf("kms: %w", err)
	}

	return km.Address(), nil
}
