package hamsadeploy

import (
	"encoding/hex"
	"fmt"

	"github.com/Ethernal-Tech/ethgo/wallet"
)

type accounts struct {
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

func newAccountsWithOwnerWallet(w *wallet.Key) (*accounts, error) {
	pkStr, err := w.MarshallPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}

	return newAccountsWithOwner(hex.EncodeToString(pkStr), w.Address().String()), nil
}

func newAccountsWithOwner(ownerKey, ownerAddr string) *accounts {
	wallets := make([]struct{ key, addr string }, 10)

	for i := range wallets {
		key, addr := getData()
		wallets[i] = struct{ key, addr string }{key, addr}
	}

	return &accounts{
		Owner:           ownerAddr,
		OwnerKey:        ownerKey,
		MasterMinter:    wallets[0].addr,
		MasterMinterKey: wallets[0].key,
		Pauser:          wallets[1].addr,
		PauserKey:       wallets[1].key,
		BlackLister:     wallets[2].addr,
		BlackListerKey:  wallets[2].key,
		Minter:          wallets[3].addr,
		MinterKey:       wallets[3].key,
		Minter2:         wallets[4].addr,
		Minter2Key:      wallets[4].key,
		Minter3:         wallets[5].addr,
		Minter3Key:      wallets[5].key,
		BlockedAccount:  wallets[6].addr,
		Spender1:        wallets[7].addr,
		Spender1Key:     wallets[7].key,
		To1:             wallets[8].addr,
		To1PrivateKey:   wallets[8].key,
		To2:             wallets[9].addr,
		To2PrivateKey:   wallets[9].key,
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
