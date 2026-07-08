package state

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/state/runtime/evm"
	"github.com/0xPolygon/polygon-edge/state/runtime/precompiled"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/types/bal"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

var (
	from         = types.StringToAddress("aa")
	to           = types.StringToAddress("bb")
	coinbaseAddr = types.StringToAddress("cc")
	contractAddr = types.StringToAddress("dd")
)

func balTestConfig(eip7928 bool) chain.ForksInTime {
	return chain.ForksInTime{
		Homestead: true,
		Istanbul:  true,
		EIP155:    true,
		EIP158:    true,
		EIP7928:   eip7928,
	}
}

func newBALTestTransition(
	t *testing.T,
	config chain.ForksInTime,
	preState map[types.Address]*PreState,
	balIndex uint,
) *Transition {
	t.Helper()

	if preState == nil {
		preState = map[types.Address]*PreState{}
	}

	snap := newStateWithPreState(preState)

	return &Transition{
		logger:      hclog.NewNullLogger(),
		state:       newTxn(snap),
		snap:        snap,
		config:      config,
		ctx:         runtime.TxContext{BaseFee: big.NewInt(0), Coinbase: coinbaseAddr},
		gasPool:     10_000_000,
		evm:         evm.NewEVM(),
		precompiles: precompiled.NewPrecompiled(),
		BalIndex:    balIndex,
	}
}

func record(
	t *testing.T,
	tr *Transition) *bal.BlockAccessListRecord {
	t.Helper()

	rec := tr.BlockAccessListRecorder().GetBlockAccessListRecord()
	require.NotNil(t, rec, "expected a real BAL recorder (is EIP7928 enabled?)")

	return rec
}

func account(
	t *testing.T,
	rec *bal.BlockAccessListRecord,
	addr types.Address) *bal.AccountAccessRecord {
	t.Helper()

	acc, ok := rec.Accounts[addr]
	require.Truef(t, ok, "address %s expected in BAL but was not recorded", addr)

	return acc
}

func Test_BAL_Disabled(t *testing.T) {
	t.Parallel()

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 1_000_000},
	}

	tr := newBALTestTransition(t, balTestConfig(false), pre, 1)

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &to,
		Value:    big.NewInt(100),
		Gas:      21_000,
		GasPrice: big.NewInt(1),
		Nonce:    0,
	}

	_, err := tr.apply(msg)
	require.NoError(t, err)

	fmt.Println(pre[from].Balance, pre[from].Nonce)

	require.Nil(t, tr.BlockAccessListRecorder().GetBlockAccessListRecord())
}

func TestApply_BAL_ZeroValueTransfer(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	pre := map[types.Address]*PreState{
		addr1: {Nonce: 0, Balance: 1_000_000},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     addr1,
		To:       &addr2,
		Value:    big.NewInt(0),
		Gas:      21_000,
		GasPrice: big.NewInt(1),
		Nonce:    0,
	}

	_, err := tr.apply(msg)
	require.NoError(t, err)

	rec := record(t, tr)

	sender := account(t, rec, addr1)
	require.Equal(t, uint64(1), sender.NonceChanges[idx])
	require.Contains(t, sender.BalanceChanges, idx)

	recipient := account(t, rec, addr2)
	require.Empty(t, recipient.BalanceChanges,
		"zero-value transfer must not record a recipient balance change")
}

func TestApply_BAL_ContractCreation(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	initCode := []byte{0x60, 0x00, 0x60, 0x00, 0x53, 0x60, 0x01, 0x60, 0x00, 0xF3}

	pre := map[types.Address]*PreState{
		addr1: {Nonce: 0, Balance: 10_000_000},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     addr1,
		To:       nil, // contract creation
		Value:    big.NewInt(0),
		Input:    initCode,
		Gas:      1_000_000,
		GasPrice: big.NewInt(0),
		Nonce:    0,
	}

	res, err := tr.apply(msg)
	require.NoError(t, err)
	require.False(t, res.Failed(), "deployment should succeed: %v", res.Err)

	deployed := crypto.CreateAddress(addr1, 0)
	rec := record(t, tr)

	// C1: caller nonce bumped to 1.
	caller := account(t, rec, addr1)
	require.Equal(t, uint64(1), caller.NonceChanges[idx])

	created := account(t, rec, deployed)
	require.Equal(t, uint64(1), created.NonceChanges[idx])
	require.Equal(t, []byte{0x00}, created.CodeChanges[idx])
}

