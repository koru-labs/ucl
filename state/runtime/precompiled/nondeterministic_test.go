package precompiled

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/contracts"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/require"
)

func TestNondeterministicPrecompileDisabledByDefault(t *testing.T) {
	t.Setenv("UCL_TEST_NONDET_PRECOMPILE", "")

	p := NewPrecompiled()
	ok := p.CanRun(
		&runtime.Contract{CodeAddress: contracts.NondeterministicTestPrecompile},
		nil,
		&chain.ForksInTime{},
	)
	require.False(t, ok)
}

func TestNondeterministicPrecompileWritesPID(t *testing.T) {
	t.Setenv("UCL_TEST_NONDET_PRECOMPILE", "1")

	caller := types.Address{0x11}

	p := NewPrecompiled()
	contract := &runtime.Contract{
		CodeAddress: contracts.NondeterministicTestPrecompile,
		Caller:      caller,
		Gas:         1000,
	}
	require.True(t, p.CanRun(contract, nil, &chain.ForksInTime{}))

	host := newRecordingHost(t)
	result := p.Run(contract, host, &chain.ForksInTime{})
	require.False(t, result.Failed())
	require.Len(t, result.ReturnValue, 32)

	want := make([]byte, 32)
	binary.BigEndian.PutUint64(want[24:], uint64(os.Getpid()))
	require.Equal(t, want, result.ReturnValue)

	got, ok := host.states[caller][types.ZeroHash]
	require.True(t, ok)
	require.Equal(t, types.BytesToHash(want), got)
}

type recordingHost struct {
	dummyHost
	states map[types.Address]map[types.Hash]types.Hash
}

func newRecordingHost(t *testing.T) *recordingHost {
	t.Helper()

	return &recordingHost{
		dummyHost: dummyHost{t: t},
		states:    map[types.Address]map[types.Hash]types.Hash{},
	}
}

func (r *recordingHost) SetState(addr types.Address, key, value types.Hash) {
	if r.states[addr] == nil {
		r.states[addr] = map[types.Hash]types.Hash{}
	}

	r.states[addr][key] = value
}
