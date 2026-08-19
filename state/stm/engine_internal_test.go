package stm

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/types"
)

// TestWriteTx_PanicBecomesError pins down that a panic beneath transaction execution is turned
// into an ordinary error instead of escaping. Execution runs on a worker goroutine, so an
// escaping panic is unrecoverable by any caller and would take the whole validator process down
// over what should only cost one failed block proposal. A nil Transition gives us a real panic
// (nil dereference) from inside the same call path, without needing to plant one.
func TestWriteTx_PanicBecomesError(t *testing.T) {
	tx := &types.Transaction{Hash: types.Hash{0x01}}

	require.NotPanics(t, func() {
		receipt, execErr, panicErr := writeTx(nil, tx, 3 /*idx*/, 2 /*incarnation*/)

		require.Error(t, panicErr, "the panic must surface as an error")
		require.Contains(t, panicErr.Error(), "panic executing tx 3",
			"the error must identify the transaction that blew up")
		require.Contains(t, panicErr.Error(), "incarnation 2",
			"and which attempt it was, since the same tx runs repeatedly")

		require.Nil(t, receipt, "a panicked execution must not hand back a receipt")
		require.NoError(t, execErr, "a panic is reported through panicErr, not as a tx-level error")
	})
}
