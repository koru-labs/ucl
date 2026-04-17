package generate

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
)

var (
	params = &generateParams{}
)

const (
	dirFlag     = "dir"
	backendFlag = "backend"
	// KMS flags
	kmsKeyIDFlag     = "kms-key-id"
	kmsRegionFlag    = "kms-region"
	kmsAccessKeyFlag = "kms-access-key"
	kmsKeyFlag       = "kms-secret-key"
	kmsRoleARNFlag   = "kms-role-arn"
	kmsEndpointFlag  = "kms-endpoint"
	// HSM flags
	hsmPinFlag            = "hsm-pin"
	hsmKeyLabelFlag       = "hsm-key-label"
	hsmLibPathFlag        = "hsm-lib-path"
	hsmLabelFlag          = "hsm-token-label"
	hsmClusterIDFlag      = "hsm-cluster-id"
	hsmMaxSessionsFlag    = "hsm-max-sessions"
	hsmSessionTimeoutFlag = "hsm-session-timeout"
)

const (
	defaultConfigFileName = "./signer.json"
)

var (
	errBackendRequired = fmt.Errorf(
		"backend is required; supported values: %s, %s",
		signer.SignerBackendKMS,
		signer.SignerBackendHSM,
	)
)

type generateParams struct {
	dir     string
	backend string

	// KMS
	kmsKeyID     string
	kmsRegion    string
	kmsAccessKey string
	kmsSecretKey string
	kmsRoleARN   string
	kmsEndpoint  string

	// HSM
	hsmPin            string
	hsmKeyLabel       string
	hsmLibPath        string
	hsmTokenLabel     string
	hsmClusterID      string
	hsmMaxSessions    int
	hsmSessionTimeout int
}

func (p *generateParams) getRequiredFlags() []string {
	return []string{
		backendFlag,
	}
}

func (p *generateParams) generateSignerConfig() (*signer.SignerConfig, error) {
	cfg := &signer.SignerConfig{
		Backend: signer.SignerBackend(p.backend),
	}

	switch cfg.Backend {
	case signer.SignerBackendKMS:
		cfg.KMS = &signer.KMSConfig{
			KeyID:           p.kmsKeyID,
			Region:          p.kmsRegion,
			AccessKeyID:     p.kmsAccessKey,
			SecretAccessKey: p.kmsSecretKey,
			AssumeRoleARN:   p.kmsRoleARN,
			Endpoint:        p.kmsEndpoint,
		}

	case signer.SignerBackendHSM:
		cfg.HSM = &signer.HSMConfig{
			Pin:            p.hsmPin,
			KeyLabel:       p.hsmKeyLabel,
			LibPath:        p.hsmLibPath,
			TokenLabel:     p.hsmTokenLabel,
			ClusterID:      p.hsmClusterID,
			MaxSessions:    p.hsmMaxSessions,
			SessionTimeout: p.hsmSessionTimeout,
		}

	default:
		return nil, errBackendRequired
	}

	return cfg, nil
}

func (p *generateParams) writeSignerConfig() error {
	cfg, err := p.generateSignerConfig()
	if err != nil {
		return err
	}

	if err := cfg.WriteConfig(p.dir); err != nil {
		return fmt.Errorf("unable to write signer configuration file: %w", err)
	}

	return nil
}

func (p *generateParams) getResult() command.CommandResult {
	return &SignerGenerateResult{
		Backend:       p.backend,
		ConfigPath:    p.dir,
		KMSKeyID:      p.kmsKeyID,
		KMSRegion:     p.kmsRegion,
		HSMKeyLabel:   p.hsmKeyLabel,
		HSMTokenLabel: p.hsmTokenLabel,
	}
}

func (p *generateParams) getKMSRequiredFlags() []string {
	return []string{
		kmsKeyIDFlag,
		kmsRegionFlag,
	}
}

func (p *generateParams) getHSMRequiredFlags() []string {
	return []string{
		hsmLibPathFlag,
		hsmPinFlag,
		hsmKeyLabelFlag,
		hsmLabelFlag,
	}
}
