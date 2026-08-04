package bal

import (
	"fmt"
	"math/big"

	"github.com/0xPolygon/polygon-edge/types"
	"github.com/umbracle/fastrlp"
)

func (b *BlockAccessList) UnmarshalRLP(input []byte) error {
	return types.UnmarshalRlp(b.unmarshalRLPFrom, input)
}

func (b *BlockAccessList) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	accounts := make([]AccountAccess, len(elems))
	for i, elem := range elems {
		if err := accounts[i].unmarshalRLPFrom(p, elem); err != nil {
			return err
		}
	}

	*b = accounts

	return nil
}

func (aa *AccountAccess) UnmarshalRLP(input []byte) error {
	return types.UnmarshalRlp(aa.unmarshalRLPFrom, input)
}

func (aa *AccountAccess) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
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

	aa.StorageChanges = make([]SlotChanges, len(scElems))
	for i, scElem := range scElems {
		if err = aa.StorageChanges[i].unmarshalRLPFrom(p, scElem); err != nil {
			return err
		}
	}

	srElems, err := elems[2].GetElems()
	if err != nil {
		return err
	}

	aa.StorageReads = make([]types.Hash, len(srElems))
	for i, srElem := range srElems {
		if err = srElem.GetHash(aa.StorageReads[i][:]); err != nil {
			return err
		}
	}

	bcElems, err := elems[3].GetElems()
	if err != nil {
		return err
	}

	aa.BalanceChanges = make([]BalanceChange, len(bcElems))
	for i, bcElem := range bcElems {
		if err = aa.BalanceChanges[i].unmarshalRLPFrom(p, bcElem); err != nil {
			return err
		}
	}

	ncElems, err := elems[4].GetElems()
	if err != nil {
		return err
	}

	aa.NonceChanges = make([]NonceChange, len(ncElems))
	for i, ncElem := range ncElems {
		if err = aa.NonceChanges[i].unmarshalRLPFrom(p, ncElem); err != nil {
			return err
		}
	}

	ccElems, err := elems[5].GetElems()
	if err != nil {
		return err
	}

	aa.CodeChanges = make([]CodeChange, len(ccElems))
	for i, ccElem := range ccElems {
		if err = aa.CodeChanges[i].unmarshalRLPFrom(p, ccElem); err != nil {
			return err
		}
	}

	return nil
}

func (sc *SlotChanges) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
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

	sc.SlotChanges = make([]StorageWrite, len(writeElems))
	for i, we := range writeElems {
		if err = sc.SlotChanges[i].unmarshalRLPFrom(p, we); err != nil {
			return err
		}
	}

	return nil
}

func (sw *StorageWrite) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode storage write, expected 2 but found %d", len(elems))
	}

	idx, err := elems[0].GetUint64()
	if err != nil {
		return err
	}

	sw.BlockAccessIndex = uint32(idx)

	return elems[1].GetHash(sw.PostValue[:])
}

func (bc *BalanceChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode balance change, expected 2 but found %d", len(elems))
	}

	idx, err := elems[0].GetUint64()
	if err != nil {
		return err
	}

	bc.BlockAccessIndex = uint32(idx)

	bc.PostBalance = new(big.Int)

	return elems[1].GetBigInt(bc.PostBalance)
}

func (nc *NonceChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode nonce change, expected 2 but found %d", len(elems))
	}

	idx, err := elems[0].GetUint64()
	if err != nil {
		return err
	}

	nc.BlockAccessIndex = uint32(idx)

	nc.PostNonce, err = elems[1].GetUint64()

	return err
}

func (cc *CodeChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 2 {
		return fmt.Errorf("incorrect number of elements to decode code change, expected 2 but found %d", len(elems))
	}

	idx, err := elems[0].GetUint64()
	if err != nil {
		return err
	}

	cc.BlockAccessIndex = uint32(idx)

	cc.NewCode, err = elems[1].GetBytes(cc.NewCode[:0])

	return err
}
