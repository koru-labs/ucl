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
		Homestead:      true,
		Istanbul:       true,
		Byzantium:      true,
		EIP150:         true,
		EIP155:         true,
		EIP158:         true,
		EIP7928:        eip7928,
		Constantinople: true,
	}
}

func newBALTransition(t *testing.T, config chain.ForksInTime, snap Snapshot, balIndex uint) *Transition {
	t.Helper()
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

func newBALTestTransition(t *testing.T,
	config chain.ForksInTime,
	preState map[types.Address]*PreState,
	codes map[types.Hash][]byte,
	balIndex uint) *Transition {
	t.Helper()
	if preState == nil {
		preState = map[types.Address]*PreState{}
	}

	if codes == nil {
		codes = map[types.Hash][]byte{}
	}

	return newBALTransition(t, config, newStateWithPreState(preState, codes), balIndex)
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

	tr := newBALTestTransition(t, balTestConfig(false), pre, nil, 1)

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
	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, uint(idx))

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
	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, uint(idx))

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
	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, uint(idx))

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
	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, uint(idx))

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

func Test_BAL_ContractCreation(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	pre := map[types.Address]*PreState{
		from: {Nonce: 0, Balance: 1_000_000},
	}

	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       nil,
		Value:    big.NewInt(1_000),
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
	require.Equal(t, big.NewInt(999_000), caller.BalanceChanges[idx])

	created := getAccount(t, rec, deployed)
	require.Equal(t, uint64(1), created.NonceChanges[idx])
	require.Equal(t, big.NewInt(1_000), created.BalanceChanges[idx])
	require.Equal(t, []byte{0x60, 0x2A, 0x00}, created.CodeChanges[idx])
}

func Test_BAL_ContractCreation_Collision(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	deployed := crypto.CreateAddress(from, 0)

	pre := map[types.Address]*PreState{
		from:     {Nonce: 0, Balance: 1_000_000},
		deployed: {Nonce: 1},
	}
	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, uint(idx))

	msg := &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
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

func newBALTestTranstionWithSeedContract(
	t *testing.T,
	code []byte,
	idx uint) *Transition {
	t.Helper()

	pre := map[types.Address]*PreState{
		from:         {Nonce: 0, Balance: 10_000_000},
		contractAddr: {CodeHash: []byte("code")},
	}

	codes := map[types.Hash][]byte{
		types.BytesToHash([]byte("code")): code,
	}

	tr := newBALTestTransition(t, balTestConfig(true), pre, codes, idx)

	return tr
}

func callContract(idx uint32, value int64) *types.Transaction {
	return &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &contractAddr,
		Value:    big.NewInt(value),
		Gas:      1_000_000,
		GasPrice: big.NewInt(0),
		Nonce:    0,
	}
}

func Test_BAL_SStore(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{
		0x61, 0x03, 0xE8, // PUSH2 0x03E8 -> value 1000
		0x61, 0x03, 0xE8, // PUSH2 0x03E8 -> slot 1000
		0x55, // SSTORE -> storage[1000] = 1000
		0x00, // STOP
	}

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 1_000))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)
	caller := getAccount(t, rec, from)
	require.Equal(t, uint64(1), caller.NonceChanges[idx])
	require.Equal(t, big.NewInt(9_999_000), caller.BalanceChanges[idx])

	slot := types.BytesToHash([]byte{0x03, 0xE8})
	writes, ok := acc.StorageWrites[slot]
	require.True(t, ok, "expected a write to slot 1000")

	val, ok := writes[idx]
	require.True(t, ok, "write must be keyed by the tx index")
	require.Equal(t, types.BytesToHash(big.NewInt(1000).Bytes()), val)

	require.NotContains(t, acc.StorageReads, slot)
	require.Equal(t, big.NewInt(1_000), acc.BalanceChanges[idx])
}

