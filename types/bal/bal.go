package bal

import (
	"bytes"
	"cmp"
	"maps"
	"math/big"
	"slices"

	"github.com/0xPolygon/polygon-edge/types"
)

type ConstructionAccountAccess struct {
	StorageWrites map[types.Hash]map[uint32]types.Hash `json:"storageWrites,omitempty"`

	StorageReads map[types.Hash]struct{} `json:"storageReads,omitempty"`

	BalanceChanges map[uint32]*big.Int `json:"balanceChanges,omitempty"`

	NonceChanges map[uint32]uint64 `json:"nonceChanges,omitempty"`

	CodeChange map[uint32][]byte `json:"codeChange,omitempty"`
}

func NewConstructionAccountAccess() *ConstructionAccountAccess {
	return &ConstructionAccountAccess{
		StorageWrites:  make(map[types.Hash]map[uint32]types.Hash),
		StorageReads:   make(map[types.Hash]struct{}),
		BalanceChanges: make(map[uint32]*big.Int),
		NonceChanges:   make(map[uint32]uint64),
		CodeChange:     make(map[uint32][]byte),
	}
}

type ConstructionBlockAccessList struct {
	Accounts map[types.Address]*ConstructionAccountAccess
}

func NewConstructionBlockAccessList() *ConstructionBlockAccessList {
	return &ConstructionBlockAccessList{
		Accounts: make(map[types.Address]*ConstructionAccountAccess),
	}
}

func (b *ConstructionBlockAccessList) Merge(other *ConstructionBlockAccessList) {
	if other == nil {
		return
	}

	for addr, otherAcc := range other.Accounts {
		acc, ok := b.Accounts[addr]
		if !ok {
			b.Accounts[addr] = otherAcc

			continue
		}

		for key, writes := range otherAcc.StorageWrites {
			existing, ok := acc.StorageWrites[key]
			if !ok {
				acc.StorageWrites[key] = writes
			} else {
				for txIdx, value := range writes {
					existing[txIdx] = value
				}
			}

			delete(acc.StorageReads, key)
		}

		for key := range otherAcc.StorageReads {
			if _, ok := acc.StorageWrites[key]; ok {
				continue
			}

			acc.StorageReads[key] = struct{}{}
		}

		maps.Copy(acc.BalanceChanges, otherAcc.BalanceChanges)
		maps.Copy(acc.NonceChanges, otherAcc.NonceChanges)
		maps.Copy(acc.CodeChange, otherAcc.CodeChange)
	}
}

func (b *ConstructionBlockAccessList) Copy() *ConstructionBlockAccessList {
	res := &ConstructionBlockAccessList{
		Accounts: make(map[types.Address]*ConstructionAccountAccess, len(b.Accounts)),
	}

	for addr, aa := range b.Accounts {
		aaCopy := &ConstructionAccountAccess{
			StorageWrites:  make(map[types.Hash]map[uint32]types.Hash, len(aa.StorageWrites)),
			StorageReads:   maps.Clone(aa.StorageReads),
			BalanceChanges: make(map[uint32]*big.Int, len(aa.BalanceChanges)),
			NonceChanges:   maps.Clone(aa.NonceChanges),
			CodeChange:     make(map[uint32][]byte, len(aa.CodeChange)),
		}

		for key, sw := range aa.StorageWrites {
			aaCopy.StorageWrites[key] = maps.Clone(sw)
		}

		for index, balance := range aa.BalanceChanges {
			aaCopy.BalanceChanges[index] = new(big.Int).Set(balance)
		}

		for index, code := range aa.CodeChange {
			aaCopy.CodeChange[index] = bytes.Clone(code)
		}

		res.Accounts[addr] = aaCopy
	}

	return res
}

