package types

import (
	"fmt"
	"math/big"

	"github.com/umbracle/fastrlp"
)

func (b *BlockAccessListEncoded) UnmarshalRLP(input []byte) error {
	return UnmarshalRlp(b.unmarshalRLPFrom, input)
}

func (b *BlockAccessListEncoded) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	accounts := make([]accountAccessEncoded, len(elems))
	for i, elem := range elems {
		if err := accounts[i].unmarshalRLPFrom(p, elem); err != nil {
			return err
		}
	}

	*b = accounts

	return nil
}

func (aa *accountAccessEncoded) UnmarshalRLP(input []byte) error {
	return UnmarshalRlp(aa.unmarshalRLPFrom, input)
}

func (aa *accountAccessEncoded) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 6 {
		return fmt.Errorf("incorrect number of elements to decode account access, expected 6 but found %d", len(elems))
	}

	if err = elems[0].GetAddr(aa.Address[:]); err != nil {
		return err
	}

	scElems, err := elems[1].GetElems()
	if err != nil {
		return err
	}

	aa.StorageChanges = make([]slotChanges, len(scElems))
	for i, scElem := range scElems {
		if err = aa.StorageChanges[i].unmarshalRLPFrom(p, scElem); err != nil {
			return err
		}
	}

	bcElems, err := elems[3].GetElems()
	if err != nil {
		return err
	}

	aa.BalanceChanges = make([]balanceChange, len(bcElems))
	for i, bcElem := range bcElems {
		if err = aa.BalanceChanges[i].unmarshalRLPFrom(p, bcElem); err != nil {
			return err
		}
	}

	ncElems, err := elems[4].GetElems()
	if err != nil {
		return err
	}

	aa.NonceChanges = make([]nonceChange, len(ncElems))
	for i, ncElem := range ncElems {
		if err = aa.NonceChanges[i].unmarshalRLPFrom(p, ncElem); err != nil {
			return err
		}
	}

	ccElems, err := elems[5].GetElems()
	if err != nil {
		return err
	}

	aa.CodeChanges = make([]codeChange, len(ccElems))
	for i, ccElem := range ccElems {
		if err = aa.CodeChanges[i].unmarshalRLPFrom(p, ccElem); err != nil {
			return err
		}
	}

	return nil
}

func (sc *slotChanges) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode slot changes, expected 2 but found %d", len(elems))
	}

	if err = elems[0].GetHash(sc.Slot[:]); err != nil {
		return err
	}

	writeElems, err := elems[1].GetElems()
	if err != nil {
		return err
	}

	sc.SlotChanges = make([]storageWrite, len(writeElems))
	for i, we := range writeElems {
		if err = sc.SlotChanges[i].unmarshalRLPFrom(p, we); err != nil {
			return err
		}
	}

	return nil
}

func (sw *storageWrite) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode storage write, expected 2 but found %d", len(elems))
	}

	id, err := elems[0].GetUint64()
	if err != nil {
		return err
	}
	sw.TxIndex = id

	return elems[1].GetHash(sw.PostValue[:])
}

func (bc *balanceChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode balance change, expected 2 but found %d", len(elems))
	}

	id, err := elems[0].GetUint64()
	if err != nil {
		return err
	}
	bc.TxIndex = id

	bc.PostBalance = new(big.Int)

	return elems[1].GetBigInt(bc.PostBalance)
}

func (nc *nonceChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode nonce change, expected 2 but found %d", len(elems))
	}

	id, err := elems[0].GetUint64()
	if err != nil {
		return err
	}
	nc.TxIndex = id

	nc.PostNonce, err = elems[1].GetUint64()

	return err
}

func (cc *codeChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode code change, expected 2 but found %d", len(elems))
	}

	id, err := elems[0].GetUint64()
	if err != nil {
		return err
	}
	cc.TxIndex = id

	cc.NewCode, err = elems[1].GetBytes(cc.NewCode[:0])

	return err
}