func Test_BAL_SLoad(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{
		0x61, 0x03, 0xE8, // PUSH2 0x03E8 -> value 1000
		0x54, // SLOAD -> load storage[1000], push onto stack
		0x50, // POP -> remove value from stack
		0x00, // STOP
	}

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 1_000))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)
	caller := getAccount(t, rec, from)
	require.Equal(t, uint64(1), caller.NonceChanges[idx])
	require.Equal(t, big.NewInt(9_999_000), caller.BalanceChanges[idx])

	slot := types.BytesToHash([]byte{0x03, 0xE8})
	require.Contains(t, acc.StorageReads, slot)
	require.NotContains(t, acc.StorageWrites, slot)
	require.Equal(t, big.NewInt(1_000), acc.BalanceChanges[idx])
}

func Test_BAL_MultipleStorageReadAndWrite(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{
		// Read from slot 3 (empty, returns 0)
		0x60, 0x03, // PUSH1 0x03
		0x54, // SLOAD -> load storage[3], push onto stack
		0x50, // POP   -> discard

		// Write 0xAB (171) to slot 3
		0x60, 0xAB, // PUSH1 0xAB
		0x60, 0x03, // PUSH1 0x03
		0x55, // SSTORE -> storage[3] = 171

		// Write 0xFF (255) to slot 3 (overwrite)
		0x60, 0xFF, // PUSH1 0xFF
		0x60, 0x03, // PUSH1 0x03
		0x55, // SSTORE -> storage[3] = 255

		// Write 0x42 (66) to slot 7
		0x60, 0x42, // PUSH1 0x42
		0x60, 0x07, // PUSH1 0x07
		0x55, // SSTORE -> storage[7] = 66

		// Read from slot 3 (should return 255)
		0x60, 0x03, // PUSH1 0x03
		0x54, // SLOAD -> load storage[3], push onto stack
		0x50, // POP   -> discard

		// Read from slot 99 (random, returns 0)
		0x60, 0x63, // PUSH1 0x63
		0x54, // SLOAD -> load storage[99], push onto stack
		0x50, // POP   -> discard

		0x00, // STOP
	}

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 1_000))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)
	caller := getAccount(t, rec, from)
	require.Equal(t, uint64(1), caller.NonceChanges[idx])
	require.Equal(t, big.NewInt(9_999_000), caller.BalanceChanges[idx])

	slot3 := types.BytesToHash([]byte{0x03})
	slot7 := types.BytesToHash([]byte{0x07})
	slot99 := types.BytesToHash([]byte{0x63})
	require.Contains(t, acc.StorageWrites, slot3)
	require.Contains(t, acc.StorageWrites, slot7)
	require.NotContains(t, acc.StorageReads, slot3,
		"a written slot must not remain in StorageReads")
	require.Contains(t, acc.StorageReads, slot99)
	require.NotContains(t, acc.StorageWrites, slot99,
		"a written slot must not remain in StorageReads")

	val, ok := acc.StorageWrites[slot3][idx]
	require.True(t, ok, "write must be keyed by the tx index")
	require.Equal(t, types.BytesToHash(big.NewInt(255).Bytes()), val)

	require.Equal(t, big.NewInt(1_000), acc.BalanceChanges[idx])
}

func Test_BAL_SStoreAndRevert(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{
		0x60, 0xAB, // PUSH1 0xAB
		0x60, 0x03, // PUSH1 0x03
		0x55,       // SSTORE -> storage[3] = 171
		0x60, 0x00, // PUSH1 0x00
		0x60, 0x00, // PUSH1 0x00
		0xFD, // REVERT -> revert(0, 0)
	}
	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 1_000))
	require.NoError(t, err)
	require.True(t, res.Failed(), "call should fail: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)
	caller := getAccount(t, rec, from)
	require.Equal(t, uint64(1), caller.NonceChanges[idx])
	require.Equal(t, big.NewInt(10_000_000), caller.BalanceChanges[idx])

	slot := types.BytesToHash([]byte{0x03})

	require.NotContains(t, acc.StorageWrites, slot)
	require.NotContains(t, acc.StorageReads, slot)
	require.NotContains(t, acc.BalanceChanges, idx)
}

