package bal

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
)

// BALItemCost is the gas-equivalent cost of a single BAL item (one address
// or one storage key), as defined by EIP-7928. Set deliberately below
// COLD_SLOAD_COST (2100) to leave headroom for system-contract and
// withdrawal entries that consume no block gas.
const BALItemCost = 2000

// StorageWrite is one transaction's write to a storage slot.
type StorageWrite struct {
	BlockAccessIndex uint32
	PostValue        types.Hash
}

// SlotChanges aggregates all per-tx writes to a signle storage slot.
type SlotChanges struct {
	Slot        types.Hash
	SlotChanges []StorageWrite
}

// BalanceChange is one transaction's post-state balance for an account.
type BalanceChange struct {
	BlockAccessIndex uint32
	PostBalance      *big.Int
}

// NonceChange is one transaction's post-state nonce for an account.
type NonceChange struct {
	BlockAccessIndex uint32
	PostNonce        uint64
}

// CodeChange is one transaction's deployed runtime bytecode for an account
type CodeChange struct {
	BlockAccessIndex uint32
	NewCode          []byte
}

// AccountAccess is the encoding format of ConstructionAccountAccess.
type AccountAccess struct {
	Address        types.Address
	StorageChanges []SlotChanges
	StorageReads   []types.Hash
	BalanceChanges []BalanceChange
	NonceChanges   []NonceChange
	CodeChanges    []CodeChange
}

// BlockAccessList is the encoding format of ConstructionBlockAccessList.
type BlockAccessList []AccountAccess

func isStrictlySortedFunc[S ~[]E, E any](x S, cmp func(a, b E) int) bool {
	for i := 1; i < len(x); i++ {
		if cmp(x[i-1], x[i]) >= 0 {
			return false // covers both unsorted and duplicate
		}
	}

	return true
}

// validate asserts that a SlotChanges entry contains at least one write
//
//	and that its writes are strictly ascending and unique by block access index
func (sc *SlotChanges) validate(maxBALIndex int) error {
	if len(sc.SlotChanges) == 0 {
		return errors.New("empty slot changes")
	}

	if !isStrictlySortedFunc(sc.SlotChanges, func(a, b StorageWrite) int {
		return cmp.Compare(a.BlockAccessIndex, b.BlockAccessIndex)
	}) {
		return errors.New("storage write indexes must be unique and sorted")
	}

	if last := sc.SlotChanges[len(sc.SlotChanges)-1].BlockAccessIndex; int(last) > maxBALIndex {
		return fmt.Errorf("storage write index exceeds limit, index: %d, limit: %d", last, maxBALIndex)
	}

	return nil
}

func (aa *AccountAccess) validate(maxBALIndex int) error {
	if !isStrictlySortedFunc(aa.StorageChanges, func(a, b SlotChanges) int {
		return bytes.Compare(a.Slot[:], b.Slot[:])
	}) {
		return errors.New("storage write slots must be unique and sorted")
	}

	for i := range aa.StorageChanges {
		if err := aa.StorageChanges[i].validate(maxBALIndex); err != nil {
			return err
		}
	}

	if !isStrictlySortedFunc(aa.StorageReads, func(a, b types.Hash) int {
		return bytes.Compare(a[:], b[:])
	}) {
		return errors.New("storage read slots must be unique and sorted")
	}

	writeKeys := make(map[types.Hash]struct{}, len(aa.StorageChanges))
	for i := range aa.StorageChanges {
		writeKeys[aa.StorageChanges[i].Slot] = struct{}{}
	}

	for i := range aa.StorageReads {
		if _, ok := writeKeys[aa.StorageReads[i]]; ok {
			return errors.New("storage key reported in both read/write sets")
		}
	}

	if !isStrictlySortedFunc(aa.BalanceChanges, func(a, b BalanceChange) int {
		return cmp.Compare(a.BlockAccessIndex, b.BlockAccessIndex)
	}) {
		return errors.New("balance changes must be unique and sorted")
	}

	if n := len(aa.BalanceChanges); n > 0 {
		if last := aa.BalanceChanges[n-1].BlockAccessIndex; int(last) > maxBALIndex {
			return fmt.Errorf("balance change index exceeds limit, index: %d, limit %d", last, maxBALIndex)
		}
	}

	if !isStrictlySortedFunc(aa.NonceChanges, func(a, b NonceChange) int {
		return cmp.Compare(a.BlockAccessIndex, b.BlockAccessIndex)
	}) {
		return errors.New("nonce changes must be unique and sorted")
	}

	if n := len(aa.NonceChanges); n > 0 {
		if last := aa.NonceChanges[n-1].BlockAccessIndex; int(last) > maxBALIndex {
			return fmt.Errorf("nonce change index exceeds limit, index: %d, limit: %d", last, maxBALIndex)
		}
	}

	if !isStrictlySortedFunc(aa.CodeChanges, func(a, b CodeChange) int {
		return cmp.Compare(a.BlockAccessIndex, b.BlockAccessIndex)
	}) {
		return errors.New("code changes must be unique and sorted")
	}

	if n := len(aa.CodeChanges); n > 0 {
		if last := aa.CodeChanges[n-1].BlockAccessIndex; int(last) > maxBALIndex {
			return fmt.Errorf("code change index exceeds limit, index: %d, limit: %d", last, maxBALIndex)
		}
	}

	return nil
}

