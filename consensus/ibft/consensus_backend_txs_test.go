package ibft

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/consensus/ibft/hook"
	"github.com/0xPolygon/polygon-edge/state"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	testBlockGasLimit = uint64(1_000_000_000)
	testBuildWindow   = 300 * time.Millisecond
)

// fakeTxPool is a minimal txPoolInterface that serves a fixed list of txs and records pool mutations.
// testify mocks are avoided on purpose: they format every call argument, which is very slow for the
// multi-megabyte transactions needed to fill a block by RLP size.
type fakeTxPool struct {
	txs     []*types.Transaction
	peeked  int
	popped  []*types.Transaction
	dropped []*types.Transaction
	demoted []*types.Transaction
}

var _ txPoolInterface = (*fakeTxPool)(nil)

func (p *fakeTxPool) Prepare() {}

func (p *fakeTxPool) Length() uint64 {
	return uint64(len(p.txs) - p.peeked)
}

func (p *fakeTxPool) Peek() *types.Transaction {
	if p.peeked >= len(p.txs) {
		return nil
	}

	tx := p.txs[p.peeked]
	p.peeked++

	return tx
}

func (p *fakeTxPool) Pop(tx *types.Transaction) {
	p.popped = append(p.popped, tx)
}

func (p *fakeTxPool) Drop(tx *types.Transaction) {
	p.dropped = append(p.dropped, tx)
}

func (p *fakeTxPool) Demote(tx *types.Transaction) {
	p.demoted = append(p.demoted, tx)
}

func (p *fakeTxPool) ResetWithBlock(*types.Block) {}

func (p *fakeTxPool) SetSealing(bool) {}

func (p *fakeTxPool) ReinsertProposed() {}

func (p *fakeTxPool) ClearProposed() {}

// fakeTransition records executed txs and returns the configured error for each of them
type fakeTransition struct {
	err     error
	written []*types.Transaction
}

func (t *fakeTransition) Write(txn *types.Transaction) error {
	t.written = append(t.written, txn)

	return t.err
}

// newTestTx returns a legacy tx from the given sender whose RLP size is driven by inputSize.
// The RLP size is computed (and cached) up front so it is not measured inside the block build window.
func newTestTx(from types.Address, gas uint64, inputSize int) *types.Transaction {
	to := types.StringToAddress("0xff")

	tx := &types.Transaction{
		From:     from,
		To:       &to,
		Value:    big.NewInt(0),
		GasPrice: big.NewInt(1),
		Gas:      gas,
		Input:    make([]byte, inputSize),
	}

	_ = tx.Size()

	return tx
}

func newTestBackend(pool txPoolInterface) *backendIBFT {
	forkManager := &forkManagerMock{}
	forkManager.On("GetHooks", mock.Anything).Return(&hook.Hooks{})

	return &backendIBFT{
		logger:      hclog.NewNullLogger(),
		txpool:      pool,
		forkManager: forkManager,
	}
}

func TestIBFTBackend_WriteTransaction(t *testing.T) {
	t.Parallel()

	sender := types.StringToAddress("1")
	regularTx := newTestTx(sender, 21_000, 100)
	oversizedTx := newTestTx(sender, 21_000, int(types.MaxTxsRlpSize))

	testCases := []struct {
		name            string
		tx              *types.Transaction
		usedRlpSize     uint64
		writeErr        error
		expectContinue  bool
		expectStatus    status
		expectWrite     bool
		expectDrop      bool
		expectDemote    bool
		expectPop       bool
		expectedRlpSize uint64
	}{
		{
			name:            "tx fits exactly into the remaining block space",
			tx:              regularTx,
			usedRlpSize:     types.MaxTxsRlpSize - regularTx.Size(),
			expectContinue:  true,
			expectStatus:    success,
			expectWrite:     true,
			expectPop:       true,
			expectedRlpSize: types.MaxTxsRlpSize,
		},
		{
			name:            "block is full by RLP size: stop without dropping the tx",
			tx:              regularTx,
			usedRlpSize:     types.MaxTxsRlpSize - regularTx.Size() + 1,
			expectedRlpSize: types.MaxTxsRlpSize - regularTx.Size() + 1,
		},
		{
			name:           "tx gas exceeds block gas limit: drop and continue",
			tx:             newTestTx(sender, testBlockGasLimit+1, 100),
			expectContinue: true,
			expectStatus:   fail,
			expectDrop:     true,
		},
		{
			name:           "tx larger than the whole block: drop and continue",
			tx:             oversizedTx,
			expectContinue: true,
			expectStatus:   fail,
			expectDrop:     true,
		},
		{
			name:        "block gas limit reached during execution: stop without dropping the tx",
			tx:          regularTx,
			writeErr:    state.NewGasLimitReachedTransitionApplicationError(assert.AnError),
			expectWrite: true,
		},
		{
			name:           "recoverable execution error: demote and continue",
			tx:             regularTx,
			writeErr:       state.NewTransitionApplicationError(assert.AnError, true),
			expectContinue: true,
			expectStatus:   skip,
			expectWrite:    true,
			expectDemote:   true,
		},
	}

	for _, tc := range testCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pool := &fakeTxPool{}
			transition := &fakeTransition{err: tc.writeErr}
			backend := newTestBackend(pool)
			backend.buildBlockTxsRlpSize = tc.usedRlpSize

			result, shouldContinue := backend.writeTransaction(tc.tx, transition, testBlockGasLimit)

			require.Equal(t, tc.expectContinue, shouldContinue)

			if tc.expectContinue {
				require.NotNil(t, result)
				assert.Equal(t, tc.expectStatus, result.status)
			} else {
				assert.Nil(t, result)
			}

			assert.Equal(t, tc.expectWrite, len(transition.written) == 1)
			assert.Equal(t, tc.expectDrop, len(pool.dropped) == 1)
			assert.Equal(t, tc.expectDemote, len(pool.demoted) == 1)
			assert.Equal(t, tc.expectPop, len(pool.popped) == 1)
			assert.Equal(t, tc.expectedRlpSize, backend.buildBlockTxsRlpSize)
		})
	}
}

