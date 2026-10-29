package jsonrpc

import (
	"encoding/base64"
	"errors"

	"github.com/0xPolygon/polygon-edge/migration/export"
)

// MigrationExporter serves the ucl to pitau migration's block records (migration/export).
type MigrationExporter interface {
	Range(from, to uint64) (*export.RangeResult, error)
}

// Ucl is the `ucl` JSON-RPC namespace, registered only on nodes started with a migration export
// directory.
type Ucl struct {
	exporter MigrationExporter
}

// ExportBlocksResult is the answer to `ucl_exportBlocks`.
type ExportBlocksResult struct {
	// Records are the frames of blocks from, from+1, … (migration/export.EncodeFrame), base64.
	Records []string `json:"records"`
	// Next is the first block not returned: where the caller asks next.
	Next argUint64 `json:"next"`
	// Head is the node's head when it answered.
	Head argUint64 `json:"head"`
	// Error names the block that stopped the range, if one did.
	Error *ExportBlockError `json:"error,omitempty"`
}

// ExportBlockError is a block the node could not export.
type ExportBlockError struct {
	Block   argUint64 `json:"block"`
	Message string    `json:"message"`
	// Irreproducible is set when the block's re-execution does not match what ucl stored.
	Irreproducible bool `json:"irreproducible"`
}

// ExportBlocks returns the records of blocks from..to, as many as the node's limits allow.
func (u *Ucl) ExportBlocks(from, to argUint64) (interface{}, error) {
	if u.exporter == nil {
		return nil, errors.New("the migration exporter is not enabled")
	}

	result, err := u.exporter.Range(uint64(from), uint64(to))
	if err != nil {
		return nil, err
	}

	out := &ExportBlocksResult{
		Records: make([]string, len(result.Frames)),
		Next:    argUint64(result.Next),
		Head:    argUint64(result.Head),
	}

	for i, frame := range result.Frames {
		out.Records[i] = base64.StdEncoding.EncodeToString(frame)
	}

	if result.Err != nil {
		out.Error = &ExportBlockError{
			Block:          argUint64(result.Err.Number),
			Message:        result.Err.Message,
			Irreproducible: result.Err.Irreproducible,
		}
	}

	return out, nil
}
