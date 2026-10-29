// Package export produces the block records the ucl to pitau migration copies into pitau: for each
// ucl block, its header and hash as ucl stored them, its transactions, the receipts its
// re-execution produced, and the state it changed with every value before and after the block.
//
// pitau stores ucl's blocks without executing them (pitau docs/plans/ucl-migration.md §6), so a
// record must carry everything reth keeps for a block: the state changes become reth's plain state,
// the values before the block become its history (reverts), and the receipts and transactions are
// stored as they are. The record is verified against ucl's own header before it leaves this
// package: the re-executed state root, gas used, transactions root, receipts root and logs bloom
// must equal the stored header's.
package export

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/umbracle/fastrlp"
)

// FormatVersion is the record format. It is part of every record and of the cache's directory, so a
// format change starts a new cache instead of serving old records.
const FormatVersion = 2

// Record is one ucl block, as pitau stores it.
type Record struct {
	Number uint64
	// Hash is ucl's hash of the block, as ucl stored it.
	Hash types.Hash
	// Header is the header's RLP as ucl encodes it, all fields verbatim, seals included.
	Header []byte
	// Transactions are the transactions' envelopes: RLP, typed ones prefixed with their type.
	Transactions [][]byte
	Receipts     []Receipt
	// Accounts are the accounts the block changed, in the order ucl committed them.
	Accounts []AccountChange
}

// Receipt is a transaction's receipt: what reth stores for it.
type Receipt struct {
	Type              uint8
	Success           bool
	CumulativeGasUsed uint64
	Logs              []Log
}

// Log is an event a transaction emitted.
type Log struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}

// Account is an account's values outside its storage.
type Account struct {
	Nonce    uint64
	Balance  *big.Int
	CodeHash types.Hash
}

// AccountChange is an account the block changed.
type AccountChange struct {
	Address types.Address
	// Before is the account before the block; nil if it did not exist.
	Before *Account
	// After is the account after the block; nil if the block destroyed it, which also deletes its
	// whole storage.
	After *Account
	// Code is the code the block deployed for the account, if it did.
	Code []byte
	// StorageReset is set when the account exists after the block but its storage before the block
	// is gone: the block destroyed it and created it again at the same address. Storage then holds
	// every non-zero slot it has after the block, not only the ones whose value changed.
	StorageReset bool
	// Storage holds the slots the block wrote. A destroyed account lists none: its whole storage
	// is gone.
	Storage []SlotChange
}

// SlotChange is a storage slot the block wrote. Zero means empty.
type SlotChange struct {
	Slot   types.Hash
	Before types.Hash
	After  types.Hash
}

// MarshalRLP encodes the record.
func (r *Record) MarshalRLP() []byte {
	ar := &fastrlp.Arena{}
	v := ar.NewArray()

	v.Set(ar.NewUint(FormatVersion))
	v.Set(ar.NewUint(r.Number))
	v.Set(ar.NewCopyBytes(r.Hash.Bytes()))
	v.Set(ar.NewCopyBytes(r.Header))

	txs := ar.NewArray()
	for _, tx := range r.Transactions {
		txs.Set(ar.NewCopyBytes(tx))
	}

	v.Set(txs)

	receipts := ar.NewArray()
	for _, receipt := range r.Receipts {
		receipts.Set(receipt.marshalWith(ar))
	}

	v.Set(receipts)

	accounts := ar.NewArray()
	for _, account := range r.Accounts {
		accounts.Set(account.marshalWith(ar))
	}

	v.Set(accounts)

	return v.MarshalTo(nil)
}

func (r *Receipt) marshalWith(ar *fastrlp.Arena) *fastrlp.Value {
	v := ar.NewArray()
	v.Set(ar.NewUint(uint64(r.Type)))
	v.Set(ar.NewBool(r.Success))
	v.Set(ar.NewUint(r.CumulativeGasUsed))

	logs := ar.NewArray()

	for _, log := range r.Logs {
		l := ar.NewArray()
		l.Set(ar.NewCopyBytes(log.Address.Bytes()))

		topics := ar.NewArray()
		for _, topic := range log.Topics {
			topics.Set(ar.NewCopyBytes(topic.Bytes()))
		}

		l.Set(topics)
		l.Set(ar.NewCopyBytes(log.Data))
		logs.Set(l)
	}

	v.Set(logs)

	return v
}

func marshalAccount(ar *fastrlp.Arena, a *Account) *fastrlp.Value {
	v := ar.NewArray()
	if a == nil {
		return v
	}

	v.Set(ar.NewUint(a.Nonce))
	v.Set(ar.NewBigInt(a.Balance))
	v.Set(ar.NewCopyBytes(a.CodeHash.Bytes()))

	return v
}

func (c *AccountChange) marshalWith(ar *fastrlp.Arena) *fastrlp.Value {
	v := ar.NewArray()
	v.Set(ar.NewCopyBytes(c.Address.Bytes()))
	v.Set(marshalAccount(ar, c.Before))
	v.Set(marshalAccount(ar, c.After))
	v.Set(ar.NewCopyBytes(c.Code))

	storage := ar.NewArray()

	for _, slot := range c.Storage {
		s := ar.NewArray()
		s.Set(ar.NewCopyBytes(slot.Slot.Bytes()))
		s.Set(ar.NewBigInt(new(big.Int).SetBytes(slot.Before.Bytes())))
		s.Set(ar.NewBigInt(new(big.Int).SetBytes(slot.After.Bytes())))
		storage.Set(s)
	}

	v.Set(storage)
	v.Set(ar.NewBool(c.StorageReset))

	return v
}

