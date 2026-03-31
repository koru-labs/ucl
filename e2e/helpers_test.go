package e2e

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/consensus/polybft/contractsapi/artifact"
	"github.com/0xPolygon/polygon-edge/secrets"
	"github.com/0xPolygon/polygon-edge/secrets/helper"
	"github.com/0xPolygon/polygon-edge/secrets/local"
	"github.com/0xPolygon/polygon-edge/txrelayer"
	"github.com/Ethernal-Tech/ethgo"
	"github.com/Ethernal-Tech/ethgo/wallet"
	"github.com/hashicorp/go-hclog"
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

// ReadValidatorBLSKey reads the BLS public key for a validator at the given dataDir
func ReadValidatorBLSKey(dataDir string) (string, error) {
	sm, err := local.SecretsManagerFactory(
		nil,
		&secrets.SecretsManagerParams{
			Logger: hclog.NewNullLogger(),
			Extra: map[string]interface{}{
				secrets.Path: dataDir,
			},
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to create secrets manager for %s: %w", dataDir, err)
	}

	return helper.LoadBLSPublicKey(sm)
}
