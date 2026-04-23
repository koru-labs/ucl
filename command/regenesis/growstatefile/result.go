package growstatefile

import (
	"bytes"
	"fmt"

	"github.com/0xPolygon/polygon-edge/command/helper"
	"github.com/0xPolygon/polygon-edge/types"
)

type growStateResult struct {
	StateRootHash types.Hash    `json:"rootHash"`
	TotalSize     int64         `json:"totalSize"`
	TimeElapsed   float64       `json:"timeElapsed"`
	GenesisHeader *types.Header `json:"genesisHeader,omitempty"`
}

func (r *growStateResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString("\n[Grow state file SUCCESS]\n")

	columns := []string{
		fmt.Sprintf("StateRootHash|%s", r.StateRootHash),
		fmt.Sprintf("TotalSizeInMB|%v", float64(r.TotalSize)/(1024*1024)),
		fmt.Sprintf("TimeElapsedInSeconds|%.2f", r.TimeElapsed),
	}

	if r.GenesisHeader != nil {
		columns = append(columns, fmt.Sprintf("GenesisHash|%s", r.GenesisHeader.Hash))
		columns = append(columns, fmt.Sprintf("GenesisStateRootHash|%s", r.GenesisHeader.StateRoot))
	}

	buffer.WriteString(helper.FormatKV(columns))

	return buffer.String()
}
