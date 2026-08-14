package types

import (
	"encoding/binary"
	"math/big"

	"github.com/umbracle/fastrlp"
)

const (
	RLPSingleByteUpperLimit = 0x7f
)

type RLPMarshaler interface {
	MarshalRLPTo(dst []byte) []byte
}

type marshalRLPFunc func(ar *fastrlp.Arena) *fastrlp.Value

func MarshalRLPTo(obj marshalRLPFunc, dst []byte) []byte {
	ar := fastrlp.DefaultArenaPool.Get()
	dst = obj(ar).MarshalTo(dst)
	fastrlp.DefaultArenaPool.Put(ar)

	return dst
}

func (b *Block) MarshalRLP() []byte {
	return b.MarshalRLPTo(nil)
}

func (b *Block) MarshalRLPTo(dst []byte) []byte {
	return MarshalRLPTo(b.MarshalRLPWith, dst)
}

func (b *Block) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	vv.Set(b.Header.MarshalRLPWith(ar))

	if len(b.Transactions) == 0 {
		vv.Set(ar.NewNullArray())
	} else {
		v0 := ar.NewArray()

		for _, tx := range b.Transactions {
			if tx.Type != LegacyTx {
				v0.Set(ar.NewCopyBytes([]byte{byte(tx.Type)}))
			}

			v0.Set(tx.MarshalRLPWith(ar))
		}

		vv.Set(v0)
	}

	if len(b.Uncles) == 0 {
		vv.Set(ar.NewNullArray())
	} else {
		v1 := ar.NewArray()

		for _, uncle := range b.Uncles {
			v1.Set(uncle.MarshalRLPWith(ar))
		}

		vv.Set(v1)
	}

	if b.BlockAccessRecord != nil {
		if len(b.BlockAccessRecord) == 0 {
			vv.Set(ar.NewNullArray())
		} else {
			vv.Set(b.BlockAccessRecord.MarshalRLPWith(ar))
		}
	}

	return vv
}

// RLPSizeWithoutAccessRecord returns len(b.MarshalRLP()) as if
// b.BlockAccessRecord were nil, without allocating a buffer.
func (b *Block) RLPSizeWithoutAccessRecord() uint64 {
	var payload uint64

	payload += b.Header.RLPSize()
	payload += transactionsSectionSize(b.Transactions)
	payload += unclesSectionSize(b.Uncles)

	return rlpListSize(payload)
}

func (h *Header) MarshalRLP() []byte {
	return h.MarshalRLPTo(nil)
}

func (h *Header) MarshalRLPTo(dst []byte) []byte {
	return MarshalRLPTo(h.MarshalRLPWith, dst)
}

// MarshalRLPWith marshals the header to RLP with a specific fastrlp.Arena
func (h *Header) MarshalRLPWith(arena *fastrlp.Arena) *fastrlp.Value {
	vv := arena.NewArray()

	vv.Set(arena.NewCopyBytes(h.ParentHash.Bytes()))
	vv.Set(arena.NewCopyBytes(h.Sha3Uncles.Bytes()))
	vv.Set(arena.NewCopyBytes(h.Miner))
	vv.Set(arena.NewCopyBytes(h.StateRoot.Bytes()))
	vv.Set(arena.NewCopyBytes(h.TxRoot.Bytes()))
	vv.Set(arena.NewCopyBytes(h.ReceiptsRoot.Bytes()))
	vv.Set(arena.NewCopyBytes(h.LogsBloom[:]))

	vv.Set(arena.NewUint(h.Difficulty))
	vv.Set(arena.NewUint(h.Number))
	vv.Set(arena.NewUint(h.GasLimit))
	vv.Set(arena.NewUint(h.GasUsed))
	vv.Set(arena.NewUint(h.Timestamp))

	vv.Set(arena.NewCopyBytes(h.ExtraData))
	vv.Set(arena.NewCopyBytes(h.MixHash.Bytes()))
	vv.Set(arena.NewCopyBytes(h.Nonce[:]))

	vv.Set(arena.NewUint(h.BaseFee))

	if h.BlockAccessRecordHash != ZeroHash {
		vv.Set(arena.NewCopyBytes(h.BlockAccessRecordHash.Bytes()))
	}

	return vv
}

