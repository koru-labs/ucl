package e2e

import (
	"github.com/0xPolygon/polygon-edge/consensus/polybft/contractsapi/artifact"
	"github.com/0xPolygon/polygon-edge/txrelayer"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/wallet"
)

func ABITransaction(
	relayer txrelayer.TxRelayer,
	key *wallet.Key,
	artifact *artifact.Artifact,
	contractAddress ethgo.Address,
	method string,
	params ...interface{}) (*ethgo.Receipt, error) {
	input, err := artifact.Abi.GetMethod(method).Encode(params)
	if err != nil {
		return nil, err
	}

	tx := &ethgo.Transaction{
		Type:  ethgo.TransactionLegacy,
		To:    &contractAddress,
		Input: input,
	}

	return relayer.SendTransaction(tx, key)
}