func Test_BAL_SLoadAndRevert(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{
		0x60, 0x03, // PUSH1 0x03
		0x54,       // SLOAD -> load storage[3], push onto stack
		0x50,       // POP   -> discard
		0x60, 0x00, // PUSH1 0x00
		0x60, 0x00, // PUSH1 0x00
		0xFD, // REVERT -> revert(0, 0)
	}
	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 1_000))
	require.NoError(t, err)
	require.True(t, res.Failed(), "call should fail: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)
	caller := getAccount(t, rec, from)
	require.Equal(t, uint64(1), caller.NonceChanges[idx])
	require.Equal(t, big.NewInt(10_000_000), caller.BalanceChanges[idx])

	slot := types.BytesToHash([]byte{0x03})
	require.NotContains(t, acc.StorageWrites, slot)
	require.NotContains(t, acc.StorageReads, slot)
	require.NotContains(t, acc.BalanceChanges, idx)
}

func TestApply_BAL_RevertedWrite_CurrentlyStillMerged(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	code := []byte{
		0x60, 0x01, 0x60, 0x00, 0x55, // SSTORE slot 0 = 1
		0x60, 0x00, 0x60, 0x00, 0xFD, // REVERT
	}

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
	require.NoError(t, err)
	require.True(t, res.Failed(), "call is expected to revert")

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

	slot0 := types.Hash{}
	require.NotContains(t, acc.StorageWrites, slot0,
		"a reverted write must NOT be merged into the BAL")
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
	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, idx)
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
	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, uint(idx))

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

	tr := newBALTestTransition(t, cfg, pre, nil, uint(idx))
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

	res, err := tr.apply(callContract(idx, 0))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	for addr, acc := range rec.Accounts { // ili kako god iterira
		t.Logf("addr=%s balChanges=%v nonceChanges=%v", addr, acc.BalanceChanges, acc.NonceChanges)
	}

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
	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
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

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
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

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, to)

	require.Empty(t, acc.StorageReads)
	require.Empty(t, acc.StorageWrites)
	require.Empty(t, acc.BalanceChanges)
	require.Empty(t, acc.NonceChanges)
	require.Empty(t, acc.CodeChanges)
}