// TestIBFTBackend_WriteTransactions_BlockFullKeepsRemainingAccounts is a regression test:
// once the block is full by RLP size, block building must stop and must not drop the
// pending transactions of the remaining accounts from the pool.
func TestIBFTBackend_WriteTransactions_BlockFullKeepsRemainingAccounts(t *testing.T) {
	t.Parallel()

	// the first tx nearly fills the block, the txs of the other accounts no longer fit
	bigTx := newTestTx(types.StringToAddress("1"), 21_000, int(types.MaxTxsRlpSize)-1_000)
	nextTx := newTestTx(types.StringToAddress("2"), 21_000, 2_000)
	lastTx := newTestTx(types.StringToAddress("3"), 21_000, 100)

	require.LessOrEqual(t, bigTx.Size(), uint64(types.MaxTxsRlpSize))
	require.Greater(t, bigTx.Size()+nextTx.Size(), uint64(types.MaxTxsRlpSize))

	pool := &fakeTxPool{txs: []*types.Transaction{bigTx, nextTx, lastTx}}
	transition := &fakeTransition{}
	backend := newTestBackend(pool)

	ctx, cancel := context.WithTimeout(context.Background(), testBuildWindow)
	defer cancel()

	executed := backend.writeTransactions(ctx, testBlockGasLimit, 1, transition)

	assert.Equal(t, []*types.Transaction{bigTx}, executed)
	assert.Equal(t, []*types.Transaction{bigTx}, transition.written)
	assert.Equal(t, []*types.Transaction{bigTx}, pool.popped)
	assert.Empty(t, pool.dropped, "no account may be dropped because the block is full")
	assert.Empty(t, pool.demoted)
	assert.Equal(t, 2, pool.peeked, "building must stop at the first tx that does not fit")
	assert.Equal(t, bigTx.Size(), backend.buildBlockTxsRlpSize)
}

// TestIBFTBackend_WriteTransactions_OversizedTxDroppedAndBuildingContinues verifies that a tx
// which can never fit into a block is still dropped while the remaining txs are processed.
func TestIBFTBackend_WriteTransactions_OversizedTxDroppedAndBuildingContinues(t *testing.T) {
	t.Parallel()

	oversizedTx := newTestTx(types.StringToAddress("1"), 21_000, int(types.MaxTxsRlpSize))
	regularTx := newTestTx(types.StringToAddress("2"), 21_000, 100)

	require.Greater(t, oversizedTx.Size(), uint64(types.MaxTxsRlpSize))

	pool := &fakeTxPool{txs: []*types.Transaction{oversizedTx, regularTx}}
	transition := &fakeTransition{}
	backend := newTestBackend(pool)

	ctx, cancel := context.WithTimeout(context.Background(), testBuildWindow)
	defer cancel()

	executed := backend.writeTransactions(ctx, testBlockGasLimit, 1, transition)

	assert.Equal(t, []*types.Transaction{regularTx}, executed)
	assert.Equal(t, []*types.Transaction{oversizedTx}, pool.dropped)
	assert.Equal(t, []*types.Transaction{regularTx}, pool.popped)
	assert.Equal(t, []*types.Transaction{regularTx}, transition.written)
	assert.Equal(t, regularTx.Size(), backend.buildBlockTxsRlpSize)
}