func (b BlockAccessList) Validate(blockGasLimit uint64, blockTxCount int) error {
	if !isStrictlySortedFunc(b, func(a, c AccountAccess) int {
		return bytes.Compare(a.Address[:], c.Address[:])
	}) {
		return errors.New("block access list accounts not in lexicographic order")
	}

	maxBALIndex := blockTxCount + 1

	for i := range b {
		if err := b[i].validate(maxBALIndex); err != nil {
			return err
		}
	}

	return b.ValidateSize(blockGasLimit)
}

func (b BlockAccessList) itemCount() uint64 {
	count := uint64(len(b))
	for i := range b {
		count += uint64(len(b[i].StorageChanges)) + uint64(len(b[i].StorageReads))
	}

	return count
}

func (b BlockAccessList) ValidateSize(blockGasLimit uint64) error {
	items := b.itemCount()

	limit := blockGasLimit / BALItemCost
	if items > limit {
		return fmt.Errorf(
			"block access list exceeds size contraint: items:%d, limit:%d (block gas limit %d / %d)",
			items, limit, blockGasLimit, BALItemCost,
		)
	}

	return nil
}

func (b BlockAccessList) Hash() types.Hash {
	return types.BytesToHash(crypto.Keccak256(b.MarshalRLPTo(nil)))
}

func (aa *AccountAccess) Copy() AccountAccess {
	res := AccountAccess{
		Address:        aa.Address,
		StorageChanges: make([]SlotChanges, len(aa.StorageChanges)),
		StorageReads:   slices.Clone(aa.StorageReads),
		BalanceChanges: make([]BalanceChange, len(aa.BalanceChanges)),
		NonceChanges:   slices.Clone(aa.NonceChanges),
		CodeChanges:    make([]CodeChange, len(aa.CodeChanges)),
	}

	for i, sc := range aa.StorageChanges {
		res.StorageChanges[i] = SlotChanges{
			Slot:        sc.Slot,
			SlotChanges: slices.Clone(sc.SlotChanges),
		}
	}

	for i, bc := range aa.BalanceChanges {
		res.BalanceChanges[i] = BalanceChange{
			BlockAccessIndex: bc.BlockAccessIndex,
			PostBalance:      new(big.Int).Set(bc.PostBalance),
		}
	}

	for i, cc := range aa.CodeChanges {
		res.CodeChanges[i] = CodeChange{
			BlockAccessIndex: cc.BlockAccessIndex,
			NewCode:          bytes.Clone(cc.NewCode),
		}
	}

	return res
}

func (b BlockAccessList) Copy() BlockAccessList {
	cpy := make(BlockAccessList, len(b))
	for i := range b {
		cpy[i] = b[i].Copy()
	}

	return cpy
}
