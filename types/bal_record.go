package types

import (
	"bytes"
	"cmp"
	"maps"
	"math/big"
	"slices"

	"golang.org/x/crypto/sha3"
)

// AccountAccessRecord records all state elements of an account that were touched
// during block execution as defined in EIP-7928. Maps are keyed by block-access
// index; a second write at the same index overwrites (last-wins = post-value of
// that tx), so no explicit dedup is needed — the map enforces it.
type AccountAccessRecord struct {
	StorageWrites  map[Hash]map[uint64]Hash
	BalanceChanges map[uint64]*big.Int
	NonceChanges   map[uint64]uint64
	CodeChanges    map[uint64][]byte
}

func NewAccountAccessRecord() *AccountAccessRecord {
	return &AccountAccessRecord{
		StorageWrites:  make(map[Hash]map[uint64]Hash),
		BalanceChanges: make(map[uint64]*big.Int),
		NonceChanges:   make(map[uint64]uint64),
		CodeChanges:    make(map[uint64][]byte),
	}
}

// BlockAccessListRecord holds all accounts and their state accesses during block
// execution as defined in EIP-7928.
type BlockAccessListRecord map[Address]*AccountAccessRecord

func NewBlockAccessListRecord() BlockAccessListRecord {
	return make(map[Address]*AccountAccessRecord)
}

// Merge folds other into b. Combines per-tx (or per-worker) records into one
// block-level record. Since maps are keyed by block-access index and each index
// belongs to exactly one tx, keys never collide across different txs — so a
// plain per-key copy is correct and order-independent.
func (b BlockAccessListRecord) Merge(other BlockAccessListRecord) {
	if other == nil {
		return
	}

	for addr, otherAcc := range other {
		acc, ok := b[addr]
		if !ok {
			b[addr] = otherAcc
			continue
		}

		for slot, writes := range otherAcc.StorageWrites {
			dst, ok := acc.StorageWrites[slot]
			if !ok {
				acc.StorageWrites[slot] = writes
				continue
			}
			maps.Copy(dst, writes)
		}

		maps.Copy(acc.BalanceChanges, otherAcc.BalanceChanges)
		maps.Copy(acc.NonceChanges, otherAcc.NonceChanges)
		maps.Copy(acc.CodeChanges, otherAcc.CodeChanges)
	}
}

func (a *AccountAccessRecord) Copy() *AccountAccessRecord {
	cpy := &AccountAccessRecord{
		StorageWrites:  make(map[Hash]map[uint64]Hash, len(a.StorageWrites)),
		BalanceChanges: make(map[uint64]*big.Int, len(a.BalanceChanges)),
		NonceChanges:   maps.Clone(a.NonceChanges),
		CodeChanges:    make(map[uint64][]byte, len(a.CodeChanges)),
	}

	for slot, writes := range a.StorageWrites {
		cpy.StorageWrites[slot] = maps.Clone(writes)
	}

	for idx, balance := range a.BalanceChanges {
		cpy.BalanceChanges[idx] = new(big.Int).Set(balance)
	}

	for idx, code := range a.CodeChanges {
		cpy.CodeChanges[idx] = bytes.Clone(code)
	}

	return cpy
}

func (b BlockAccessListRecord) Copy() BlockAccessListRecord {
	res := make(BlockAccessListRecord, len(b))
	for addr, aa := range b {
		res[addr] = aa.Copy()
	}

	return res
}

// sortedIndices returns the map's uint64 keys ascending — the canonical
// block-access-index order every encoded change slice requires.
func sortedIndices[V any](m map[uint64]V) []uint64 {
	idxs := slices.Collect(maps.Keys(m))
	slices.SortFunc(idxs, cmp.Compare)

	return idxs
}

