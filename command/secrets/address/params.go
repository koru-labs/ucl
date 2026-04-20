package address

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/0xPolygon/polygon-edge/crypto"
)

const pubkeyFlag = "pubkey"

var params = &addressParams{}

type addressParams struct {
	pubkeyB64 string
}

type addressResult struct {
	Address string `json:"address"`
}

func (r *addressResult) GetOutput() string {
	out, _ := json.Marshal(r)

	return string(out)
}

func (p *addressParams) deriveAddress() (string, error) {
	der, err := base64.StdEncoding.DecodeString(p.pubkeyB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 pubkey: %w", err)
	}

	if len(der) < 65 {
		return "", fmt.Errorf("DER public key too short")
	}

	raw := der[len(der)-65:]
	if raw[0] != 0x04 {
		return "", fmt.Errorf("expected uncompressed EC point (0x04 prefix)")
	}

	pub, err := crypto.ParsePublicKey(raw)
	if err != nil {
		return "", fmt.Errorf("failed to parse public key: %w", err)
	}

	return crypto.PubKeyToAddress(pub).String(), nil
}