// RLPSize returns len(h.MarshalRLP()) without allocating a buffer.
// Field order and conditionals must stay in sync with Header.MarshalRLPWith.
func (h *Header) RLPSize() uint64 {
	var payload uint64

	payload += rlpFixedBytesSize(32)  // ParentHash
	payload += rlpFixedBytesSize(32)  // Sha3Uncles
	payload += rlpBytesSize(h.Miner)  // Miner (variable length)
	payload += rlpFixedBytesSize(32)  // StateRoot
	payload += rlpFixedBytesSize(32)  // TxRoot
	payload += rlpFixedBytesSize(32)  // ReceiptsRoot
	payload += rlpFixedBytesSize(256) // LogsBloom

	payload += rlpUintSize(h.Difficulty)
	payload += rlpUintSize(h.Number)
	payload += rlpUintSize(h.GasLimit)
	payload += rlpUintSize(h.GasUsed)
	payload += rlpUintSize(h.Timestamp)

	payload += rlpBytesSize(h.ExtraData)
	payload += rlpFixedBytesSize(32) // MixHash
	payload += rlpFixedBytesSize(8)  // Nonce

	payload += rlpUintSize(h.BaseFee)

	// Optional field, only encoded when set — mirrors MarshalRLPWith.
	if h.BlockAccessRecordHash != ZeroHash {
		payload += rlpFixedBytesSize(32)
	}

	return rlpListSize(payload)
}

func (r Receipts) MarshalRLPTo(dst []byte) []byte {
	return MarshalRLPTo(r.MarshalRLPWith, dst)
}

func (r *Receipts) MarshalRLPWith(a *fastrlp.Arena) *fastrlp.Value {
	vv := a.NewArray()

	for _, rr := range *r {
		if !rr.IsLegacyTx() {
			vv.Set(a.NewCopyBytes([]byte{byte(rr.TransactionType)}))
		}

		vv.Set(rr.MarshalRLPWith(a))
	}

	return vv
}

func (r *Receipt) MarshalRLP() []byte {
	return r.MarshalRLPTo(nil)
}

func (r *Receipt) MarshalRLPTo(dst []byte) []byte {
	if !r.IsLegacyTx() {
		dst = append(dst, byte(r.TransactionType))
	}

	return MarshalRLPTo(r.MarshalRLPWith, dst)
}

// MarshalRLPWith marshals a receipt with a specific fastrlp.Arena
func (r *Receipt) MarshalRLPWith(a *fastrlp.Arena) *fastrlp.Value {
	vv := a.NewArray()

	if r.Status != nil &&
		(len(r.Root) == 0 || r.Root == ZeroHash) {
		vv.Set(a.NewUint(uint64(*r.Status)))
	} else {
		vv.Set(a.NewCopyBytes(r.Root[:]))
	}

	vv.Set(a.NewUint(r.CumulativeGasUsed))
	vv.Set(a.NewCopyBytes(r.LogsBloom[:]))
	vv.Set(r.MarshalLogsWith(a))

	return vv
}

// MarshalLogsWith marshals the logs of the receipt to RLP with a specific fastrlp.Arena
func (r *Receipt) MarshalLogsWith(a *fastrlp.Arena) *fastrlp.Value {
	if len(r.Logs) == 0 {
		// There are no receipts, write the RLP null array entry
		return a.NewNullArray()
	}

	logs := a.NewArray()

	for _, l := range r.Logs {
		logs.Set(l.MarshalRLPWith(a))
	}

	return logs
}

func (l *Log) MarshalRLPWith(a *fastrlp.Arena) *fastrlp.Value {
	v := a.NewArray()
	v.Set(a.NewCopyBytes(l.Address.Bytes()))

	topics := a.NewArray()
	for _, t := range l.Topics {
		topics.Set(a.NewCopyBytes(t.Bytes()))
	}

	v.Set(topics)
	v.Set(a.NewCopyBytes(l.Data))

	return v
}

