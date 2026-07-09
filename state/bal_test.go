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

	// Init code deploys a contract with runtime code that pushes 42 (0x2A)
	// onto the stack and then stops.
	initCode = []byte{
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

func Test_BAL_ContractCreation(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

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

func seedContractState(
	t *testing.T,
	code []byte,
	balance uint64,
	storage map[types.Hash]types.Hash,
	idx uint,
) *Transition {
	t.Helper()

	pre := map[types.Address]*PreState{
		addr1:        {Nonce: 0, Balance: 10_000_000},
		contractAddr: {Balance: balance, State: storage},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, idx)
	tr.state.SetCode(contractAddr, code)

	return tr
}

// push20 builds `PUSH20 <addr>` for use inside test bytecode.
func push20(addr types.Address) []byte {
	return append([]byte{0x73}, addr.Bytes()...)
}

// The coinbase must get a BalanceChange equal to gasUsed*effectiveTip whenever a
// non-zero fee is collected.
func TestApply_BAL_CoinbaseFee(t *testing.T) {
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
		GasPrice: big.NewInt(3),
		Nonce:    0,
	}

	res, err := tr.apply(msg)
	require.NoError(t, err)

	rec := getRecord(t, tr)
	coinbase := getAccount(t, rec, coinbaseAddr)

	wantFee := new(big.Int).Mul(big.NewInt(int64(res.GasUsed)), big.NewInt(3))
	require.Equalf(t, 0, coinbase.BalanceChanges[idx].Cmp(wantFee),
		"coinbase fee: want %s got %s", wantFee, coinbase.BalanceChanges[idx])
}

// Under London the burn contract receives a BalanceChange entry (even with a
// zero base fee it is still recorded because apply records it unconditionally on
// the London path).
func TestApply_BAL_BurnContract_London(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	burnAddr := types.StringToAddress("b02n")

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 1_000_000},
	}

	cfg := balTestConfig(true)
	cfg.London = true

	tr := newBALTestTransition(t, cfg, pre, uint(idx))
	tr.ctx.BurnContract = burnAddr

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
	burn := getAccount(t, rec, burnAddr)
	require.Contains(t, burn.BalanceChanges, idx,
		"London path must record a burn-contract balance change")
}

// A self-destruct that moves a non-zero balance records BalanceChange(addr->0)
// and BalanceChange(beneficiary->new).
func TestApply_BAL_Selfdestruct_WithBalance(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// PUSH20 <to>  SELFDESTRUCT
	code := append(push20(to), 0xFF)

	tr := seedContractState(t, code, 500, nil, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	self := getAccount(t, rec, contractAddr)
	require.Equalf(t, 0, self.BalanceChanges[idx].Cmp(big.NewInt(0)),
		"self-destructed account balance must be recorded as 0")

	beneficiary := getAccount(t, rec, to)
	require.Equalf(t, 0, beneficiary.BalanceChanges[idx].Cmp(big.NewInt(500)),
		"beneficiary must be credited the destructed balance")
}

// A self-destruct of a zero-balance account records only AccountReads (no
// balance changes) for both the account and the beneficiary.
func TestApply_BAL_Selfdestruct_ZeroBalance(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := append(push20(to), 0xFF)

	// seedContract gives the contract no balance.
	tr := seedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	self := getAccount(t, rec, contractAddr)
	require.Empty(t, self.BalanceChanges,
		"zero-balance self-destruct must not record a balance change")

	beneficiary := getAccount(t, rec, to)
	require.Empty(t, beneficiary.BalanceChanges)
}

// BALANCE(to) must touch to in the BAL as a bare account read.
func TestApply_BAL_BalanceOpcode_AccountRead(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// PUSH20 <to>  BALANCE  POP  STOP
	code := append(push20(to), 0x31, 0x50, 0x00)

	tr := seedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, to) // presence proves the read
	require.Empty(t, acc.StorageWrites)
	require.Empty(t, acc.BalanceChanges)
}

// EXTCODESIZE(to) must touch to in the BAL as a bare account read.
func TestApply_BAL_ExtCodeSize_AccountRead(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// PUSH20 <to>  EXTCODESIZE  POP  STOP
	code := append(push20(to), 0x3B, 0x50, 0x00)

	tr := seedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	require.Contains(t, rec.Accounts, to)
}

// Writing a slot with the value it already holds yields StorageUnchanged, which
// must be recorded as a read (not a write).
func TestApply_BAL_SStore_SameValue_RecordsRead(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	slot0 := types.Hash{}
	one := types.BytesToHash(big.NewInt(1).Bytes())

	// runtime: PUSH1 0x01 PUSH1 0x00 SSTORE STOP  (writes 1 to slot 0)
	code := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}

	// pre-existing storage already holds 1 at slot 0
	tr := seedContractState(t, code, 0, map[types.Hash]types.Hash{slot0: one}, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

	require.Contains(t, acc.StorageReads, slot0,
		"an unchanged SSTORE must be recorded as a read")
	require.NotContains(t, acc.StorageWrites, slot0)
}

// Two writes to the same slot within one tx collapse to the last value at the
// tx index.
func TestApply_BAL_SStore_MultipleWrites_LastWins(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// PUSH1 1 PUSH1 0 SSTORE  PUSH1 2 PUSH1 0 SSTORE  STOP
	code := []byte{
		0x60, 0x01, 0x60, 0x00, 0x55,
		0x60, 0x02, 0x60, 0x00, 0x55,
		0x00,
	}

	tr := seedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

	slot0 := types.Hash{}
	require.Len(t, acc.StorageWrites[slot0], 1, "only one entry per tx index")
	require.Equal(t, types.BytesToHash(big.NewInt(2).Bytes()), acc.StorageWrites[slot0][idx])
}

// All recorded changes must be keyed by the transaction's BalIndex, whatever it
// is (here 7), and nothing must land on any other index.
func TestApply_BAL_IndexPropagation(t *testing.T) {
	t.Parallel()

	const idx uint32 = 7

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 1_000_000},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &to,
		Value:    big.NewInt(10),
		Gas:      21_000,
		GasPrice: big.NewInt(1),
		Nonce:    0,
	}

	_, err := tr.apply(msg)
	require.NoError(t, err)

	rec := getRecord(t, tr)
	sender := getAccount(t, rec, from)

	require.Contains(t, sender.NonceChanges, idx)
	require.Contains(t, sender.BalanceChanges, idx)
	require.Len(t, sender.NonceChanges, 1)
	require.Len(t, sender.BalanceChanges, 1)

	coinbase := getAccount(t, rec, coinbaseAddr)
	require.Contains(t, coinbase.BalanceChanges, idx)
}
