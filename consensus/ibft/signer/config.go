package signer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/0xPolygon/polygon-edge/helper/common"
)

const SignerConfigFilename = "signer.json"

type SignerBackend string

const (
	SignerBackendLocal SignerBackend = "local"
	SignerBackendKMS   SignerBackend = "kms"
	SignerBackendHSM   SignerBackend = "hsm"
)

// KMSConfig holds the configuration needed to connect to AWS KMS
type KMSConfig struct {
	// Key ARN or alias, e.g. "arn:aws:kms:us-east-1:123456789012:key/abc-123"
	KeyID string `json:"key_id"`

	// AWS region where the key lives, e.g. "us-east-1"
	Region string `json:"region"`

	// Explicit credentials — leave empty to use instance role (recommended)
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`

	// Role to assume before calling KMS — use for cross-account keys
	AssumeRoleARN string `json:"assume_role_arn,omitempty"`

	// Endpoint override for LocalStack or testing — leave empty in production
	Endpoint string `json:"endpoint,omitempty"`
}

func (c *KMSConfig) validate() error {
	if c.KeyID == "" {
		return errors.New("kms.key_id is required")
	}

	if c.Region == "" {
		return errors.New("kms.region is required")
	}

	return nil
}

// HSMConfig holds the configuration needed to connect to AWS CloudHSM
type HSMConfig struct {
	LibPath        string `json:"lib_path,omitempty"`
	TokenLabel     string `json:"token_label,omitempty"`
	Pin            string `json:"pin"`
	PubKeyLabel    string `json:"key_label"`            // used for FindKeyPair
	PrivKeyLabel   string `json:"priv_key_label"`       // used for FindKeyPair
	ClusterID      string `json:"cluster_id,omitempty"` // used by CloudHSM client daemon
	MaxSessions    int    `json:"max_sessions,omitempty"`
	SessionTimeout int    `json:"session_timeout,omitempty"`
}

func (c *HSMConfig) validate() error {
	if c.Pin == "" {
		return errors.New("hsm.pin is required")
	}

	if c.PubKeyLabel == "" {
		return errors.New("hsm.key_label is required")
	}

	return nil
}

// SignerConfig holds the signer backend configuration for this node.
// Only one of KMS or HSM may be set at a time.
type SignerConfig struct {
	Backend SignerBackend `json:"backend"`
	KMS     *KMSConfig    `json:"kms,omitempty"` // set only when backend is "kms"
	HSM     *HSMConfig    `json:"hsm,omitempty"` // set only when backend is "hsm"
}

func (c *SignerConfig) validate() error {
	switch c.Backend {
	case SignerBackendKMS:
		if c.HSM != nil {
			return errors.New("hsm config must not be set when backend is kms")
		}

		if c.KMS == nil {
			return errors.New("kms config is required when backend is kms")
		}

		return c.KMS.validate()

	case SignerBackendHSM:
		if c.KMS != nil {
			return errors.New("kms config must not be set when backend is hsm")
		}

		if c.HSM == nil {
			return errors.New("hsm config is required when backend is hsm")
		}

		return c.HSM.validate()

	case SignerBackendLocal, "":
		if c.KMS != nil {
			return errors.New("kms config must not be set when backend is local")
		}

		if c.HSM != nil {
			return errors.New("hsm config must not be set when backend is local")
		}

		return nil

	default:
		return fmt.Errorf("unknown signer backend %q, supported: local, kms, hsm", c.Backend)
	}
}

// WriteConfig writes the SignerConfig to the specified path
func (c *SignerConfig) WriteConfig(path string) error {
	if err := c.validate(); err != nil {
		return fmt.Errorf("invalid signer config: %w", err)
	}

	jsonBytes, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return common.SaveFileSafe(path, jsonBytes, 0660)
}

// ReadConfig reads the SignerConfig from the specified path.
// If the file does not exist, nil is returned — caller falls back to local signing.
func ReadConfig(path string) (*SignerConfig, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to read signer config: %w", err)
	}

	cfg := &SignerConfig{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse signer config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid signer config: %w", err)
	}

	return cfg, nil
}