// EXTCODEHASH(to) must touch to in the BAL as a bare account read.
func TestApply_BAL_ExtCodeHash_AccountRead(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// PUSH20 <to>
	// EXTCODEHASH
	// POP
	// STOP
	code := append(push20(to), 0x3F, 0x50, 0x00)

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	acc := getAccount(t, rec, to)
	require.NotNil(t, acc)

	require.Empty(t, acc.StorageReads)
	require.Empty(t, acc.StorageWrites)
	require.Empty(t, acc.BalanceChanges)
	require.Empty(t, acc.NonceChanges)
	require.Empty(t, acc.CodeChanges)
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

	res, err := tr.apply(callContract(idx, 0))
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

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
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
	tr := newBALTestTransition(t, balTestConfig(true), pre, nil, uint(idx))

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

func callerCallingCallee() []byte {
	code := []byte{
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x00, // inSize
		0x60, 0x00, // inOffset
		0x60, 0x00, // value
	}
	code = append(code, push20(from)...)  // addr
	code = append(code, 0x5A, 0xF1, 0x00) // GAS, CALL, STOP

	return code
}

// seedCallerCallee wires caller code onto contractAddr and callee code onto
// calleeAddr; `from` is the funded EOA.
func seedCallerCallee(t *testing.T, callerCode, calleeCode []byte, calleeBalance uint64, idx uint) *Transition {
	t.Helper()

	snap := newStateWithCode(
		map[types.Address]*PreState{
			from:         {Nonce: 0, Balance: 10_000_000},
			contractAddr: {},
			from:         {Balance: calleeBalance},
		},
		map[types.Address][]byte{
			contractAddr: callerCode,
			from:         calleeCode,
		},
	)

	return newBALTransition(t, balTestConfig(true), snap, idx)
}

// callCaller is a tx from `from` to the caller contract.
func callCaller() *types.Transaction {
	return &types.Transaction{
		Type:     types.LegacyTx,
		From:     from,
		To:       &contractAddr,
		Value:    big.NewInt(0),
		Gas:      10_000_000,
		GasPrice: big.NewInt(0),
		Nonce:    0,
	}
}

// A successful nested CALL: the callee's storage write bubbles up into the BAL.
func TestApply_BAL_NestedCall_SuccessMerges(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// callee: SSTORE slot0 = 1, STOP
	callee := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}

	tr := seedCallerCallee(t, callerCallingCallee(), callee, 0, uint(idx))

	res, err := tr.apply(callCaller())
	require.NoError(t, err)
	require.False(t, res.Failed(), "top-level call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	acc := getAccount(t, rec, from)
	slot0 := types.Hash{}
	writes, ok := acc.StorageWrites[slot0]
	require.True(t, ok, "callee write must be merged into the BAL")
	require.Equal(t, types.BytesToHash(big.NewInt(1).Bytes()), writes[idx])

	require.Contains(t, rec.Accounts, contractAddr) // caller touched
	require.Contains(t, rec.Accounts, from)         // callee touched
}

// A reverted nested CALL: the callee SSTOREs then REVERTs. The caller swallows
// the failed CALL so the tx succeeds, but the reverted write must NOT reach the
// BAL (applyCall merges the sub-recorder only on success). EIP-7928 correct.
func TestApply_BAL_NestedCall_RevertedSubcall_NotMerged(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// callee: SSTORE slot0 = 1, then REVERT
	callee := []byte{
		0x60, 0x01, 0x60, 0x00, 0x55,
		0x60, 0x00, 0x60, 0x00, 0xFD,
	}

	tr := seedCallerCallee(t, callerCallingCallee(), callee, 0, uint(idx))

	res, err := tr.apply(callCaller())
	require.NoError(t, err)
	require.False(t, res.Failed(), "caller swallows the failed CALL, so the tx succeeds")

	rec := getRecord(t, tr)

	slot0 := types.Hash{}
	if acc := getAccount(t, rec, from); acc != nil {
		require.NotContains(t, acc.StorageWrites, slot0,
			"a reverted nested write must NOT be merged into the BAL")
	}
}

// SELFDESTRUCT in a nested CALL: the callee's zeroing and the beneficiary
// credit are merged up into the BAL.
func TestApply_BAL_NestedCall_Selfdestruct(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// callee: PUSH20 <to> SELFDESTRUCT
	callee := append(push20(to), 0xFF)

	tr := seedCallerCallee(t, callerCallingCallee(), callee, 500, uint(idx))

	res, err := tr.apply(callCaller())
	require.NoError(t, err)
	require.False(t, res.Failed(), "top-level call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	self := getAccount(t, rec, from)
	require.Equalf(t, 0, self.BalanceChanges[idx].Cmp(big.NewInt(0)),
		"self-destructed callee balance must be recorded as 0")

	beneficiary := getAccount(t, rec, to)
	require.Equalf(t, 0, beneficiary.BalanceChanges[idx].Cmp(big.NewInt(500)),
		"beneficiary must be credited the destructed balance")
}

// EXTCODECOPY(to) must touch to in the BAL as a bare account read.
func TestApply_BAL_ExtCodeCopy_AccountRead(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// EXTCODECOPY pops in this order: addr (top), destOff, codeOff, size.
	// Push them so `addr` ends up on top.
	code := []byte{
		0x60, 0x00, // size
		0x60, 0x00, // codeOffset
		0x60, 0x00, // destOffset
	}
	code = append(code, push20(to)...) // addr on top
	code = append(code, 0x3C, 0x00)    // EXTCODECOPY, STOP

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	acc := getAccount(t, rec, to)
	require.NotNil(t, acc)
	require.Empty(t, acc.StorageReads)
	require.Empty(t, acc.StorageWrites)
	require.Empty(t, acc.BalanceChanges)
	require.Empty(t, acc.NonceChanges)
	require.Empty(t, acc.CodeChanges)
}

// SLOAD of the same slot twice records exactly one read entry (a set, not a
// list).
func TestApply_BAL_SLoad_Idempotent(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// PUSH1 0 SLOAD POP  PUSH1 0 SLOAD POP  STOP
	code := []byte{
		0x60, 0x00, 0x54, 0x50,
		0x60, 0x00, 0x54, 0x50,
		0x00,
	}

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

	slot0 := types.Hash{}
	require.Contains(t, acc.StorageReads, slot0)
	require.Len(t, acc.StorageReads, 1, "repeated SLOAD of the same slot is a set membership, not a list append")
}

// BALANCE(<to>) followed by SSTORE on the caller's own storage: `to` must be
// touched as a bare account read, and the caller's write must land cleanly on
// its own slot -- no cross-contamination between the two records.
func TestApply_BAL_AccountRead_DoesNotBleedIntoWrites(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// PUSH20 <to> BALANCE POP  PUSH1 1 PUSH1 0 SSTORE  STOP
	code := append(push20(to), 0x31, 0x50)
	code = append(code, 0x60, 0x01, 0x60, 0x00, 0x55, 0x00)

	tr := newBALTestTranstionWithSeedContract(t, code, uint(idx))

	res, err := tr.apply(callContract(idx, 0))
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	target := getAccount(t, rec, to)
	require.NotNil(t, target)
	require.Empty(t, target.StorageWrites, "BALANCE target must not accumulate writes")

	caller := getAccount(t, rec, contractAddr)
	slot0 := types.Hash{}
	require.Contains(t, caller.StorageWrites, slot0)
}

// CALL with a non-zero value records BalanceChange for BOTH caller and callee
// in the sub-recorder, and gets merged up into the block BAL on success.
func TestApply_BAL_Call_WithValue_RecordsBothBalances(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// caller runtime: CALL(gas=GAS, calleeAddr, value=1, in=0/0, ret=0/0), STOP
	caller := []byte{
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x00, // inSize
		0x60, 0x00, // inOffset
		0x60, 0x01, // value = 1
	}
	caller = append(caller, push20(from)...)
	caller = append(caller, 0x5A, 0xF1, 0x00) // GAS, CALL, STOP

	// callee: STOP (empty body still receives value)
	callee := []byte{0x00}

	// give the caller a starting balance so it can transfer 1 wei
	snap := newStateWithCode(
		map[types.Address]*PreState{
			from:         {Nonce: 0, Balance: 10_000_000},
			contractAddr: {Balance: 100},
			from:         {},
		},
		map[types.Address][]byte{
			contractAddr: caller,
			from:         callee,
		},
	)

	tr := newBALTransition(t, balTestConfig(true), snap, uint(idx))

	res, err := tr.apply(callCaller())
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	callerAcc := getAccount(t, rec, contractAddr)
	require.Contains(t, callerAcc.BalanceChanges, idx,
		"caller must have a BalanceChange recorded for the value transfer")

	calleeAcc := getAccount(t, rec, from)
	require.Contains(t, calleeAcc.BalanceChanges, idx,
		"callee must have a BalanceChange recorded for the value received")
}

// DELEGATECALL executes callee code under the CALLER's storage. So an SSTORE
// inside callee code must be recorded under the CALLER's account, not the
// callee's.
func TestApply_BAL_DelegateCall_StorageBelongsToCaller(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// caller: DELEGATECALL(gas=GAS, calleeAddr, in=0/0, ret=0/0)  STOP
	// DELEGATECALL pops: gas, addr, inOff, inSize, retOff, retSize  (no value)
	caller := []byte{
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x00, // inSize
		0x60, 0x00, // inOffset
	}
	caller = append(caller, push20(from)...)
	caller = append(caller, 0x5A, 0xF4, 0x00) // GAS, DELEGATECALL, STOP

	// callee: SSTORE slot0 = 1, STOP  -- under DELEGATECALL, this writes to
	// the CALLER's storage.
	callee := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}

	snap := newStateWithCode(
		map[types.Address]*PreState{
			from:         {Nonce: 0, Balance: 10_000_000},
			contractAddr: {},
			from:         {},
		},
		map[types.Address][]byte{
			contractAddr: caller,
			from:         callee,
		},
	)

	tr := newBALTransition(t, balTestConfig(true), snap, uint(idx))

	res, err := tr.apply(callCaller())
	require.NoError(t, err)
	require.False(t, res.Failed(), "call should succeed: %v", res.Err)

	rec := getRecord(t, tr)

	slot0 := types.Hash{}

	callerAcc := getAccount(t, rec, contractAddr)
	require.Contains(t, callerAcc.StorageWrites, slot0,
		"DELEGATECALL: storage write must be recorded under the caller's address")

	if calleeAcc := getAccount(t, rec, from); calleeAcc != nil {
		require.NotContains(t, calleeAcc.StorageWrites, slot0,
			"DELEGATECALL: callee's storage map must NOT receive the write")
	}
}

// STATICCALL forbids state modification. An SSTORE inside a static frame
// triggers errWriteProtection BEFORE the recorder is called, so no write ends
// up in the BAL.
func TestApply_BAL_StaticCall_WriteProtection_NoRecording(t *testing.T) {
	t.Parallel()

	const idx uint32 = 1

	// caller: STATICCALL(gas=GAS, calleeAddr, in=0/0, ret=0/0)  STOP
	// STATICCALL pops: gas, addr, inOff, inSize, retOff, retSize
	caller := []byte{
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x00, // inSize
		0x60, 0x00, // inOffset
	}
	caller = append(caller, push20(from)...)
	caller = append(caller, 0x5A, 0xFA, 0x00) // GAS, STATICCALL, STOP

	// callee attempts SSTORE, which must be write-protected.
	callee := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}

	snap := newStateWithCode(
		map[types.Address]*PreState{
			from:         {Nonce: 0, Balance: 10_000_000},
			contractAddr: {},
			from:         {},
		},
		map[types.Address][]byte{
			contractAddr: caller,
			from:         callee,
		},
	)

	cfg := balTestConfig(true)
	cfg.Byzantium = true // STATICCALL requires Byzantium; opCall gate-checks it

	tr := newBALTransition(t, cfg, snap, uint(idx))

	res, err := tr.apply(callCaller())
	require.NoError(t, err)
	require.False(t, res.Failed(),
		"the outer tx succeeds; only the inner STATICCALL fails with write-protection")

	rec := getRecord(t, tr)

	slot0 := types.Hash{}
	if calleeAcc := getAccount(t, rec, from); calleeAcc != nil {
		require.NotContains(t, calleeAcc.StorageWrites, slot0,
			"STATICCALL must reject SSTORE; no write may reach the BAL")
	}
	if callerAcc := getAccount(t, rec, contractAddr); callerAcc != nil {
		require.NotContains(t, callerAcc.StorageWrites, slot0,
			"STATICCALL: no write may leak into the caller's slot either")
	}
}