func (t *Transaction) MarshalRLP() []byte {
	return t.MarshalRLPTo(nil)
}

func (t *Transaction) MarshalRLPTo(dst []byte) []byte {
	if t.Type != LegacyTx {
		dst = append(dst, byte(t.Type))
	}

	return MarshalRLPTo(t.MarshalRLPWith, dst)
}

// MarshalRLPWith marshals the transaction to RLP with a specific fastrlp.Arena
// Be careful! This function does not serialize tx type as a first byte.
// Use MarshalRLP/MarshalRLPTo in most cases
func (t *Transaction) MarshalRLPWith(arena *fastrlp.Arena) *fastrlp.Value {
	vv := arena.NewArray()

	// Check Transaction1559Payload there https://eips.ethereum.org/EIPS/eip-1559#specification
	if t.Type == DynamicFeeTx {
		vv.Set(arena.NewBigInt(t.ChainID))
	}

	vv.Set(arena.NewUint(t.Nonce))

	if t.Type == DynamicFeeTx {
		// Add EIP-1559 related fields.
		// For non-dynamic-fee-tx gas price is used.
		vv.Set(arena.NewBigInt(t.GasTipCap))
		vv.Set(arena.NewBigInt(t.GasFeeCap))
	} else {
		vv.Set(arena.NewBigInt(t.GasPrice))
	}

	vv.Set(arena.NewUint(t.Gas))

	// Address may be empty
	if t.To != nil {
		vv.Set(arena.NewCopyBytes(t.To.Bytes()))
	} else {
		vv.Set(arena.NewNull())
	}

	vv.Set(arena.NewBigInt(t.Value))
	vv.Set(arena.NewCopyBytes(t.Input))

	// Specify access list as per spec.
	// This is needed to have the same format as other EVM chains do.
	// There is no access list feature here, so it is always empty just to be compatible.
	// Check Transaction1559Payload there https://eips.ethereum.org/EIPS/eip-1559#specification
	if t.Type == DynamicFeeTx {
		vv.Set(arena.NewArray())
	}

	// signature values
	vv.Set(arena.NewBigInt(t.V))
	vv.Set(arena.NewBigInt(t.R))
	vv.Set(arena.NewBigInt(t.S))

	if t.Type == StateTx {
		vv.Set(arena.NewCopyBytes(t.From.Bytes()))
	}

	return vv
}

func (t Transactions) MarshalRLPTo(dst []byte) []byte {
	return MarshalRLPTo(t.MarshalRLPWith, dst)
}

func (t *Transactions) MarshalRLPWith(a *fastrlp.Arena) *fastrlp.Value {
	vv := a.NewArray()

	for _, tt := range *t {
		if tt.Type != LegacyTx {
			vv.Set(a.NewCopyBytes([]byte{byte(tt.Type)}))
		}

		vv.Set(tt.MarshalRLPWith(a))
	}

	return vv
}

func (t *Transaction) MarshalJournal() []byte {
	rlpBytes := t.MarshalRLP()
	result := make([]byte, 8+len(rlpBytes))
	// IsLocal is not need to be stored in journal, so it is not included in the result
	// TxPoolTime (int64, little endian) - 8 bytes
	binary.LittleEndian.PutUint64(result, uint64(t.TxPoolTime))
	// Remaining bytes - RLP-encoded transaction
	copy(result[8:], rlpBytes)

	return result
}

