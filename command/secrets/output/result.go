package output

import (
	"bytes"
	"fmt"

	"github.com/0xPolygon/polygon-edge/command/helper"
)

// SecretsOutputAllResult for default output case
type SecretsOutputAllResult struct {
	Address string `json:"address"`
	NodeID  string `json:"node_id"`
}

// SecretsOutputNodeIDResult for `--node` output case
type SecretsOutputNodeIDResult struct {
	NodeID string `json:"node_id"`
}

// SecretsOutputValidatorResult for `--validator` output case
type SecretsOutputValidatorResult struct {
	Address string `json:"address"`
}

func (r *SecretsOutputNodeIDResult) GetOutput() string {
	return r.NodeID
}

func (r *SecretsOutputValidatorResult) GetOutput() string {
	return r.Address
}

func (r *SecretsOutputAllResult) GetOutput() string {
	var buffer bytes.Buffer

	vals := make([]string, 0, 2)

	vals = append(
		vals,
		fmt.Sprintf("Public key (address)|%s", r.Address),
	)

	vals = append(
		vals,
		fmt.Sprintf("Node ID|%s", r.NodeID),
	)

	buffer.WriteString("\n[SECRETS OUTPUT]\n")
	buffer.WriteString(helper.FormatKV(vals))

	buffer.WriteString("\n")

	return buffer.String()
}