// Pure stack/arithmetic opcodes: ADD/MUL/SUB/DIV/MOD, PUSH/POP/DUP/SWAP, plus
// PC/GAS/MSIZE/JUMPDEST -- none touch the BAL. The contract's account is
// present only because applyCall's AccountRead(c.Address) fires at frame entry;
// the account record must otherwise be empty.
func TestApply_BAL_PureStackAndArithmetic_NoRecording(t *testing.T) {
	t.Parallel()

	// PUSH1 5 PUSH1 3 ADD  PUSH1 2 MUL  DUP1  POP POP  PC POP  GAS POP  STOP
	code := []byte{
		0x60, 0x05, 0x60, 0x03, 0x01, // 5 + 3
		0x60, 0x02, 0x02, // * 2
		0x80,       // DUP1
		0x50, 0x50, // POP POP
		0x58, 0x50, // PC POP
		0x5A, 0x50, // GAS POP
		0x00, // STOP
	}

	tr := newBALTestTranstionWithSeedContract(t, code, 1)
	_, err := tr.apply(callContract(1, 0))
	require.NoError(t, err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)
	require.NotNil(t, acc)

	require.Empty(t, acc.StorageReads)
	require.Empty(t, acc.StorageWrites)
	require.Empty(t, acc.BalanceChanges)
	require.Empty(t, acc.NonceChanges)
	require.Empty(t, acc.CodeChanges)
}

