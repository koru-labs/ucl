package signer

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/validators"
)

type KeyManagerFactory func(valType validators.ValidatorType) (KeyManager, error)

// Local — uses SecretsManager as before
func LocalKeyManagerFactory(sm secrets.SecretsManager) KeyManagerFactory {
	return func(valType validators.ValidatorType) (KeyManager, error) {
		return NewKeyManagerFromType(sm, valType)
	}
}

// KMS — only ECDSA, no SecretsManager involved
func KMSKeyManagerFactory(cfg *KMSConfig) KeyManagerFactory {
	return func(valType validators.ValidatorType) (KeyManager, error) {
		if valType != validators.ECDSAValidatorType {
			return nil, fmt.Errorf("KMS backend only supports ECDSA, got %s", valType)
		}

		return NewKMSKeyManager(cfg)
	}
}

// HSM — only ECDSA, no SecretsManager involved
func HSMKeyManagerFactory(cfg *HSMConfig) KeyManagerFactory {
	return func(valType validators.ValidatorType) (KeyManager, error) {
		if valType != validators.ECDSAValidatorType {
			return nil, fmt.Errorf("HSM backend only supports ECDSA, got %s", valType)
		}

		return NewHSMKeyManagerFromConfig(cfg)
	}
}

func LoadKeyManagerFactory(cfg *SignerConfig, fallback secrets.SecretsManager) (KeyManagerFactory, error) {
	if cfg == nil {
		// no signer config — existing nodes unaffected
		return LocalKeyManagerFactory(fallback), nil
	}

	switch cfg.Backend {
	case SignerBackendKMS:
		return KMSKeyManagerFactory(cfg.KMS), nil
	case SignerBackendHSM:
		return HSMKeyManagerFactory(cfg.HSM), nil
	case SignerBackendLocal, "":
		return LocalKeyManagerFactory(fallback), nil
	default:
		return nil, fmt.Errorf("unknown signer backend %q", cfg.Backend)
	}
}
