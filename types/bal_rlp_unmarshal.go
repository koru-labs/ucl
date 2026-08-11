package types

import (
	"fmt"
	"math/big"

	"github.com/umbracle/fastrlp"
)

func (b *BlockAccessRecord) UnmarshalRLP(input []byte) error {
	return UnmarshalRlp(b.unmarshalRLPFrom, input)
}

func (b *BlockAccessRecord) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	accounts := make([]AccountAccessRecord, len(elems))
	for i, elem := range elems {
		if err := accounts[i].unmarshalRLPFrom(p, elem); err != nil {
			return err
		}
	}

	*b = accounts

	return nil
}

func (aa *AccountAccessRecord) UnmarshalRLP(input []byte) error {
	return UnmarshalRlp(aa.unmarshalRLPFrom, input)
}

func (aa *AccountAccessRecord) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
	elems, err := v.GetElems()
	if err != nil {
		return err
	}

	if len(elems) != 5 {
		return fmt.Errorf("incorrect number of elements to decode account access, expected 5 but found %d", len(elems))
	}

	if err = elems[0].GetAddr(aa.Address[:]); err != nil {
		return err
	}

	scElems, err := elems[1].GetElems()
	if err != nil {
		return err
	}

	aa.StorageChanges = make([]StorageChange, len(scElems))
	for i, scElem := range scElems {
		if err = aa.StorageChanges[i].unmarshalRLPFrom(p, scElem); err != nil {
			return err
		}
	}

	bcElems, err := elems[2].GetElems()
	if err != nil {
		return err
	}

	aa.BalanceChanges = make([]BalanceChange, len(bcElems))
	for i, bcElem := range bcElems {
		if err = aa.BalanceChanges[i].unmarshalRLPFrom(p, bcElem); err != nil {
			return err
		}
	}

	ncElems, err := elems[3].GetElems()
	if err != nil {
		return err
	}

	aa.NonceChanges = make([]NonceChange, len(ncElems))
	for i, ncElem := range ncElems {
		if err = aa.NonceChanges[i].unmarshalRLPFrom(p, ncElem); err != nil {
			return err
		}
	}

	ccElems, err := elems[4].GetElems()
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

func (sc *StorageChange) unmarshalRLPFrom(p *fastrlp.Parser, v *fastrlp.Value) error {
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

	sc.SlotChanges = make([]SlotChange, len(writeElems))
	for i, we := range writeElems {
		if err = sc.SlotChanges[i].unmarshalRLPFrom(p, we); err != nil {
			return err
		}
	}

	return nil
}

func (sw *SlotChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
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
<<<<<<< HEAD:types/bal_rlp_unmarshal.go
	sw.TxIndex = id
=======

	sw.BlockAccessIndex = uint32(idx)
>>>>>>> EIP-7928-block-access-list:types/bal/bal_rlp_unmarshal.go

	return elems[1].GetHash(sw.Value[:])
}

func (bc *BalanceChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
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
<<<<<<< HEAD:types/bal_rlp_unmarshal.go
	bc.TxIndex = id
=======

	bc.BlockAccessIndex = uint32(idx)
>>>>>>> EIP-7928-block-access-list:types/bal/bal_rlp_unmarshal.go

	bc.Balance = new(big.Int)

	return elems[1].GetBigInt(bc.Balance)
}

func (nc *NonceChange) unmarshalRLPFrom(_ *fastrlp.Parser, v *fastrlp.Value) error {
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
<<<<<<< HEAD:types/bal_rlp_unmarshal.go
	nc.TxIndex = id
=======

	nc.BlockAccessIndex = uint32(idx)
>>>>>>> EIP-7928-block-access-list:types/bal/bal_rlp_unmarshal.go

	nc.Nonce, err = elems[1].GetUint64()

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

	id, err := elems[0].GetUint64()
	if err != nil {
		return err
	}
<<<<<<< HEAD:types/bal_rlp_unmarshal.go
	cc.TxIndex = id
=======

	cc.BlockAccessIndex = uint32(idx)
>>>>>>> EIP-7928-block-access-list:types/bal/bal_rlp_unmarshal.go

	cc.Code, err = elems[1].GetBytes(cc.Code[:0])

	return err
}