// Memory opcodes (MSTORE/MLOAD) are frame-local -- they must not appear in the
// block BAL.
func TestApply_BAL_MemoryOps_NoRecording(t *testing.T) {
	t.Parallel()

	// PUSH1 42 PUSH1 0 MSTORE  PUSH1 0 MLOAD POP  STOP
	code := []byte{
		0x60, 0x2A, 0x60, 0x00, 0x52,
		0x60, 0x00, 0x51, 0x50,
		0x00,
	}

	tr := newBALTestTranstionWithSeedContract(t, code, 1)
	_, err := tr.apply(callContract(1, 0))
	require.NoError(t, err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)
	require.Empty(t, acc.StorageReads)
	require.Empty(t, acc.StorageWrites)
}

// LOG0/1/2/3/4 emit events; they must not touch the BAL.
func TestApply_BAL_Log_NoRecording(t *testing.T) {
	t.Parallel()

	// PUSH1 0 PUSH1 0 LOG0  STOP    (size=0, offset=0 -> zero-length log)
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xA0, 0x00}

	tr := newBALTestTranstionWithSeedContract(t, code, 1)
	_, err := tr.apply(callContract(1, 0))
	require.NoError(t, err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)
	require.Empty(t, acc.StorageReads)
	require.Empty(t, acc.StorageWrites)
	require.Empty(t, acc.BalanceChanges)
}

