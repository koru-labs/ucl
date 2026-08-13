package state

import (
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSnapshot struct {
	state map[types.Address]*PreState
	codes map[types.Hash][]byte
}

func (m *mockSnapshot) GetStorage(addr types.Address, root types.Hash, key types.Hash) types.Hash {
	raw, ok := m.state[addr]
	if !ok {
		return types.Hash{}
	}

	res, ok := raw.State[key]
	if !ok {
		return types.Hash{}
	}

	return res
}

func (m *mockSnapshot) GetAccount(addr types.Address) (*Account, error) {
	raw, ok := m.state[addr]
	if !ok {
		return nil, fmt.Errorf("account not found")
	}

	acct := &Account{
		Balance: new(big.Int).SetUint64(raw.Balance),
		Nonce:   raw.Nonce,
	}

	if len(raw.CodeHash) > 0 {
		acct.CodeHash = raw.CodeHash
	}

	return acct, nil
}

func (m *mockSnapshot) GetCode(hash types.Hash) ([]byte, bool) {
	code, ok := m.codes[hash]
	if !ok {
		return []byte{}, false
	}

	return code, true
}

func (m *mockSnapshot) GetRootHash() types.Hash {
	return emptyStateHash
}

func (m *mockSnapshot) Commit(objs []*Object) (Snapshot, []byte, error) {
	return nil, nil, nil
}

func newStateWithPreState(preState map[types.Address]*PreState, codes map[types.Hash][]byte) Snapshot {
	return &mockSnapshot{state: preState, codes: codes}
}

func newTestTxn(p map[types.Address]*PreState) *Txn {
	return newTxn(newStateWithPreState(p, nil))
}

func newStateWithCode(preState map[types.Address]*PreState, code map[types.Address][]byte) Snapshot {
	codes := make(map[types.Hash][]byte, len(code))
	for addr, c := range code {
		acc, ok := preState[addr]
		if !ok {
			acc = &PreState{}
			preState[addr] = acc
		}

		h := crypto.Keccak256(c)
		acc.CodeHash = h
		codes[types.BytesToHash(h)] = c
	}

	return &mockSnapshot{state: preState, codes: codes}
}

func TestTransientStorage(t *testing.T) {
	t.Run("set and get value", func(t *testing.T) {
		txn := newTestTxn(defaultPreState)

		// Set value hash2 for addr1 at slot1 in transient storage.
		txn.SetTransientState(addr1, slot1, hash2)

		// Retrieving a value must not remove it, so we read it twice to confirm.
		for range 2 {
			require.Equal(t, hash2, txn.GetTransientState(addr1, slot1))
		}

		// Other slots for the same address must remain zero.
		require.Equal(t, types.Hash{}, txn.GetTransientState(addr1, slot0))
		require.Equal(t, types.Hash{}, txn.GetTransientState(addr1, slot2))

		// The same slot on a different address must also remain zero.
		require.Equal(t, types.Hash{}, txn.GetTransientState(addr2, slot0))
	})

	t.Run("clear transient storage", func(t *testing.T) {
		txn := newTestTxn(defaultPreState)

		// Populate both ordinary storage and transient storage.
		txn.SetState(addr1, slot0, hash1)
		txn.SetTransientState(addr1, slot0, hash1)
		txn.SetTransientState(addr2, slot0, hash2)
		txn.SetTransientState(addr2, slot1, hash1)

		require.Equal(t, hash1, txn.GetState(addr1, slot0))
		require.Equal(t, hash1, txn.GetTransientState(addr1, slot0))
		require.Equal(t, hash2, txn.GetTransientState(addr2, slot0))
		require.Equal(t, hash1, txn.GetTransientState(addr2, slot1))

		txn.CleanRadixObjects()

		// After clearing, all transient slots must be zero.
		// Ordinary storage must remain untouched.
		require.Equal(t, hash1, txn.GetState(addr1, slot0))
		require.Equal(t, types.Hash{}, txn.GetTransientState(addr1, slot0))
		require.Equal(t, types.Hash{}, txn.GetTransientState(addr2, slot0))
		require.Equal(t, types.Hash{}, txn.GetTransientState(addr2, slot1))
	})
}

func TestSnapshotUpdateData(t *testing.T) {
	txn := newTestTxn(defaultPreState)

	txn.SetState(addr1, slot1, hash1)
	assert.Equal(t, hash1, txn.GetState(addr1, slot1))

	txn.SetTransientState(addr1, slot1, hash2)
	assert.Equal(t, hash2, txn.GetTransientState(addr1, slot1))
	txn.SetTransientState(addr2, slot2, hash1)
	assert.Equal(t, hash1, txn.GetTransientState(addr2, slot2))

	ss := txn.Snapshot()

	txn.SetState(addr1, slot1, hash2)
	assert.Equal(t, hash2, txn.GetState(addr1, slot1))

	txn.SetTransientState(addr1, slot1, hash3)
	assert.Equal(t, hash3, txn.GetTransientState(addr1, slot1))
	txn.SetTransientState(addr2, slot2, hash2)
	assert.Equal(t, hash2, txn.GetTransientState(addr2, slot2))
	txn.SetTransientState(addr2, slot1, hash1)
	assert.Equal(t, hash1, txn.GetTransientState(addr2, slot1))

	assert.NoError(t, txn.RevertToSnapshot(ss))

	assert.Equal(t, hash1, txn.GetState(addr1, slot1))
	assert.Equal(t, hash2, txn.GetTransientState(addr1, slot1))
	assert.Equal(t, hash1, txn.GetTransientState(addr2, slot2))
	assert.Equal(t, types.Hash{}, txn.GetTransientState(addr2, slot1))
}

func TestGetDumpTree(t *testing.T) {
	txn := newTestTxn(defaultPreState)
	txn.SetState(addr1, hash1, hash2)
	txn.SetState(addr2, hash2, hash1)

	dump := &Dump{
		Accounts: make(map[types.Address]DumpAccount),
	}
	opts := &DumpInfo{
		Start:             []byte{},
		Max:               1,
		OnlyWithAddresses: false,
		SkipCode:          false,
		SkipStorage:       false,
	}

	nextKey, err := txn.GetDumpTree(dump, opts, false)

	assert.NoError(t, err)
	assert.Equal(t, addr2.Bytes(), nextKey)
	assert.Len(t, dump.Accounts, 1)

	dumpAcc, exist := dump.Accounts[addr1]

	assert.True(t, exist)
	assert.Equal(t, dumpAcc.Key, addr1.Bytes())

	opts.Start = nextKey
	nextKey, err = txn.GetDumpTree(dump, opts, false)

	assert.NoError(t, err)
	assert.Equal(t, []byte(nil), nextKey)
	assert.Len(t, dump.Accounts, 1)

	dumpAcc, exist = dump.Accounts[addr2]

	assert.True(t, exist)
	assert.Equal(t, dumpAcc.Key, addr2.Bytes())
}

func TestIncrNonce(t *testing.T) {
	t.Parallel()

	var (
		address0               = types.StringToAddress("0")
		address1               = types.StringToAddress("1")
		maxUint64NonceValue    = uint64(math.MaxUint64)
		nonMaxUint64NonceValue = uint64(3)
	)

	txn := newTestTxn(defaultPreState)

	txn.SetNonce(address0, maxUint64NonceValue)
	txn.SetNonce(address1, nonMaxUint64NonceValue)

	require.Error(t, txn.IncrNonce(address0))
	require.NoError(t, txn.IncrNonce(address1))
	require.Equal(t, nonMaxUint64NonceValue+1, txn.GetNonce(address1))
}

func newBALWorkerTxn(t *testing.T, preState map[types.Address]*PreState) *Txn {
	t.Helper()

	txn := newTestTxn(preState)
	txn.recorder = NewTxAccessRecorder(0)
	txn.bar = types.BlockAccessRecord{} // Empty BAR - recorder is the only source

	return txn
}

func TestSuicide_RecordsEmptyStateForBAL(t *testing.T) {
	t.Parallel()

	preState := map[types.Address]*PreState{
		contractAddr: {
			Nonce:   1,
			Balance: 100,
		},
	}

	txn := newBALWorkerTxn(t, preState)

	suicided := txn.Suicide(contractAddr)
	require.True(t, suicided, "the first Suicide call must return true")

	// The recorder must have received "empty account" markers
	acc, ok := txn.recorder.current[contractAddr]
	require.True(t, ok, "recorder must have an entry for the suicided account")

	require.NotNil(t, acc.Balance, "recorder Balance must be set")
	require.Zero(t, acc.Balance.Sign(),
		"recorder Balance must be 0, got: %s", acc.Balance)

	require.NotNil(t, acc.Nonce, "recorder Nonce must be set")
	require.Zero(t, *acc.Nonce,
		"recorder Nonce must be 0, got: %d", *acc.Nonce)

	require.NotNil(t, acc.Code, "recorder Code must be set (non-nil)")
	require.Empty(t, acc.Code,
		"recorder Code must be an empty slice, got: %x", acc.Code)
}

func TestSuicide_RepeatedInSameTxRecordsOnce(t *testing.T) {
	t.Parallel()

	preState := map[types.Address]*PreState{
		contractAddr: {
			Nonce:   1,
			Balance: 100,
		},
	}

	txn := newBALWorkerTxn(t, preState)

	require.True(t, txn.Suicide(contractAddr))
	require.False(t, txn.Suicide(contractAddr),
		"the second Suicide call must return false")

	// The state in the recorder remains unchanged (empty account)
	acc := txn.recorder.current[contractAddr]
	require.Zero(t, acc.Balance.Sign())
	require.NotNil(t, acc.Nonce)
	require.Zero(t, *acc.Nonce)
	require.Empty(t, acc.Code)
}

func TestSuicide_LaterBalanceWriteOverwrites(t *testing.T) {
	// If a callback after Suicide adds a balance to the account
	// (e.g. resurrection in the same tx through AddBalance), the latest balance
	// must be recorded. The goal is for the verifier to see the final state,
	// not the intermediate state.
	t.Parallel()

	preState := map[types.Address]*PreState{
		contractAddr: {Nonce: 1, Balance: 100},
	}

	txn := newBALWorkerTxn(t, preState)

	require.True(t, txn.Suicide(contractAddr))

	txn.AddBalance(contractAddr, big.NewInt(50))

	acc := txn.recorder.current[contractAddr]
	require.Equal(t, uint64(50), acc.Balance.Uint64(),
		"final balance in recorder must be 50, got: %s", acc.Balance)

	// The account is still marked as suicided in the trie — resurrection semantics
	// are delicate, but for now the important thing is that the recorder reflects
	// the final state.
	require.True(t, txn.HasSuicided(contractAddr))
}

func TestSuicide_NoRecorder_DoesNothingToRecorder(t *testing.T) {
	// Proposer mode (EIP-7928 off): txn.recorder == nil. Suicide should
	// work normally without writing anything to the recorder.
	t.Parallel()

	preState := map[types.Address]*PreState{
		contractAddr: {Nonce: 1, Balance: 100},
	}

	txn := newTestTxn(preState) // without a recorder

	require.True(t, txn.Suicide(contractAddr))
	require.True(t, txn.HasSuicided(contractAddr))
	require.Zero(t, txn.GetBalance(contractAddr).Uint64())
}

func TestSuicide_NonExistentAccount(t *testing.T) {
	t.Parallel()

	preState := map[types.Address]*PreState{} // empty pre-state

	txn := newBALWorkerTxn(t, preState)

	suicided := txn.Suicide(contractAddr)
	require.False(t, suicided,
		"Suicide on a non-existent account must return false")

	// The recorder must not contain an entry
	_, ok := txn.recorder.current[contractAddr]
	require.False(t, ok,
		"recorder must not record Suicide for a non-existent account")
}
