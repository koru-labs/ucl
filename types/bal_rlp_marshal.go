package types

import (
	"github.com/umbracle/fastrlp"
)

func (b BlockAccessListEncoded) MarshalRLP() []byte {
	return b.MarshalRLPTo(nil)
}

func (b BlockAccessListEncoded) MarshalRLPTo(dst []byte) []byte {
	return MarshalRLPTo(b.MarshalRLPWith, dst)
}

func (b BlockAccessListEncoded) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	if len(b) == 0 {
		return ar.NewNullArray()
	}

	vv := ar.NewArray()
	for i := range b {
		vv.Set(b[i].MarshalRLPWith(ar))
	}

	return vv
}

func (aa *accountAccessEncoded) MarshalRLP() []byte {
	return aa.MarshalRLPTo(nil)
}

func (aa *accountAccessEncoded) MarshalRLPTo(dst []byte) []byte {
	return MarshalRLPTo(aa.MarshalRLPWith, dst)
}

func (aa *accountAccessEncoded) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()

	vv.Set(ar.NewCopyBytes(aa.Address.Bytes()))

	if len(aa.StorageChanges) == 0 {
		vv.Set(ar.NewNullArray())
	} else {
		scArr := ar.NewArray()
		for i := range aa.StorageChanges {
			scArr.Set(aa.StorageChanges[i].MarshalRLPWith(ar))
		}
		vv.Set(scArr)
	}

	if len(aa.BalanceChanges) == 0 {
		vv.Set(ar.NewNullArray())
	} else {
		bcArr := ar.NewArray()
		for i := range aa.BalanceChanges {
			bcArr.Set(aa.BalanceChanges[i].MarshalRLPWith(ar))
		}
		vv.Set(bcArr)
	}

	if len(aa.NonceChanges) == 0 {
		vv.Set(ar.NewNullArray())
	} else {
		ncArr := ar.NewArray()
		for i := range aa.NonceChanges {
			ncArr.Set(aa.NonceChanges[i].MarshalRLPWith(ar))
		}
		vv.Set(ncArr)
	}

	if len(aa.CodeChanges) == 0 {
		vv.Set(ar.NewNullArray())
	} else {
		ccArr := ar.NewArray()
		for i := range aa.CodeChanges {
			ccArr.Set(aa.CodeChanges[i].MarshalRLPWith(ar))
		}
		vv.Set(ccArr)
	}

	return vv
}

func (sc *slotChanges) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	vv.Set(ar.NewCopyBytes(sc.Slot.Bytes()))

	writes := ar.NewArray()
	for i := range sc.SlotChanges {
		writes.Set(sc.SlotChanges[i].MarshalRLPWith(ar))
	}
	vv.Set(writes)

	return vv
}

func (sw *storageWrite) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	vv.Set(ar.NewUint(uint64(sw.TxIndex)))
	vv.Set(ar.NewCopyBytes(sw.PostValue.Bytes()))

	return vv
}

func (bc *balanceChange) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	vv.Set(ar.NewUint(uint64(bc.TxIndex)))
	vv.Set(ar.NewBigInt(bc.PostBalance))

	return vv
}

func (nc *nonceChange) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	vv.Set(ar.NewUint(uint64(nc.TxIndex)))
	vv.Set(ar.NewUint(nc.PostNonce))

	return vv
}

func (cc *codeChange) MarshalRLPWith(ar *fastrlp.Arena) *fastrlp.Value {
	vv := ar.NewArray()
	vv.Set(ar.NewUint(uint64(cc.TxIndex)))
	vv.Set(ar.NewCopyBytes(cc.NewCode))

	return vv
}