// RLPSize returns the RLP-encoded size of the transaction body (without the
// EIP-2718 type prefix byte, which is emitted separately by the caller).
// Field order and conditionals must stay in sync with Transaction.MarshalRLPWith.
func (t *Transaction) RLPSize() uint64 {
	var payload uint64

	// EIP-1559 dynamic-fee txs prepend ChainID.
	if t.Type == DynamicFeeTx {
		payload += rlpBigIntSize(t.ChainID)
	}

	payload += rlpUintSize(t.Nonce)

	// Dynamic-fee txs use tip/fee caps instead of a single gas price.
	if t.Type == DynamicFeeTx {
		payload += rlpBigIntSize(t.GasTipCap)
		payload += rlpBigIntSize(t.GasFeeCap)
	} else {
		payload += rlpBigIntSize(t.GasPrice)
	}

	payload += rlpUintSize(t.Gas)

	// Nil To (contract creation) encodes as the empty string (0x80).
	if t.To != nil {
		payload += rlpFixedBytesSize(20)
	} else {
		payload += 1
	}

	payload += rlpBigIntSize(t.Value)
	payload += rlpBytesSize(t.Input)

	// Access list is always emitted empty for EIP-1559 spec compatibility.
	if t.Type == DynamicFeeTx {
		payload += 1 // empty list → 0xc0
	}

	// Signature fields.
	payload += rlpBigIntSize(t.V)
	payload += rlpBigIntSize(t.R)
	payload += rlpBigIntSize(t.S)

	// StateTx carries an explicit From address after the signature.
	if t.Type == StateTx {
		payload += rlpFixedBytesSize(20)
	}

	return rlpListSize(payload)
}

// byteLen returns the number of bytes needed to represent v in big-endian
// with no leading zeros. Returns 0 for v == 0 (RLP encodes zero as an empty string).
func byteLen(v uint64) uint64 {
	var n uint64
	for v > 0 {
		n++
		v >>= 8
	}
	return n
}

// rlpUintSize returns the RLP-encoded size of a uint64, matching
// fastrlp arena.NewUint(v). Zero is encoded as the empty string (0x80).
func rlpUintSize(v uint64) uint64 {
	if v == 0 {
		return 1
	}
	if v < 0x80 {
		return 1
	}
	return 1 + byteLen(v)
}

// rlpBytesSize returns the RLP-encoded size of an arbitrary byte string,
// matching fastrlp arena.NewCopyBytes / NewBytes.
func rlpBytesSize(b []byte) uint64 {
	n := uint64(len(b))
	if n == 1 && b[0] < 0x80 {
		return 1
	}
	if n <= 55 {
		return 1 + n
	}
	return 1 + byteLen(n) + n
}

// rlpFixedBytesSize is a fast path for fixed-length byte fields where the
// single-byte (<0x80) special case can never apply (hashes, bloom, nonce, address).
func rlpFixedBytesSize(n uint64) uint64 {
	if n <= 55 {
		return 1 + n
	}
	return 1 + byteLen(n) + n
}

// rlpBigIntSize returns the RLP-encoded size of a *big.Int, matching
// fastrlp arena.NewBigInt. Nil and zero encode as the empty string (0x80).
func rlpBigIntSize(i *big.Int) uint64 {
	if i == nil || i.Sign() == 0 {
		return 1
	}
	return rlpBytesSize(i.Bytes())
}

// rlpListSize wraps a payload of the given size in an RLP list header.
func rlpListSize(payloadSize uint64) uint64 {
	if payloadSize <= 55 {
		return 1 + payloadSize
	}
	return 1 + byteLen(payloadSize) + payloadSize
}

// transactionsSectionSize mirrors the transactions slot in Block.MarshalRLPWith:
// an empty slice serializes as a null array, otherwise each non-legacy tx is
// preceded by a 1-byte type marker element inside the outer list.
func transactionsSectionSize(txs []*Transaction) uint64 {
	if len(txs) == 0 {
		return 1 // NewNullArray → 0xc0
	}

	var payload uint64
	for _, tx := range txs {
		if tx.Type != LegacyTx {
			// EIP-2718 type is always < 0x80, so it fits in a single byte.
			payload += 1
		}
		payload += tx.RLPSize()
	}

	return rlpListSize(payload)
}

// unclesSectionSize mirrors the uncles slot in Block.MarshalRLPWith.
func unclesSectionSize(uncles []*Header) uint64 {
	if len(uncles) == 0 {
		return 1 // NewNullArray → 0xc0
	}

	var payload uint64
	for _, u := range uncles {
		payload += u.RLPSize()
	}

	return rlpListSize(payload)
}