// Transient storage (EIP-1153) is per-tx and never touches persistent state,
// so TSTORE / TLOAD must not appear in the BAL at all.
func TestApply_BAL_TransientStorage_NoRecording(t *testing.T) {
	t.Parallel()

	// PUSH1 7 PUSH1 3 TSTORE  PUSH1 3 TLOAD POP  STOP
	code := []byte{
		0x60, 0x07, 0x60, 0x03, 0x5D,
		0x60, 0x03, 0x5C, 0x50,
		0x00,
	}

	cfg := balTestConfig(true)
	cfg.EIP1153 = true

	pre := map[types.Address]*PreState{
		from:         {Nonce: 0, Balance: 10_000_000},
		contractAddr: {},
	}
	snap := newStateWithCode(pre, map[types.Address][]byte{contractAddr: code})

	tr := newBALTransition(t, cfg, snap, 1)

	_, err := tr.apply(callContract(1, 0))
	require.NoError(t, err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

	require.Empty(t, acc.StorageReads, "TLOAD must not appear as a persistent StorageRead")
	require.Empty(t, acc.StorageWrites, "TSTORE must not appear as a persistent StorageWrite")
}

// Context opcodes ADDRESS/CALLER/CALLVALUE/CALLDATASIZE/GASPRICE/CHAINID etc.
// read frame or block context, not account state. None must record anything.
func TestApply_BAL_ContextOps_NoRecording(t *testing.T) {
	t.Parallel()

	// ADDRESS POP  CALLER POP  CALLVALUE POP  CALLDATASIZE POP  GASPRICE POP  STOP
	code := []byte{
		0x30, 0x50, // ADDRESS POP
		0x33, 0x50, // CALLER POP
		0x34, 0x50, // CALLVALUE POP
		0x36, 0x50, // CALLDATASIZE POP
		0x3A, 0x50, // GASPRICE POP
		0x00,
	}

	tr := newBALTestTranstionWithSeedContract(t, code, 1)
	_, err := tr.apply(callContract(1, 0))
	require.NoError(t, err)

	rec := getRecord(t, tr)
	acc := getAccount(t, rec, contractAddr)

	// The contract is touched (AccountRead at frame entry) but nothing more.
	require.Empty(t, acc.StorageReads)
	require.Empty(t, acc.StorageWrites)
	require.Empty(t, acc.CodeChanges)

	// Critically: the account map must not have gained any other address from
	// these opcodes. Only the tx participants belong here. `callContract`
	// sends from addr1 (see bal_test.go), so that -- not `from` -- is the EOA.
	for addr := range rec.Accounts {
		require.Contains(t,
			map[types.Address]struct{}{from: {}, contractAddr: {}},
			addr,
			"context opcodes must not add unrelated accounts to the BAL (got %s)", addr)
	}
}
