package types

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	"github.com/valyala/fastjson"
)

var defaultPool fastjson.ParserPool

// UnmarshalJSON implements the unmarshal interface
func (b *Block) UnmarshalJSON(buf []byte) error {
	p := defaultPool.Get()
	defer defaultPool.Put(p)

	v, err := p.Parse(string(buf))
	if err != nil {
		return err
	}

	// header
	b.Header = &Header{}
	if err := b.Header.unmarshalJSON(v); err != nil {
		return err
	}

	// transactions
	b.Transactions = b.Transactions[:0]

	elems := v.GetArray("transactions")
	if len(elems) != 0 && elems[0].Type() != fastjson.TypeString {
		for _, elem := range elems {
			txn := new(Transaction)
			if err := txn.UnmarshalJSONWith(elem); err != nil {
				return err
			}

			b.Transactions = append(b.Transactions, txn)
		}
	}

	// uncles
	b.Uncles = b.Uncles[:0]

	uncles := v.GetArray("uncles")
	if len(uncles) != 0 && uncles[0].Type() != fastjson.TypeString {
		for _, elem := range uncles {
			h := new(Header)
			if err := h.unmarshalJSON(elem); err != nil {
				return err
			}

			b.Uncles = append(b.Uncles, h)
		}
	}

	return nil
}

func (h *Header) UnmarshalJSON(buf []byte) error {
	p := defaultPool.Get()
	defer defaultPool.Put(p)

	v, err := p.Parse(string(buf))
	if err != nil {
		return err
	}

	return h.unmarshalJSON(v)
}

func (h *Header) unmarshalJSON(v *fastjson.Value) error {
	var err error

	if h.Hash, err = unmarshalJSONHash(v, "hash"); err != nil {
		return err
	}

	if h.ParentHash, err = unmarshalJSONHash(v, "parentHash"); err != nil {
		return err
	}

	if h.Sha3Uncles, err = unmarshalJSONHash(v, "sha3Uncles"); err != nil {
		return err
	}

	if h.TxRoot, err = unmarshalJSONHash(v, "transactionsRoot"); err != nil {
		return err
	}

	if h.StateRoot, err = unmarshalJSONHash(v, "stateRoot"); err != nil {
		return err
	}

	if h.ReceiptsRoot, err = unmarshalJSONHash(v, "receiptsRoot"); err != nil {
		return err
	}

	if h.Miner, err = unmarshalJSONBytes(v, "miner"); err != nil {
		return err
	}

	if h.Number, err = unmarshalJSONUint64(v, "number"); err != nil {
		return err
	}

	if h.GasLimit, err = unmarshalJSONUint64(v, "gasLimit"); err != nil {
		return err
	}

	if h.GasUsed, err = unmarshalJSONUint64(v, "gasUsed"); err != nil {
		return err
	}

	if h.MixHash, err = unmarshalJSONHash(v, "mixHash"); err != nil {
		return err
	}

	if err = unmarshalJSONNonce(&h.Nonce, v, "nonce"); err != nil {
		return err
	}

	if h.Timestamp, err = unmarshalJSONUint64(v, "timestamp"); err != nil {
		return err
	}

	if h.Difficulty, err = unmarshalJSONUint64(v, "difficulty"); err != nil {
		return err
	}

	if h.ExtraData, err = unmarshalJSONBytes(v, "extraData"); err != nil {
		return err
	}

	if h.BaseFee, err = unmarshalJSONUint64(v, "baseFee"); err != nil {
		if err.Error() != "field 'baseFee' not found" {
			return err
		}
	}

	return nil
}

// UnmarshalJSON implements the unmarshal interface
func (t *Transaction) UnmarshalJSON(buf []byte) error {
	p := defaultPool.Get()
	defer defaultPool.Put(p)

	v, err := p.Parse(string(buf))
	if err != nil {
		return err
	}

	return t.UnmarshalJSONWith(v)
}

