package state

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
)

var (
	contractAddr = types.StringToAddress("dd")
)

func TestOverride(t *testing.T) {
	t.Parallel()

	state := newStateWithPreState(map[types.Address]*PreState{
		{0x0}: {
			Nonce:   1,
			Balance: 1,
			State: map[types.Hash]types.Hash{
				types.ZeroHash: {0x1},
			},
		},
		{0x1}: {
			State: map[types.Hash]types.Hash{
				types.ZeroHash: {0x1},
			},
		},
	}, nil)

	nonce := uint64(2)
	balance := big.NewInt(2)
	code := []byte{0x1}

	tt := NewTransition(chain.ForksInTime{}, state, newTxn(state))

	require.Empty(t, tt.state.GetCode(types.ZeroAddress))

	err := tt.WithStateOverride(types.StateOverride{
		{0x0}: types.OverrideAccount{
			Nonce:   &nonce,
			Balance: balance,
			Code:    code,
			StateDiff: map[types.Hash]types.Hash{
				types.ZeroHash: {0x2},
			},
		},
		{0x1}: types.OverrideAccount{
			State: map[types.Hash]types.Hash{
				{0x1}: {0x1},
			},
		},
	})
	require.NoError(t, err)

	require.Equal(t, nonce, tt.state.GetNonce(types.ZeroAddress))
	require.Equal(t, balance, tt.state.GetBalance(types.ZeroAddress))
	require.Equal(t, code, tt.state.GetCode(types.ZeroAddress))
	require.Equal(t, types.Hash{0x2}, tt.state.GetState(types.ZeroAddress, types.ZeroHash))

	// state is fully replaced
	require.Equal(t, types.Hash{0x0}, tt.state.GetState(types.Address{0x1}, types.ZeroHash))
	require.Equal(t, types.Hash{0x1}, tt.state.GetState(types.Address{0x1}, types.Hash{0x1}))
}

func Test_Transition_checkDynamicFees(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseFee *big.Int
		tx      *types.Transaction
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name:    "happy path",
			baseFee: big.NewInt(100),
			tx: &types.Transaction{
				Type:      types.DynamicFeeTx,
				GasFeeCap: big.NewInt(100),
				GasTipCap: big.NewInt(100),
			},
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				assert.NoError(t, err, i)

				return false
			},
		},
		{
			name:    "happy path with empty values",
			baseFee: big.NewInt(0),
			tx: &types.Transaction{
				Type:      types.DynamicFeeTx,
				GasFeeCap: big.NewInt(0),
				GasTipCap: big.NewInt(0),
			},
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				assert.NoError(t, err, i)

				return false
			},
		},
		{
			name:    "gas fee cap less than base fee",
			baseFee: big.NewInt(20),
			tx: &types.Transaction{
				Type:      types.DynamicFeeTx,
				GasFeeCap: big.NewInt(10),
				GasTipCap: big.NewInt(0),
			},
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				expectedError := fmt.Sprintf("max fee per gas less than block base fee: "+
					"address %s, GasFeeCap/GasPrice: 10, BaseFee: 20", types.ZeroAddress)
				assert.EqualError(t, err, expectedError, i)

				return true
			},
		},
		{
			name:    "gas fee cap less than tip cap",
			baseFee: big.NewInt(5),
			tx: &types.Transaction{
				Type:      types.DynamicFeeTx,
				GasFeeCap: big.NewInt(10),
				GasTipCap: big.NewInt(15),
			},
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				expectedError := fmt.Sprintf("max priority fee per gas higher than max fee per gas: "+
					"address %s, GasTipCap: 15, GasFeeCap: 10", types.ZeroAddress)
				assert.EqualError(t, err, expectedError, i)

				return true
			},
		},
	}

	for _, tt := range tests {
		tt := tt

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := &Transition{
				ctx: runtime.TxContext{
					BaseFee: tt.baseFee,
				},
				config: chain.ForksInTime{
					London: true,
				},
			}

			err := tr.checkDynamicFees(tt.tx)
			tt.wantErr(t, err, fmt.Sprintf("checkDynamicFees(%v)", tt.tx))
		})
	}
}

var (
	// beneficiary receiving the funds
	beneficiaryAddr = types.StringToAddress("0xbb")
	// code of the self-destructing contract, used to prove the code survives EIP-6780
	contractCode = []byte{0x60, 0x00, 0x60, 0x00, 0xfd}
)

