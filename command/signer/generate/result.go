package generate

import (
	"bytes"
	"fmt"

	"github.com/0xPolygon/polygon-edge/command/helper"
)

type SignerGenerateResult struct {
	Backend       string `json:"backend"`
	ConfigPath    string `json:"config_path"`
	KMSKeyID      string `json:"kms_key_id,omitempty"`
	KMSRegion     string `json:"kms_region,omitempty"`
	HSMKeyLabel   string `json:"hsm_key_label,omitempty"`
	HSMTokenLabel string `json:"hsm_token_label,omitempty"`
}

func (r *SignerGenerateResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString("\n[SIGNER GENERATE]\n")

	kv := []string{
		fmt.Sprintf("Backend|%s", r.Backend),
		fmt.Sprintf("Config Path|%s", r.ConfigPath),
	}

	if r.KMSKeyID != "" {
		kv = append(kv,
			fmt.Sprintf("KMS Key ID|%s", r.KMSKeyID),
			fmt.Sprintf("KMS Region|%s", r.KMSRegion),
		)
	}

	if r.HSMKeyLabel != "" {
		kv = append(kv,
			fmt.Sprintf("HSM Key Label|%s", r.HSMKeyLabel),
			fmt.Sprintf("HSM Token Label|%s", r.HSMTokenLabel),
		)
	}

	buffer.WriteString(helper.FormatKV(kv))
	buffer.WriteString("\n")

	return buffer.String()
}