func (a *AccountAccessRecord) encode(addr Address) accountAccessEncoded {
	res := accountAccessEncoded{
		Address:        addr,
		StorageChanges: make([]slotChanges, 0, len(a.StorageWrites)),
		BalanceChanges: make([]balanceChange, 0, len(a.BalanceChanges)),
		NonceChanges:   make([]nonceChange, 0, len(a.NonceChanges)),
		CodeChanges:    make([]codeChange, 0, len(a.CodeChanges)),
	}

	// storage_changes: slots lexicographic; writes ascending by index
	writeSlots := slices.Collect(maps.Keys(a.StorageWrites))
	slices.SortFunc(writeSlots, func(x, y Hash) int {
		return bytes.Compare(x[:], y[:])
	})

	for _, slot := range writeSlots {
		slotWrites := a.StorageWrites[slot]

		changes := make([]storageWrite, 0, len(slotWrites))
		for _, id := range sortedIndices(slotWrites) {
			changes = append(changes, storageWrite{
				TxIndex:   id,
				PostValue: slotWrites[id],
			})
		}

		res.StorageChanges = append(res.StorageChanges, slotChanges{
			Slot:        slot,
			SlotChanges: changes,
		})
	}

	// balance_changes: ascending by index
	for _, id := range sortedIndices(a.BalanceChanges) {
		res.BalanceChanges = append(res.BalanceChanges, balanceChange{
			TxIndex:     id,
			PostBalance: new(big.Int).Set(a.BalanceChanges[id]),
		})
	}

	// nonce_changes: ascending by index
	for _, id := range sortedIndices(a.NonceChanges) {
		res.NonceChanges = append(res.NonceChanges, nonceChange{
			TxIndex:   id,
			PostNonce: a.NonceChanges[id],
		})
	}

	// code_changes: ascending by index
	for _, id := range sortedIndices(a.CodeChanges) {
		res.CodeChanges = append(res.CodeChanges, codeChange{
			TxIndex: id,
			NewCode: bytes.Clone(a.CodeChanges[id]),
		})
	}

	return res
}

func (b BlockAccessListRecord) Encode() BlockAccessListEncoded {
	addresses := slices.Collect(maps.Keys(b))
	slices.SortFunc(addresses, func(x, y Address) int {
		return bytes.Compare(x[:], y[:])
	})

	res := make(BlockAccessListEncoded, 0, len(addresses))
	for _, addr := range addresses {
		res = append(res, b[addr].encode(addr))
	}

	return res
}

func (b BlockAccessListRecord) GetOrCreate(addr Address) *AccountAccessRecord {
	acc, ok := b[addr]
	if !ok {
		acc = NewAccountAccessRecord()
		b[addr] = acc
	}

	return acc
}

func (a *AccountAccessRecord) RecordStorageWrite(id uint64, slot, val Hash) {
	slotWrites, ok := a.StorageWrites[slot]
	if !ok {
		slotWrites = make(map[uint64]Hash)
		a.StorageWrites[slot] = slotWrites
	}

	slotWrites[id] = val
}

func (a *AccountAccessRecord) RecordBalanceChange(id uint64, balance *big.Int) {
	a.BalanceChanges[id] = new(big.Int).Set(balance)
}

func (a *AccountAccessRecord) RecordNonceChange(id uint64, nonce uint64) {
	a.NonceChanges[id] = nonce
}

func (a *AccountAccessRecord) RecordCodeChange(id uint64, code []byte) {
	a.CodeChanges[id] = bytes.Clone(code)
}

// storageWrite is one transaction's write to a storage slot.
type storageWrite struct {
	TxIndex   uint64
	PostValue Hash
}

// slotChanges aggregates all per-tx writes to a signle storage slot.
type slotChanges struct {
	Slot        Hash
	SlotChanges []storageWrite
}

// balanceChange is one transaction's post-state balance for an account.
type balanceChange struct {
	TxIndex     uint64
	PostBalance *big.Int
}

// nonceChange is one transaction's post-state nonce for an account.
type nonceChange struct {
	TxIndex   uint64
	PostNonce uint64
}

// codeChange is one transaction's deployed runtime bytecode for an account
type codeChange struct {
	TxIndex uint64
	NewCode []byte
}

// accountAccessEncoded is the encoding format of ConstructionAccountAccess.
type accountAccessEncoded struct {
	Address        Address
	StorageChanges []slotChanges
	BalanceChanges []balanceChange
	NonceChanges   []nonceChange
	CodeChanges    []codeChange
}

// BlockAccessListEncoded is the encoding format of ConstructionBlockAccessList.
type BlockAccessListEncoded []accountAccessEncoded

func (b BlockAccessListEncoded) Hash() Hash {
	keccak256 := func(v ...[]byte) []byte {
		h := sha3.NewLegacyKeccak256()
		for _, i := range v {
			h.Write(i)
		}

		return h.Sum(nil)
	}

	return BytesToHash(keccak256(b.MarshalRLPTo(nil)))
}