// newSelfdestructTransition builds a Transition over a pre-state where the self-destructing
// contract holds `contractBalance`, has code and one storage slot set, and the beneficiary
// holds `beneficiaryBalance`.
func newSelfdestructTransition(
	t *testing.T,
	forks chain.ForksInTime,
	contractBalance, beneficiaryBalance uint64,
) *Transition {
	t.Helper()

	preState := map[types.Address]*PreState{
		contractAddr: {
			Nonce:   1,
			Balance: contractBalance,
			State: map[types.Hash]types.Hash{
				types.ZeroHash: {0x1},
			},
		},
		beneficiaryAddr: {
			Nonce:   0,
			Balance: beneficiaryBalance,
		},
	}

	snap := newStateWithCode(preState, map[types.Address][]byte{contractAddr: contractCode})

	transition := NewTransition(forks, snap, newTxn(snap))

	return transition
}

func TestSelfdestruct_EIP6780(t *testing.T) {
	t.Parallel()

	legacyForks := chain.ForksInTime{EIP150: true, EIP158: true}
	forks6780 := chain.ForksInTime{EIP150: true, EIP158: true, EIP6780: true}

	t.Run("legacy: account is destroyed and refund is granted", func(t *testing.T) {
		t.Parallel()

		tr := newSelfdestructTransition(t, legacyForks, 100, 5)

		tr.Selfdestruct(contractAddr, beneficiaryAddr)

		require.True(t, tr.state.HasSuicided(contractAddr))
		require.Zero(t, tr.state.GetBalance(contractAddr).Uint64())
		require.Equal(t, uint64(105), tr.state.GetBalance(beneficiaryAddr).Uint64())
		require.Equal(t, uint64(24000), tr.state.GetRefund())
	})

	t.Run("legacy: self as beneficiary burns the balance", func(t *testing.T) {
		t.Parallel()

		tr := newSelfdestructTransition(t, legacyForks, 100, 0)

		tr.Selfdestruct(contractAddr, contractAddr)

		require.True(t, tr.state.HasSuicided(contractAddr))
		require.Zero(t, tr.state.GetBalance(contractAddr).Uint64())
	})

	t.Run("eip-6780: pre-existing contract keeps code, storage and nonce", func(t *testing.T) {
		t.Parallel()

		tr := newSelfdestructTransition(t, forks6780, 100, 5)
		require.False(t, tr.state.IsContractCreatedInTx(contractAddr))

		tr.Selfdestruct(contractAddr, beneficiaryAddr)

		// the balance moves to the beneficiary ...
		require.Zero(t, tr.state.GetBalance(contractAddr).Uint64())
		require.Equal(t, uint64(105), tr.state.GetBalance(beneficiaryAddr).Uint64())

		// but nothing is deleted
		require.False(t, tr.state.HasSuicided(contractAddr))
		require.True(t, tr.state.Exist(contractAddr))
		require.Equal(t, contractCode, tr.state.GetCode(contractAddr))
		require.Equal(t, uint64(1), tr.state.GetNonce(contractAddr))
		require.Equal(t, types.Hash{0x1}, tr.state.GetState(contractAddr, types.ZeroHash))

		// no state is freed on this path, so no refund is granted
		require.Zero(t, tr.state.GetRefund())
	})

	t.Run("eip-6780: self as beneficiary does not burn the balance", func(t *testing.T) {
		t.Parallel()

		tr := newSelfdestructTransition(t, forks6780, 100, 0)

		tr.Selfdestruct(contractAddr, contractAddr)

		require.Equal(t, uint64(100), tr.state.GetBalance(contractAddr).Uint64())
		require.False(t, tr.state.HasSuicided(contractAddr))
	})

	t.Run("eip-6780: contract created in the same tx is destroyed", func(t *testing.T) {
		t.Parallel()

		tr := newSelfdestructTransition(t, forks6780, 100, 5)
		tr.state.MarkContractCreated(contractAddr)

		tr.Selfdestruct(contractAddr, beneficiaryAddr)

		require.True(t, tr.state.HasSuicided(contractAddr))
		require.Zero(t, tr.state.GetBalance(contractAddr).Uint64())
		require.Equal(t, uint64(105), tr.state.GetBalance(beneficiaryAddr).Uint64())

		require.Equal(t, uint64(24000), tr.state.GetRefund())
	})

	t.Run("eip-6780: contract created in the same tx burns on self beneficiary", func(t *testing.T) {
		t.Parallel()

		tr := newSelfdestructTransition(t, forks6780, 100, 0)
		tr.state.MarkContractCreated(contractAddr)

		tr.Selfdestruct(contractAddr, contractAddr)

		require.True(t, tr.state.HasSuicided(contractAddr))
		require.Zero(t, tr.state.GetBalance(contractAddr).Uint64())
	})

	t.Run("eip-6780: zero balance is a no-op", func(t *testing.T) {
		t.Parallel()

		tr := newSelfdestructTransition(t, forks6780, 0, 5)

		tr.Selfdestruct(contractAddr, beneficiaryAddr)

		require.Zero(t, tr.state.GetBalance(contractAddr).Uint64())
		require.Equal(t, uint64(5), tr.state.GetBalance(beneficiaryAddr).Uint64())
		require.False(t, tr.state.HasSuicided(contractAddr))
		require.Equal(t, contractCode, tr.state.GetCode(contractAddr))
	})

	t.Run("eip-6780: repeated selfdestruct moves no extra funds", func(t *testing.T) {
		t.Parallel()

		tr := newSelfdestructTransition(t, forks6780, 100, 0)

		tr.Selfdestruct(contractAddr, beneficiaryAddr)
		tr.Selfdestruct(contractAddr, beneficiaryAddr)
		tr.Selfdestruct(contractAddr, beneficiaryAddr)

		require.Equal(t, uint64(100), tr.state.GetBalance(beneficiaryAddr).Uint64())
		require.Zero(t, tr.state.GetBalance(contractAddr).Uint64())

		// the transfer-only path must never grant a refund, otherwise it could be farmed
		// by calling SELFDESTRUCT repeatedly on the same contract
		require.Zero(t, tr.state.GetRefund())
	})
}

