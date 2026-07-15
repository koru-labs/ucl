//nolint:all
package blockstm

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/types"
)

const FlagDone = 0
const FlagEstimate = 1

const addressType = 1
const stateType = 2
const subpathType = 3

const KeyLength = types.AddressLength + types.HashLength + 2

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
	switch k[KeyLength-1] {
	case stateType:
		return fmt.Sprintf("StateKey: %s -> %s", k.GetAddress(), k.GetStateKey())
	case subpathType:
		subPath := ""
		switch k.GetSubpath() {
		case 1:
			subPath = "Balance"
		case 2:
			subPath = "Nonce"
		case 3:
			subPath = "Code"
		case 4:
			subPath = "Suicide"
		}

		return fmt.Sprintf("SubPath: %s -> %v", k.GetAddress(), subPath)
	default:
		return fmt.Sprintf("AddressKey: %s", k.GetAddress())
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
	k := newKey(addr, hash, 0, stateType)
	if !k.IsState() {
		panic(fmt.Errorf("key is not a state key"))
	}

	return k
}

func NewSubpathKey(addr types.Address, subpath byte) Key {
	return newKey(addr, types.Hash{}, subpath, subpathType)
}