var errFormat = errors.New("malformed record")

// UnmarshalRLP decodes a record. It refuses another format version.
func (r *Record) UnmarshalRLP(input []byte) error {
	p := &fastrlp.Parser{}

	v, err := p.Parse(input)
	if err != nil {
		return err
	}

	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 7 {
		return fmt.Errorf("%w: %d fields", errFormat, len(elems))
	}

	version, err := elems[0].GetUint64()
	if err != nil {
		return err
	}

	if version != FormatVersion {
		return fmt.Errorf("%w: version %d, want %d", errFormat, version, FormatVersion)
	}

	if r.Number, err = elems[1].GetUint64(); err != nil {
		return err
	}

	if err = elems[2].GetHash(r.Hash[:]); err != nil {
		return err
	}

	if r.Header, err = elems[3].GetBytes(nil); err != nil {
		return err
	}

	txs, err := elems[4].GetElems()
	if err != nil {
		return err
	}

	r.Transactions = make([][]byte, len(txs))
	for i, tx := range txs {
		if r.Transactions[i], err = tx.GetBytes(nil); err != nil {
			return err
		}
	}

	receipts, err := elems[5].GetElems()
	if err != nil {
		return err
	}

	r.Receipts = make([]Receipt, len(receipts))
	for i, receipt := range receipts {
		if err = r.Receipts[i].unmarshal(receipt); err != nil {
			return err
		}
	}

	accounts, err := elems[6].GetElems()
	if err != nil {
		return err
	}

	r.Accounts = make([]AccountChange, len(accounts))
	for i, account := range accounts {
		if err = r.Accounts[i].unmarshal(account); err != nil {
			return err
		}
	}

	return nil
}

func (r *Receipt) unmarshal(v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 4 {
		return fmt.Errorf("%w: receipt with %d fields", errFormat, len(elems))
	}

	txType, err := elems[0].GetUint64()
	if err != nil {
		return err
	}

	if txType > 0xff {
		return fmt.Errorf("%w: transaction type %d", errFormat, txType)
	}

	r.Type = uint8(txType)

	if r.Success, err = elems[1].GetBool(); err != nil {
		return err
	}

	if r.CumulativeGasUsed, err = elems[2].GetUint64(); err != nil {
		return err
	}

	logs, err := elems[3].GetElems()
	if err != nil {
		return err
	}

	r.Logs = make([]Log, len(logs))

	for i, log := range logs {
		fields, err := log.GetElems()
		if err != nil {
			return err
		}

		if len(fields) != 3 {
			return fmt.Errorf("%w: log with %d fields", errFormat, len(fields))
		}

		if err = fields[0].GetAddr(r.Logs[i].Address[:]); err != nil {
			return err
		}

		topics, err := fields[1].GetElems()
		if err != nil {
			return err
		}

		r.Logs[i].Topics = make([]types.Hash, len(topics))
		for j, topic := range topics {
			if err = topic.GetHash(r.Logs[i].Topics[j][:]); err != nil {
				return err
			}
		}

		if r.Logs[i].Data, err = fields[2].GetBytes(nil); err != nil {
			return err
		}
	}

	return nil
}

func unmarshalAccount(v *fastrlp.Value) (*Account, error) {
	elems, err := v.GetElems()
	if err != nil {
		return nil, err
	}

	if len(elems) == 0 {
		return nil, nil
	}

	if len(elems) != 3 {
		return nil, fmt.Errorf("%w: account with %d fields", errFormat, len(elems))
	}

	a := &Account{Balance: new(big.Int)}

	if a.Nonce, err = elems[0].GetUint64(); err != nil {
		return nil, err
	}

	if err = elems[1].GetBigInt(a.Balance); err != nil {
		return nil, err
	}

	if err = elems[2].GetHash(a.CodeHash[:]); err != nil {
		return nil, err
	}

	return a, nil
}

func (c *AccountChange) unmarshal(v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 6 {
		return fmt.Errorf("%w: account change with %d fields", errFormat, len(elems))
	}

	if err = elems[0].GetAddr(c.Address[:]); err != nil {
		return err
	}

	if c.Before, err = unmarshalAccount(elems[1]); err != nil {
		return err
	}

	if c.After, err = unmarshalAccount(elems[2]); err != nil {
		return err
	}

	if c.Code, err = elems[3].GetBytes(nil); err != nil {
		return err
	}

	if len(c.Code) == 0 {
		c.Code = nil
	}

	slots, err := elems[4].GetElems()
	if err != nil {
		return err
	}

	c.Storage = make([]SlotChange, len(slots))

	for i, slot := range slots {
		fields, err := slot.GetElems()
		if err != nil {
			return err
		}

		if len(fields) != 3 {
			return fmt.Errorf("%w: slot with %d fields", errFormat, len(fields))
		}

		if err = fields[0].GetHash(c.Storage[i].Slot[:]); err != nil {
			return err
		}

		for j, dst := range []*types.Hash{&c.Storage[i].Before, &c.Storage[i].After} {
			value := new(big.Int)
			if err = fields[1+j].GetBigInt(value); err != nil {
				return err
			}

			if value.BitLen() > 256 {
				return fmt.Errorf("%w: slot value over 32 bytes", errFormat)
			}

			*dst = types.BytesToHash(value.Bytes())
		}
	}

	if c.StorageReset, err = elems[5].GetBool(); err != nil {
		return err
	}

	if c.StorageReset && c.After == nil {
		return fmt.Errorf("%w: a destroyed account with its storage reset", errFormat)
	}

	return nil
}