func TestCreatedContractMarkers(t *testing.T) {
	t.Parallel()

	t.Run("mark, read and clear", func(t *testing.T) {
		t.Parallel()

		txn := newTestTxn(defaultPreState)

		require.False(t, txn.IsContractCreatedInTx(addr1))

		txn.MarkContractCreated(addr1)

		require.True(t, txn.IsContractCreatedInTx(addr1))
		require.False(t, txn.IsContractCreatedInTx(addr2))

		txn.ClearCreatedContracts()

		require.False(t, txn.IsContractCreatedInTx(addr1))
	})

	t.Run("marker does not disturb ordinary state", func(t *testing.T) {
		t.Parallel()

		txn := newTestTxn(defaultPreState)

		txn.SetState(addr1, slot0, hash1)
		txn.SetBalance(addr1, big.NewInt(42))
		txn.MarkContractCreated(addr1)

		txn.ClearCreatedContracts()

		require.Equal(t, hash1, txn.GetState(addr1, slot0))
		require.Equal(t, uint64(42), txn.GetBalance(addr1).Uint64())
	})

	t.Run("marker is reverted with the snapshot", func(t *testing.T) {
		t.Parallel()

		txn := newTestTxn(defaultPreState)

		snapshot := txn.Snapshot()

		txn.MarkContractCreated(addr1)
		require.True(t, txn.IsContractCreatedInTx(addr1))

		require.NoError(t, txn.RevertToSnapshot(snapshot))

		// a reverted CREATE frame must not leave the address marked as created,
		// otherwise a later SELFDESTRUCT would wrongly delete the account
		require.False(t, txn.IsContractCreatedInTx(addr1))
	})

	t.Run("marker survives a snapshot taken after it", func(t *testing.T) {
		t.Parallel()

		txn := newTestTxn(defaultPreState)

		txn.MarkContractCreated(addr1)

		snapshot := txn.Snapshot()

		txn.SetState(addr1, slot0, hash1)
		require.NoError(t, txn.RevertToSnapshot(snapshot))

		// an inner frame reverting must not clear a marker set by an outer CREATE
		require.True(t, txn.IsContractCreatedInTx(addr1))
	})
}

// TestSelfdestruct_EIP6780_InConstructor drives the marker through the real creation path:
// init code that immediately runs SELFDESTRUCT must still delete the account.
func TestSelfdestruct_EIP6780_InConstructor(t *testing.T) {
	t.Parallel()

	caller := types.StringToAddress("0xcc")

	preState := map[types.Address]*PreState{
		caller:          {Nonce: 0, Balance: 1000},
		beneficiaryAddr: {Nonce: 0, Balance: 0},
	}

	snap := newStateWithPreState(preState, nil)

	tr := NewTransition(
		chain.ForksInTime{Homestead: true, EIP150: true, EIP158: true, EIP6780: true},
		snap,
		newTxn(snap),
	)

	// init code: PUSH20 <beneficiary> ; SELFDESTRUCT
	initCode := append([]byte{0x73}, beneficiaryAddr.Bytes()...)
	initCode = append(initCode, 0xff)

	result := tr.Create2(caller, initCode, big.NewInt(50), 1_000_000)
	require.NoError(t, result.Err)

	created := crypto.CreateAddress(caller, 0)

	require.True(t, tr.state.IsContractCreatedInTx(created))
	require.True(t, tr.state.HasSuicided(created))
	require.Equal(t, uint64(50), tr.state.GetBalance(beneficiaryAddr).Uint64())
}
