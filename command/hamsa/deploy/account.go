package hamsadeploy

import (
	"encoding/hex"

	"github.com/Ethernal-Tech/ethgo/wallet"
)

type account struct {
	Owner    string `json:"Owner"`
	OwnerKey string `json:"OwnerKey"`

	MasterMinter    string `json:"MasterMinter"`
	MasterMinterKey string `json:"MasterMinterKey"` // not in original json

	Pauser    string `json:"Pauser"`
	PauserKey string `json:"PauserKey"` // not in original json

	BlackLister    string `json:"BlackLister"`
	BlackListerKey string `json:"BlackListerKey"` // not in original json

	Minter    string `json:"Minter"`
	MinterKey string `json:"MinterKey"`

	Minter2    string `json:"Minter2"`
	Minter2Key string `json:"Minter2Key"`

	Minter3    string `json:"Minter3"`
	Minter3Key string `json:"Minter3Key"`

	BlockedAccount string `json:"BlockedAccount"`

	Spender1    string `json:"Spender1"`
	Spender1Key string `json:"Spender1Key"`

	To1           string `json:"To1"`
	To1PrivateKey string `json:"To1PrivateKey"`

	To2           string `json:"To2"`
	To2PrivateKey string `json:"To2PrivateKey"`
}

func newAccount() *account {
	accounts := make([]struct{ key, addr string }, 11)

	for i := range accounts {
		key, addr := getData()
		accounts[i] = struct{ key, addr string }{key, addr}
	}

	return &account{
		Owner:           accounts[0].addr,
		OwnerKey:        accounts[0].key,
		MasterMinter:    accounts[1].addr,
		MasterMinterKey: accounts[1].key,
		Pauser:          accounts[2].addr,
		PauserKey:       accounts[2].key,
		BlackLister:     accounts[3].addr,
		BlackListerKey:  accounts[3].key,
		Minter:          accounts[4].addr,
		MinterKey:       accounts[4].key,
		Minter2:         accounts[5].addr,
		Minter2Key:      accounts[5].key,
		Minter3:         accounts[6].addr,
		Minter3Key:      accounts[6].key,
		BlockedAccount:  accounts[7].addr,
		Spender1:        accounts[8].addr,
		Spender1Key:     accounts[8].key,
		To1:             accounts[9].addr,
		To1PrivateKey:   accounts[9].key,
		To2:             accounts[10].addr,
		To2PrivateKey:   accounts[10].key,
	}
}

func getData() (string, string) {
	key, err := wallet.GenerateKey()
	if err != nil {
		return "", ""
	}

	pkStr, err := key.MarshallPrivateKey()
	if err != nil {
		return "", ""
	}

	return key.Address().String(), hex.EncodeToString(pkStr)
}
