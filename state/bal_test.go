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

func getRecord(
	t *testing.T,
	tr *Transition) *bal.BlockAccessListRecord {
	t.Helper()

	rec := tr.BlockAccessListRecorder().GetBlockAccessListRecord()
	require.NotNil(t, rec, "expected a real BAL recorder (is EIP7928 enabled?)")

	return rec
}

func getAccount(
	t *testing.T,
	rec *bal.BlockAccessListRecord,
	addr types.Address) *bal.AccountAccessRecord {
	t.Helper()

	acc, _ := rec.Accounts[addr]

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

func Test_BAL_ZeroValueTransfer(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 1_000_000},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &to,
		Value:    big.NewInt(0),
		Gas:      21_000,
		GasPrice: big.NewInt(1),
		Nonce:    0,
	}

	_, err := tr.apply(msg)
	require.NoError(t, err)

	rec := getRecord(t, tr)

	sender := getAccount(t, rec, from)
	require.Equal(t, uint64(1), sender.NonceChanges[idx])
	require.Equal(t, big.NewInt(979_000), sender.BalanceChanges[idx])

	recipient := getAccount(t, rec, to)
	require.Empty(t, recipient.BalanceChanges,
		"zero-value transfer must not record a recipient balance change")
}

func Test_BAL_Transfer(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 1_000_000},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &to,
		Value:    big.NewInt(1_000),
		Gas:      21_000,
		GasPrice: big.NewInt(1),
		Nonce:    0,
	}

	_, err := tr.apply(msg)
	require.NoError(t, err)

	rec := getRecord(t, tr)

	sender := getAccount(t, rec, from)
	require.Equal(t, uint64(1), sender.NonceChanges[idx])
	require.Equal(t, big.NewInt(978_000), sender.BalanceChanges[idx])

	recipient := getAccount(t, rec, to)
	require.Equal(t, big.NewInt(1_000), recipient.BalanceChanges[idx])
}

func Test_BAL_InsufficientBalanceToCoverIntrinsicGas(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 10_000},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &to,
		Value:    big.NewInt(10_000),
		Gas:      21_000,
		GasPrice: big.NewInt(1),
		Nonce:    0,
	}

	_, err := tr.apply(msg)
	require.Error(t, err)

	rec := getRecord(t, tr)

	sender := getAccount(t, rec, from)
	require.Nil(t, sender)

	recipient := getAccount(t, rec, to)
	require.Nil(t, recipient)
}

func Test_BAL_InsufficientBalanceToCoverTransfer(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 30_000},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &to,
		Value:    big.NewInt(10_000),
		Gas:      21_000,
		GasPrice: big.NewInt(1),
		Nonce:    0,
	}

	_, err := tr.apply(msg)
	require.NoError(t, err)

	rec := getRecord(t, tr)

	sender := getAccount(t, rec, from)
	require.Equal(t, uint64(1), sender.NonceChanges[idx])
	require.Equal(t, big.NewInt(9_000), sender.BalanceChanges[idx])

	recipient := getAccount(t, rec, to)
	require.NotNil(t, recipient)
}

func TestApply_BAL_ContractCreation(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// Init code deploys a contract with runtime code that pushes 42 (0x2A)
	// onto the stack and then stops.
	initCode := []byte{
		0x60, 0x60, // PUSH1 0x60
		0x60, 0x00, // PUSH1 0x00
		0x53, // MSTORE8 -> memory[0] = 0x60

		0x60, 0x2A, // PUSH1 0x2A
		0x60, 0x01, // PUSH1 0x01
		0x53, // MSTORE8 -> memory[1] = 0x2A

		0x60, 0x00, // PUSH1 0x00
		0x60, 0x02, // PUSH1 0x02
		0x53, // MSTORE8 -> memory[2] = 0x00

		0x60, 0x03, // PUSH1 3
		0x60, 0x00, // PUSH1 0
		0xF3, // RETURN
	}

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 1_000_000},
	}

	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       nil,
		Value:    big.NewInt(0),
		Input:    initCode,
		Gas:      1_000_000,
		GasPrice: big.NewInt(0),
		Nonce:    0,
	}

	res, err := tr.apply(msg)
	require.NoError(t, err)
	require.False(t, res.Failed(), "deployment should succeed: %v", res.Err)

	deployed := crypto.CreateAddress(from, 0)
	rec := getRecord(t, tr)

	caller := getAccount(t, rec, from)
	require.Equal(t, uint64(1), caller.NonceChanges[idx])
	require.Equal(t, big.NewInt(1_000_000), caller.BalanceChanges[idx])

	created := getAccount(t, rec, deployed)
	require.Equal(t, uint64(1), created.NonceChanges[idx])
	require.Equal(t, []byte{0x60, 0x2A, 0x00}, created.CodeChanges[idx])
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

	rec := getRecord(t, tr)
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

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

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

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

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

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

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

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

	slot0 := types.Hash{}
	require.Contains(t, acc.StorageWrites, slot0,
		"documents current merge-on-revert behavior; revisit against EIP-7928")
}
