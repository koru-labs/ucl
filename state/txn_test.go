package state

import (
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSnapshot struct {
	state map[types.Address]*PreState
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

	return acct, nil
}

func (m *mockSnapshot) GetCode(hash types.Hash) ([]byte, bool) {
	return nil, false
}

func (m *mockSnapshot) GetRootHash() types.Hash {
	return emptyStateHash
}

func (m *mockSnapshot) Commit(objs []*Object) (Snapshot, []byte, error) {
	return nil, nil, nil
}

func newStateWithPreState(preState map[types.Address]*PreState) Snapshot {
	return &mockSnapshot{state: preState}
}

func newTestTxn(p map[types.Address]*PreState) *Txn {
	return newTxn(newStateWithPreState(p))
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

		txn.ClearTransientStorage()

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