func (t *Transaction) UnmarshalJSONWith(v *fastjson.Value) error {
	if hasKey(v, "type") {
		txnType, err := unmarshalJSONUint64(v, "type")
		if err != nil {
			return err
		}

		t.Type = TxType(txnType)
	} else {
		if hasKey(v, "chainId") {
			if hasKey(v, "maxFeePerGas") {
				t.Type = DynamicFeeTx
			} /*else {
				t.Type = TransactionAccessList
			}*/
		} else {
			t.Type = LegacyTx
		}
	}

	var err error
	if t.Hash, err = unmarshalJSONHash(v, "hash"); err != nil {
		return err
	}

	if t.From, err = unmarshalJSONAddr(v, "from"); err != nil {
		return err
	}

	if t.Type == DynamicFeeTx {
		if t.GasTipCap, err = unmarshalJSONBigInt(v, "maxPriorityFeePerGas"); err != nil {
			return err
		}

		if t.GasFeeCap, err = unmarshalJSONBigInt(v, "maxFeePerGas"); err != nil {
			return err
		}
	} else if t.Type == LegacyTx /*|| t.Type == TransactionAccessList*/ {
		if t.GasPrice, err = unmarshalJSONBigInt(v, "gasPrice"); err != nil {
			return err
		}
	}

	if t.Input, err = unmarshalJSONBytes(v, "input"); err != nil {
		return err
	}

	if t.Value, err = unmarshalJSONBigInt(v, "value"); err != nil {
		return err
	}

	if t.Nonce, err = unmarshalJSONUint64(v, "nonce"); err != nil {
		return err
	}

	// Do not decode 'to' if it doesn't exist.
	if hasKey(v, "to") {
		if v.Get("to").String() != "null" {
			var to Address
			if to, err = unmarshalJSONAddr(v, "to"); err != nil {
				return err
			}

			t.To = &to
		}
	}

	if t.V, err = unmarshalJSONBigInt(v, "v"); err != nil {
		return err
	}

	if t.R, err = unmarshalJSONBigInt(v, "r"); err != nil {
		return err
	}

	if t.S, err = unmarshalJSONBigInt(v, "s"); err != nil {
		return err
	}

	if t.Type == DynamicFeeTx /*|| t.Type == TransactionAccessList*/ {
		if t.ChainID, err = unmarshalJSONBigInt(v, "chainId"); err != nil {
			return err
		}
		// if isKeySet(v, "accessList") {
		// 	if err := t.AccessList.unmarshalJSON(v.Get("accessList")); err != nil {
		// 		return err
		// 	}
		// }
	}

	if t.Gas, err = unmarshalJSONUint64(v, "gas"); err != nil {
		return err
	}

	return nil
}

// UnmarshalJSON implements the unmarshal interface
func (r *Receipt) UnmarshalJSON(buf []byte) error {
	p := defaultPool.Get()
	defer defaultPool.Put(p)

	v, err := p.Parse(string(buf))
	if err != nil {
		return nil
	}

	if hasKey(v, "contractAddress") {
		contractAddr, err := unmarshalJSONAddr(v, "contractAddress")
		if err != nil {
			return err
		}

		r.ContractAddress = &contractAddr
	}

	if r.TxHash, err = unmarshalJSONHash(v, "transactionHash"); err != nil {
		return err
	}

	if r.GasUsed, err = unmarshalJSONUint64(v, "gasUsed"); err != nil {
		return err
	}

	if r.CumulativeGasUsed, err = unmarshalJSONUint64(v, "cumulativeGasUsed"); err != nil {
		return err
	}

	if err = unmarshalJSONBloom(&r.LogsBloom, v, "logsBloom"); err != nil {
		return err
	}

	if r.Root, err = unmarshalJSONHash(v, "root"); err != nil {
		return err
	}

	if hasKey(v, "status") {
		// post-byzantium fork
		status, err := unmarshalJSONUint64(v, "status")
		if err != nil {
			return err
		}

		r.SetStatus(ReceiptStatus(status))
	}

	// logs
	r.Logs = r.Logs[:0]

	for _, elem := range v.GetArray("logs") {
		log := new(Log)
		if err := log.unmarshalJSON(elem); err != nil {
			return err
		}

		r.Logs = append(r.Logs, log)
	}

	return nil
}

