package evm

import (
	"context"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/holiman/uint256"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defaultInitialGas = uint64(1000)
)

type codeHelper struct {
	buf []byte
}

func (c *codeHelper) Code() []byte {
	return c.buf
}

func (c *codeHelper) push1() {
	c.buf = append(c.buf, PUSH1)
	c.buf = append(c.buf, 0x1)
}

func (c *codeHelper) opDup() {
	c.buf = append(c.buf, DUP16)
}

func (c *codeHelper) pop() {
	c.buf = append(c.buf, POP)
}

func getState(forks *chain.ForksInTime) (*state, func()) {
	c := statePool.Get().(*state)

	c.config = forks
	c.gas = defaultInitialGas
	c.msg = &runtime.Contract{}

	return c, func() {
		c.reset()
		statePool.Put(c)
	}
}

func TestStackTop(t *testing.T) {
	s, closeFn := getState(&chain.ForksInTime{})
	defer closeFn()

	s.push(*uint256.NewInt(1))
	s.push(*uint256.NewInt(2))

	assert.Equal(t, *uint256.NewInt(2), *s.top())
	assert.Equal(t, s.stackSize(), 2)
}

func TestStackOverflow(t *testing.T) {
	code := codeHelper{}
	for i := 0; i < stackSize; i++ {
		code.push1()
	}

	s, closeFn := getState(&chain.ForksInTime{})
	defer closeFn()

	s.code = code.buf
	s.gas = 10000
	s.host = &mockHost{}

	_, err := s.Run()
	assert.NoError(t, err)

	// add one more item to the stack
	code.push1()

	s.reset()
	s.code = code.buf
	s.gas = 10000
	s.host = &mockHost{}

	_, err = s.Run()
	assert.Equal(t, &runtime.StackOverflowError{StackLen: stackSize + 1, Limit: stackSize}, err)
}

func TestStackUnderflow(t *testing.T) {
	s, closeFn := getState(&chain.ForksInTime{})
	defer closeFn()

	code := codeHelper{}
	for i := 0; i < 10; i++ {
		code.push1()
	}

	for i := 0; i < 10; i++ {
		code.pop()
	}

	s.code = code.buf
	s.gas = 10000
	s.host = &mockHost{}

	_, err := s.Run()
	assert.NoError(t, err)

	code.pop()

	s.reset()
	s.code = code.buf
	s.gas = 10000
	s.host = &mockHost{}

	_, err = s.Run()
	// need at least one operation on the stack
	assert.Equal(t, &runtime.StackUnderflowError{StackLen: 0, Required: 1}, err)
}

func TestOpcodeNotFound(t *testing.T) {
	t.Run("code contains undefined opcode", func(t *testing.T) {
		s, closeFn := getState(&chain.ForksInTime{})
		defer closeFn()

		s.code = []byte{0xA5}
		s.gas = 1000
		s.host = &mockHost{}

		_, err := s.Run()
		assert.Equal(t, errOpCodeNotFound, err)
	})

	t.Run("code contains invalid opcode (0xFE)", func(t *testing.T) {
		s, closeFn := getState(&chain.ForksInTime{})
		defer closeFn()

		s.code = []byte{0xFE}
		s.gas = 1000
		s.host = &mockHost{}

		_, err := s.Run()
		assert.Equal(t, errOpCodeNotFound, err)
	})
}

func TestRun_ExecutionAborted(t *testing.T) {
	loop := []byte{JUMPDEST, PUSH1, 0x00, JUMP}

	t.Run("already-cancelled context", func(t *testing.T) {
		s, closeFn := getState(&chain.ForksInTime{})
		defer closeFn()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		s.evm = NewEVM()
		s.evm.SetExecutionContext(ctx)
		s.host = &mockHost{}
		s.code = loop
		s.bitmap.setCode(loop)
		s.gas = 1 << 40

		_, err := s.Run()
		require.ErrorIs(t, err, runtime.ErrExecutionAborted)
	})

	t.Run("timeout during infinite loop", func(t *testing.T) {
		s, closeFn := getState(&chain.ForksInTime{})
		defer closeFn()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		s.evm = NewEVM()
		s.evm.SetExecutionContext(ctx)
		s.host = &mockHost{}
		s.code = loop
		s.bitmap.setCode(loop)
		s.gas = 1 << 40

		start := time.Now().UTC()
		_, err := s.Run()
		elapsed := time.Since(start)

		require.ErrorIs(t, err, runtime.ErrExecutionAborted)
		require.Less(t, elapsed, 2*time.Second)
	})

	t.Run("nil evm is unchanged consensus path", func(t *testing.T) {
		s, closeFn := getState(&chain.ForksInTime{})
		defer closeFn()

		s.evm = nil
		s.host = &mockHost{}
		s.code = loop
		s.bitmap.setCode(loop)
		s.gas = 20

		_, err := s.Run()
		require.ErrorIs(t, err, runtime.ErrOutOfGas)
	})

	t.Run("evm without context is unchanged consensus path", func(t *testing.T) {
		s, closeFn := getState(&chain.ForksInTime{})
		defer closeFn()

		s.evm = NewEVM()
		s.host = &mockHost{}
		s.code = loop
		s.bitmap.setCode(loop)
		s.gas = 20

		_, err := s.Run()
		require.ErrorIs(t, err, runtime.ErrOutOfGas)
	})

	t.Run("cancelled context on STOP still aborts", func(t *testing.T) {
		s, closeFn := getState(&chain.ForksInTime{})
		defer closeFn()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		s.evm = NewEVM()
		s.evm.SetExecutionContext(ctx)
		s.host = &mockHost{}
		s.code = []byte{byte(STOP)}
		s.gas = 1000

		_, err := s.Run()
		require.ErrorIs(t, err, runtime.ErrExecutionAborted)
	})
}

func TestErrorHandlingStopsContractExecution(t *testing.T) {
	code := codeHelper{}
	code.opDup()
	code.opDup()

	s, closeFn := getState(&chain.ForksInTime{})
	defer closeFn()

	s.code = code.buf
	s.gas = 10000
	s.host = &mockHost{}

	_, err := s.Run()
	assert.Error(t, err, "The EVM did not handle an error")
	assert.Equal(t, s.ip, 0, "The EVM did not executingon first error.")
}
