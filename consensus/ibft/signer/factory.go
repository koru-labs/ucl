package signer

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/validators"
)

// BackendType selects the signing backend at node startup
type BackendType string

const (
	BackendLocal  BackendType = "local"
	BackendAWSKMS BackendType = "aws-kms"
)

// KeyManagerConfig holds all configuration needed to construct either backend.
// Only the fields relevant to the chosen Backend need to be populated.
type KeyManagerConfig struct {
	// Backend selects which signing backend to use
	Backend BackendType

	// ValidatorType is used only when Backend == BackendLocal
	ValidatorType validators.ValidatorType

	// SecretsManager is used only when Backend == BackendLocal
	SecretsManager secrets.SecretsManager

	// AWSKMS is used only when Backend == BackendAWSKMS
	AWSKMS KMSConfig
}

// LocalConfig holds configuration for the local (classic) key manager
type LocalConfig struct {
	// SecretsManager is the existing polygon-edge secrets abstraction.
	// When nil and backend is local, construction will fail.
	SecretsManager secrets.SecretsManager
}

// NewKeyManagerFromConfig is the single decision point for which backend
// is used. Everything downstream receives a KeyManager and is blind to
// which backend was chosen.
//
// Usage:
//
//	// Classic — behaviour identical to before
//	km, err := NewKeyManagerFromConfig(KeyManagerConfig{
//	    Backend: BackendLocal,
//	    Local:   LocalConfig{SecretsManager: sm},
//	})
//
//	// HSM — no key file, signs via KMS
//	km, err := NewKeyManagerFromConfig(KeyManagerConfig{
//	    Backend: BackendAWSKMS,
//	    AWSKMS:  KMSConfig{KeyID: "arn:...", Region: "us-east-1"},
//	})
func NewKeyManagerFromConfig(cfg KeyManagerConfig) (KeyManager, error) {
	switch cfg.Backend {
	case BackendLocal:
		return newLocalKeyManager(cfg.SecretsManager, cfg.ValidatorType)

	case BackendAWSKMS:
		return NewKMSKeyManager(cfg.AWSKMS, nil)

	default:
		return nil, fmt.Errorf(
			"unknown signing backend %q — valid options are %q and %q",
			cfg.Backend, BackendLocal, BackendAWSKMS,
		)
	}
}

func newLocalKeyManager(
	manager secrets.SecretsManager,
	validatorType validators.ValidatorType,
) (KeyManager, error) {
	if manager == nil {
		return nil, fmt.Errorf("local backend requires a non-nil SecretsManager")
	}

	return NewKeyManagerFromType(manager, validatorType)
}