// UnmarshalJSON implements the unmarshal interface
func (r *Log) UnmarshalJSON(buf []byte) error {
	p := defaultPool.Get()
	defer defaultPool.Put(p)

	v, err := p.Parse(string(buf))
	if err != nil {
		return nil
	}

	return r.unmarshalJSON(v)
}

func (r *Log) unmarshalJSON(v *fastjson.Value) error {
	var err error

	if r.Address, err = unmarshalJSONAddr(v, "address"); err != nil {
		return err
	}

	if r.Data, err = unmarshalJSONBytes(v, "data"); err != nil {
		return err
	}

	r.Topics = r.Topics[:0]

	for _, topic := range v.GetArray("topics") {
		b, err := topic.StringBytes()
		if err != nil {
			return err
		}

		var t Hash
		if err := t.UnmarshalText(b); err != nil {
			return err
		}

		r.Topics = append(r.Topics, t)
	}

	return nil
}

func unmarshalJSONHash(v *fastjson.Value, key string) (Hash, error) {
	hash := Hash{}

	b := v.GetStringBytes(key)
	if len(b) == 0 {
		return ZeroHash, fmt.Errorf("field '%s' not found", key)
	}

	err := hash.UnmarshalText(b)

	return hash, err
}

func unmarshalJSONAddr(v *fastjson.Value, key string) (Address, error) {
	b := v.GetStringBytes(key)
	if len(b) == 0 {
		return ZeroAddress, fmt.Errorf("field '%s' not found", key)
	}

	a := Address{}
	err := a.UnmarshalText(b)

	return a, err
}

func unmarshalJSONBytes(v *fastjson.Value, key string, bits ...int) ([]byte, error) {
	vv := v.Get(key)
	if vv == nil {
		return nil, fmt.Errorf("field '%s' not found", key)
	}

	str := vv.String()
	str = strings.Trim(str, "\"")

	if !strings.HasPrefix(str, "0x") {
		return nil, fmt.Errorf("field '%s' does not have 0x prefix: '%s'", key, str)
	}

	str = str[2:]
	if len(str)%2 != 0 {
		str = "0" + str
	}

	buf, err := hex.DecodeString(str)
	if err != nil {
		return nil, err
	}

	if len(bits) > 0 && bits[0] != len(buf) {
		return nil, fmt.Errorf("field '%s' invalid length, expected %d but found %d: %s", key, bits[0], len(buf), str)
	}

	return buf, nil
}

func unmarshalJSONUint64(v *fastjson.Value, key string) (uint64, error) {
	vv := v.Get(key)
	if vv == nil {
		return 0, fmt.Errorf("field '%s' not found", key)
	}

	str := vv.String()
	str = strings.Trim(str, "\"")

	return common.ParseUint64orHex(&str)
}

func unmarshalJSONBigInt(v *fastjson.Value, key string) (*big.Int, error) {
	vv := v.Get(key)
	if vv == nil {
		return nil, fmt.Errorf("field '%s' not found", key)
	}

	str := vv.String()
	str = strings.Trim(str, "\"")

	return common.ParseUint256orHex(&str)
}

func unmarshalJSONNonce(n *Nonce, v *fastjson.Value, key string) error {
	b := v.GetStringBytes(key)
	if len(b) == 0 {
		return fmt.Errorf("field '%s' not found", key)
	}

	return unmarshalTextByte(n[:], b, 8)
}

func unmarshalJSONBloom(bloom *Bloom, v *fastjson.Value, key string) error {
	b := v.GetStringBytes(key)
	if len(b) == 0 {
		return fmt.Errorf("field '%s' not found", key)
	}

	return unmarshalTextByte(bloom[:], b, BloomByteLength)
}

func unmarshalTextByte(dst, src []byte, size int) error {
	str := string(src)
	str = strings.Trim(str, "\"")

	b, err := hex.DecodeHex(str)
	if err != nil {
		return err
	}

	if len(b) != size {
		return fmt.Errorf("length %d is not correct, expected %d", len(b), size)
	}

	copy(dst, b)

	return nil
}

// hasKey is a helper function for checking if given key exists in json
func hasKey(v *fastjson.Value, key string) bool {
	value := v.Get(key)

	return value != nil && value.Type() != fastjson.TypeNull
}
