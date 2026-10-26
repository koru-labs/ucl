package jsonrpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/0xPolygon/polygon-edge/migration/export"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

type fakeExporter struct {
	from, to uint64
	result   *export.RangeResult
}

func (f *fakeExporter) Range(from, to uint64) (*export.RangeResult, error) {
	f.from, f.to = from, to

	return f.result, nil
}

func TestDispatcher_UclNamespaceOnlyWithAnExporter(t *testing.T) {
	t.Parallel()

	d, err := newDispatcher(hclog.NewNullLogger(), newMockStore(), &dispatcherParams{})
	require.NoError(t, err)

	_, _, rpcErr := d.getFnHandler(Request{Method: "ucl_exportBlocks"})
	require.NotNil(t, rpcErr, "no exporter, no ucl namespace")
}

func TestUcl_ExportBlocks(t *testing.T) {
	t.Parallel()

	exporter := &fakeExporter{result: &export.RangeResult{
		Frames: [][]byte{{1, 2, 3}, {4}},
		Next:   12,
		Head:   40,
		Err:    &export.BlockError{Number: 12, Message: "boom", Irreproducible: true},
	}}

	d, err := newDispatcher(hclog.NewNullLogger(), newMockStore(), &dispatcherParams{migrationExporter: exporter})
	require.NoError(t, err)

	resp, err := d.Handle(context.Background(), []byte(
		`{"jsonrpc":"2.0","id":1,"method":"ucl_exportBlocks","params":["0xa","0x14"]}`))
	require.NoError(t, err)

	var out struct {
		Result ExportBlocksResult `json:"result"`
		Error  *ObjectError      `json:"error"`
	}
	require.NoError(t, json.Unmarshal(resp, &out))
	require.Nil(t, out.Error)
	require.Equal(t, uint64(10), exporter.from)
	require.Equal(t, uint64(20), exporter.to)
	require.Equal(t, []string{
		base64.StdEncoding.EncodeToString([]byte{1, 2, 3}),
		base64.StdEncoding.EncodeToString([]byte{4}),
	}, out.Result.Records)
	require.Equal(t, argUint64(12), out.Result.Next)
	require.Equal(t, argUint64(40), out.Result.Head)
	require.NotNil(t, out.Result.Error)
	require.Equal(t, argUint64(12), out.Result.Error.Block)
	require.True(t, out.Result.Error.Irreproducible)
}
