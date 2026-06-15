package state

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/types"
)

const (
	addressType byte = 1
	stateType   byte = 2
	subpathType byte = 3

	KeyLength = types.AddressLength + types.HashLength + 2

	FullPath    byte = 0
	BalancePath byte = 1
	NoncePath   byte = 2
	CodePath    byte = 3
	SuicidePath byte = 4
)

type Key [KeyLength]byte

func (k Key) IsAddress() bool {
	return k[KeyLength-1] == addressType
}

func (k Key) IsState() bool {
	return k[KeyLength-1] == stateType
}

func (k Key) IsSubpath() bool {
	return k[KeyLength-1] == subpathType
}

func (k Key) GetAddress() types.Address {
	return types.BytesToAddress(k[:types.AddressLength])
}

func (k Key) GetStateKey() types.Hash {
	return types.BytesToHash(k[types.AddressLength : KeyLength-2])
}

func (k Key) GetSubpath() byte {
	return k[KeyLength-2]
}

func (k Key) String() string {
	switch {
	case k.IsAddress():
		return fmt.Sprintf("AddressKey(%s)", k.GetAddress())
	case k.IsState():
		return fmt.Sprintf("StateKey(%s, %s)", k.GetAddress(), k.GetStateKey())
	case k.IsSubpath():
		return fmt.Sprintf("SubpathKey(%s, %d)", k.GetAddress(), k.GetSubpath())
	default:
		return "UnknownKey"
	}
}

func newKey(addr types.Address, hash types.Hash, subpath byte, keyType byte) Key {
	var k Key

	copy(k[:types.AddressLength], addr.Bytes())
	copy(k[types.AddressLength:KeyLength-2], hash.Bytes())
	k[KeyLength-2] = subpath
	k[KeyLength-1] = keyType

	return k
}

func NewAddressKey(addr types.Address) Key {
	return newKey(addr, types.Hash{}, 0, addressType)
}

func NewStateKey(addr types.Address, hash types.Hash) Key {
	return newKey(addr, hash, 0, stateType)
}

func NewSubpathKey(addr types.Address, subpath byte) Key {
	return newKey(addr, types.Hash{}, subpath, subpathType)
}

func NewGenericKey(addr types.Address, hash types.Hash, subpath byte) Key {
	var keyType byte
	//nolint:gocritic
	if hash != types.ZeroHash {
		keyType = stateType
	} else if subpath != 0 {
		keyType = subpathType
	} else {
		keyType = addressType
	}

	return newKey(addr, hash, subpath, keyType)
}