func TestApply_BAL_ContractCreation_Collision(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	deployed := crypto.CreateAddress(addr1, 0)

	pre := map[types.Address]*PreState{
		addr1:    {Nonce: 0, Balance: 10_000_000},
		deployed: {Nonce: 1}, // makes hasCodeOrNonce == true
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     addr1,
		To:       nil,
		Value:    big.NewInt(0),
		Input:    []byte{0x00},
		Gas:      1_000_000,
		GasPrice: big.NewInt(0),
		Nonce:    0,
	}

	res, err := tr.apply(msg)
	require.NoError(t, err)
	require.True(t, res.Failed())
	require.ErrorIs(t, res.Err, runtime.ErrContractAddressCollision)

	rec := record(t, tr)
	require.Contains(t, rec.Accounts, deployed)
}

func seedContract(t *testing.T, code []byte, idx uint) *Transition {
	t.Helper()

	pre := map[types.Address]*PreState{
		addr1: {Nonce: 0, Balance: 10_000_000},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, idx)
	// install the runtime code directly (bypasses the recorder: setup only)
	tr.state.SetCode(contractAddr, code)

	return tr
}

func callContract(idx uint32) *types.Transaction {
	return &types.Transaction{
		Type:     types.LegacyTx,
		From:     addr1,
		To:       &contractAddr,
		Value:    big.NewInt(0),
		Gas:      1_000_000,
		GasPrice: big.NewInt(0),
		Nonce:    0,
	}
}

func TestApply_BAL_SStore(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}

	tr := seedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := record(t, tr)
	acc := account(t, rec, contractAddr)

	slot0 := types.Hash{} // key 0x00..00
	writes, ok := acc.StorageWrites[slot0]
	require.True(t, ok, "expected a write to slot 0")

	val, ok := writes[idx]
	require.True(t, ok, "write must be keyed by the tx index")
	require.Equal(t, types.BytesToHash(big.NewInt(1).Bytes()), val)

	require.NotContains(t, acc.StorageReads, slot0)
}

func TestApply_BAL_SLoad(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{0x60, 0x00, 0x54, 0x50, 0x00}

	tr := seedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := record(t, tr)
	acc := account(t, rec, contractAddr)

	slot0 := types.Hash{}
	require.Contains(t, acc.StorageReads, slot0)
	require.NotContains(t, acc.StorageWrites, slot0)
}

func TestApply_BAL_SLoadThenSStore(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{
		0x60, 0x00, 0x54, 0x50, // SLOAD slot 0, drop
		0x60, 0x01, 0x60, 0x00, 0x55, // SSTORE slot 0 = 1
		0x00,
	}

	tr := seedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := record(t, tr)
	acc := account(t, rec, contractAddr)

	slot0 := types.Hash{}
	require.Contains(t, acc.StorageWrites, slot0)
	require.NotContains(t, acc.StorageReads, slot0,
		"a written slot must not remain in StorageReads")
}

func TestApply_BAL_RevertedWrite_CurrentlyStillMerged(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{
		0x60, 0x01, 0x60, 0x00, 0x55, // SSTORE slot 0 = 1
		0x60, 0x00, 0x60, 0x00, 0xFD, // REVERT
	}

	tr := seedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.True(t, res.Failed(), "call is expected to revert")

	rec := record(t, tr)
	acc := account(t, rec, contractAddr)

	slot0 := types.Hash{}
	require.Contains(t, acc.StorageWrites, slot0,
		"documents current merge-on-revert behavior; revisit against EIP-7928")
}
