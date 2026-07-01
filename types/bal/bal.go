package bal

import (
	"bytes"
	"cmp"
	"maps"
	"math/big"
	"slices"

	"github.com/0xPolygon/polygon-edge/types"
)

// AccountAccessRecord records all state elements of an account that were touched
// during block execution as defined in EIP-7928.
type AccountAccessRecord struct {
	// StorageWrites holds the values of storage slots that were modified during
	// block execution, indexed by slot key and the tx index at which each modification
	// occurred.
	StorageWrites map[types.Hash]map[uint32]types.Hash `json:"storageWrites,omitempty"`

	// StorageReads is the set of slot keys that were read during block execution
	// but never written. Once a slot is written, it is removed from this set and
	// tracked exclusively in StorageWrites.
	//
	// Note: reads are not keyed by tx index, so this field cannot be used to detect
	// R/W conflicts between transactions. It is retained for IO prefetching purposes
	// only.
	//
	// Required for paralelization:
	// StorageReads map[types.Hash]map[uint32]struct{} `json:"storageReads,omitempty"`
	StorageReads map[types.Hash]struct{} `json:"storageReads,omitempty"`

	// BalanceChanges contains all the changes of the account balance during the
	// block execution, keyed by transaction indices where the balance was changed.
	//
	// Note: balance reads (e.g. via the BALANCE opcode) are not tracked, so R/W
	// conflicts on balance between transactions cannot be detected from this field
	// alone.
	//
	// Additional field is required for paralelization:
	// BalanceReads  map[uint32]struct{} `json:"balanceReads,omitempty"`
	BalanceChanges map[uint32]*big.Int `json:"balanceChange,omitempty"`

	// NonceChanges contains all the changes of the account nonce during the block
	// execution, keyed by transaction indices where the nonce was changed. Since nonce
	// reads are not possible from within the EVM (no opcode exists), so R/W conflict
	// detection is not applicable here.
	NonceChanges map[uint32]uint64 `json:"nonceChanges,omitempty"`

	// CodeChanges contains all the changes of the account code during the block
	// execution, keyed by transaction indices where the code was changed.
	//
	// Note: code reads (e.g. via EXTCODECOPY) are not tracked, so R/W conflicts on
	// code between transactions cannot be detected from this field alone.
	//
	// Additional field is required for parallelization:
	// CodeReads map[uint32]struct{} `json:"codeReads,omitempty"`
	CodeChanges map[uint32][]byte `json:"codeChanges,omitempty"`
}

func NewAccountAccessRecord() *AccountAccessRecord {
	return &AccountAccessRecord{
		StorageWrites:  make(map[types.Hash]map[uint32]types.Hash),
		StorageReads:   make(map[types.Hash]struct{}),
		BalanceChanges: make(map[uint32]*big.Int),
		NonceChanges:   make(map[uint32]uint64),
		CodeChanges:    make(map[uint32][]byte),
	}
}

// BlockAccessListRecord holds all accounts and their state accesses during block
// execution as defined in EIP-7928.
type BlockAccessListRecord struct {
	Accounts map[types.Address]*AccountAccessRecord
}

func NewBlockAccessListRecord() *BlockAccessListRecord {
	return &BlockAccessListRecord{
		Accounts: make(map[types.Address]*AccountAccessRecord),
	}
}

func (b *BlockAccessListRecord) Merge(other *BlockAccessListRecord) {
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
		maps.Copy(acc.CodeChanges, otherAcc.CodeChanges)
	}
}

func (b *BlockAccessListRecord) Copy() *BlockAccessListRecord {
	res := &BlockAccessListRecord{
		Accounts: make(map[types.Address]*AccountAccessRecord, len(b.Accounts)),
	}

	for addr, aa := range b.Accounts {
		aaCopy := &AccountAccessRecord{
			StorageWrites:  make(map[types.Hash]map[uint32]types.Hash, len(aa.StorageWrites)),
			StorageReads:   maps.Clone(aa.StorageReads),
			BalanceChanges: make(map[uint32]*big.Int, len(aa.BalanceChanges)),
			NonceChanges:   maps.Clone(aa.NonceChanges),
			CodeChanges:    make(map[uint32][]byte, len(aa.CodeChanges)),
		}

		for key, sw := range aa.StorageWrites {
			aaCopy.StorageWrites[key] = maps.Clone(sw)
		}

		for index, balance := range aa.BalanceChanges {
			aaCopy.BalanceChanges[index] = new(big.Int).Set(balance)
		}

		for index, code := range aa.CodeChanges {
			aaCopy.CodeChanges[index] = bytes.Clone(code)
		}

		res.Accounts[addr] = aaCopy
	}

	return res
}

func (a *AccountAccessRecord) toEncodingObj(addr types.Address) AccountAccess {
	res := AccountAccess{
		Address:        addr,
		StorageChanges: make([]SlotChanges, 0, len(a.StorageWrites)),
		StorageReads:   make([]types.Hash, 0, len(a.StorageReads)),
		BalanceChanges: make([]BalanceChange, 0, len(a.BalanceChanges)),
		NonceChanges:   make([]NonceChange, 0, len(a.NonceChanges)),
		CodeChanges:    make([]CodeChange, 0, len(a.CodeChanges)),
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
	codeIndices := slices.Collect(maps.Keys(a.CodeChanges))
	slices.SortFunc(codeIndices, cmp.Compare)

	for _, idx := range codeIndices {
		res.CodeChanges = append(res.CodeChanges, CodeChange{
			BlockAccessIndex: idx,
			NewCode:          bytes.Clone(a.CodeChanges[idx]),
		})
	}

	return res
}

func (b *BlockAccessListRecord) ToEncodingObj() BlockAccessList {
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

func (b *BlockAccessListRecord) GetOrCreate(addr types.Address) *AccountAccessRecord {
	acc, ok := b.Accounts[addr]
	if !ok {
		acc = NewAccountAccessRecord()
		b.Accounts[addr] = acc
	}

	return acc
}

func (a *AccountAccessRecord) RecordStorageRead(slot types.Hash) {
	if _, written := a.StorageWrites[slot]; written {
		return
	}
	a.StorageReads[slot] = struct{}{}
}

func (a *AccountAccessRecord) RecordStorageWrite(idx uint32, slot, val types.Hash) {
	if _, ok := a.StorageWrites[slot]; !ok {
		a.StorageWrites[slot] = make(map[uint32]types.Hash)
	}
	a.StorageWrites[slot][idx] = val
	delete(a.StorageReads, slot)
}

func (a *AccountAccessRecord) RecordBalanceChange(idx uint32, balance *big.Int) {
	a.BalanceChanges[idx] = new(big.Int).Set(balance)
}

func (a *AccountAccessRecord) RecordNonceChange(idx uint32, nonce uint64) {
	a.NonceChanges[idx] = nonce
}

func (a *AccountAccessRecord) RecordCodeChange(idx uint32, code []byte) {
	a.CodeChanges[idx] = bytes.Clone(code)
}
