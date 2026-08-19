package init

import (
	"bytes"
	"fmt"

	"github.com/0xPolygon/polygon-edge/command/helper"
	"github.com/0xPolygon/polygon-edge/types"
)

type SecretsInitResult struct {
	Address   types.Address `json:"address"`
	NodeID    string        `json:"node_id"`
	Generated string        `json:"generated"`
	Insecure  bool          `json:"insecure"`
}

func (r *SecretsInitResult) GetOutput() string {
	var buffer bytes.Buffer

	vals := make([]string, 0, 2)

	vals = append(
		vals,
		fmt.Sprintf("Public key (address)|%s", r.Address.String()),
	)

	vals = append(vals, fmt.Sprintf("Node ID|%s", r.NodeID))

	if r.Insecure {
		buffer.WriteString("\n[WARNING: INSECURE LOCAL SECRETS - SHOULD NOT BE RUN IN PRODUCTION]\n")
	}

	buffer.WriteString("\n[SECRETS INIT]\n")
	buffer.WriteString(helper.FormatKV(vals))
	buffer.WriteString("\n")

	return buffer.String()
}