func (a *ConstructionAccountAccess) toEncodingObj(addr types.Address) AccountAccess {
	res := AccountAccess{
		Address:        addr,
		StorageChanges: make([]SlotChanges, 0, len(a.StorageWrites)),
		StorageReads:   make([]types.Hash, 0, len(a.StorageReads)),
		BalanceChanges: make([]BalanceChange, 0, len(a.BalanceChanges)),
		NonceChanges:   make([]NonceChange, 0, len(a.NonceChanges)),
		CodeChanges:    make([]CodeChange, 0, len(a.CodeChange)),
	}

	// storage_changes: slots lexicographic; writes ascending by index
	writeSlots := slices.Collect(maps.Keys(a.StorageWrites))
	slices.SortFunc(writeSlots, func(x, y types.Hash) int {
		return bytes.Compare(x[:], y[:])
	})

	for _, slot := range writeSlots {
		slotWrites := a.StorageWrites[slot]

		indices := slices.Collect(maps.Keys(slotWrites))
		slices.SortFunc(indices, cmp.Compare)

		changes := make([]StorageWrite, 0, len(indices))
		for _, idx := range indices {
			changes = append(changes, StorageWrite{
				BlockAccessIndex: idx,
				PostValue:        slotWrites[idx],
			})
		}

		res.StorageChanges = append(res.StorageChanges, SlotChanges{
			Slot:        slot,
			SlotChanges: changes,
		})
	}

	// storage_reads: lexicographic by slot
	readSlots := slices.Collect(maps.Keys(a.StorageReads))
	slices.SortFunc(readSlots, func(x, y types.Hash) int {
		return bytes.Compare(x[:], y[:])
	})
	res.StorageReads = append(res.StorageReads, readSlots...)

	// balance_changes: ascending by index
	balanceIndices := slices.Collect(maps.Keys(a.BalanceChanges))
	slices.SortFunc(balanceIndices, cmp.Compare)

	for _, idx := range balanceIndices {
		res.BalanceChanges = append(res.BalanceChanges, BalanceChange{
			BlockAccessIndex: idx,
			PostBalance:      new(big.Int).Set(a.BalanceChanges[idx]),
		})
	}

	// nonce_changes: ascending by index
	nonceIndices := slices.Collect(maps.Keys(a.NonceChanges))
	slices.SortFunc(nonceIndices, cmp.Compare)

	for _, idx := range nonceIndices {
		res.NonceChanges = append(res.NonceChanges, NonceChange{
			BlockAccessIndex: idx,
			PostNonce:        a.NonceChanges[idx],
		})
	}

	// code_changes: ascending by index
	codeIndices := slices.Collect(maps.Keys(a.CodeChange))
	slices.SortFunc(codeIndices, cmp.Compare)

	for _, idx := range codeIndices {
		res.CodeChanges = append(res.CodeChanges, CodeChange{
			BlockAccessIndex: idx,
			NewCode:          bytes.Clone(a.CodeChange[idx]),
		})
	}

	return res
}

func (b *ConstructionBlockAccessList) ToEncodingObj() BlockAccessList {
	addresses := slices.Collect(maps.Keys(b.Accounts))
	slices.SortFunc(addresses, func(x, y types.Address) int {
		return bytes.Compare(x[:], y[:])
	})

	res := make(BlockAccessList, 0, len(addresses))
	for _, addr := range addresses {
		res = append(res, b.Accounts[addr].toEncodingObj(addr))
	}

	return res
}

func (b *ConstructionBlockAccessList) GetOrCreate(addr types.Address) *ConstructionAccountAccess {
	acc, ok := b.Accounts[addr]
	if !ok {
		acc = NewConstructionAccountAccess()
		b.Accounts[addr] = acc
	}
	return acc
}

func (a *ConstructionAccountAccess) RecordStorageRead(slot types.Hash) {
	if _, written := a.StorageWrites[slot]; written {
		return
	}
	a.StorageReads[slot] = struct{}{}
}

func (a *ConstructionAccountAccess) RecordStorageWrite(idx uint32, slot, val types.Hash) {
	if _, ok := a.StorageWrites[slot]; !ok {
		a.StorageWrites[slot] = make(map[uint32]types.Hash)
	}
	a.StorageWrites[slot][idx] = val
	delete(a.StorageReads, slot)
}

func (a *ConstructionAccountAccess) RecordBalanceChange(idx uint32, balance *big.Int) {
	a.BalanceChanges[idx] = new(big.Int).Set(balance)
}

func (a *ConstructionAccountAccess) RecordNonceChange(idx uint32, nonce uint64) {
	a.NonceChanges[idx] = nonce
}

func (a *ConstructionAccountAccess) RecordCodeChange(idx uint32, code []byte) {
	a.CodeChange[idx] = bytes.Clone(code)
}
